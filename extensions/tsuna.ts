import { readFileSync } from "node:fs";
import { join } from "node:path";
import { AgentRegistry, type ExtensionAPI, type ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { Effort } from "@oh-my-pi/pi-catalog";
import { BUILTIN_SLASH_COMMAND_RESERVED_NAMES } from "@oh-my-pi/pi-coding-agent/slash-commands/builtin-registry";
import roles from "../config/roles.json";
import commands from "../config/commands.json";
import { compressToolResults, type Compression } from "../lib/tsuna.ts";

export default function tsuna(pi: ExtensionAPI) {
  const z = pi.zod;
  const summaries = new Map<string, string>();
  const roleInfo = roles as Record<string, typeof roles.orchestrator>;

  async function selectRole(name: string, ctx: ExtensionContext) {
    const role = roleInfo[name];
    if (!role) throw new Error(`Unknown role ${name}`);
    const [selector, effort] = role.model.split(":");
    const slash = selector.indexOf("/");
    const model = ctx.modelRegistry.find(selector.slice(0, slash), selector.slice(slash + 1));
    if (!model || !await pi.setModel(model)) throw new Error(`Model unavailable for ${name}: ${selector}`);
    if (effort && ["low", "medium", "high", "xhigh", "max"].includes(effort)) pi.setThinkingLevel(effort as Effort);
    process.env.TSUNA_AGENT = name;
    pi.appendEntry("tsuna-role", { role: name });
    ctx.ui.setStatus("tsuna-role", `tsuna · ${name}`);
  }

  pi.on("session_start", (_event, ctx) => {
    summaries.clear();
    for (const entry of ctx.sessionManager.getBranch()) {
      if (ctx.agent.kind === "main" && process.env.TSUNA_AGENT_EXPLICIT !== "1" && entry.type === "custom" && (entry.customType === "tsuna-role" || entry.customType === "hoshi-role")) {
        const saved = entry.data as { role?: string };
        if (saved.role === "build") process.env.TSUNA_AGENT = "orchestrator";
        else if (saved.role && roleInfo[saved.role]) process.env.TSUNA_AGENT = saved.role;
      }
      if (entry.type === "custom" && (entry.customType === "tsuna-compress" || entry.customType === "hoshi-compress")) {
        const compression = entry.data as Compression;
        for (const id of compression.toolCallIds) summaries.set(id, compression.summary);
      }
    }
    if (ctx.agent.kind === "main") {
      const role = process.env.TSUNA_AGENT ?? "orchestrator";
      pi.appendEntry("tsuna-role", { role });
      ctx.ui.setStatus("tsuna-role", `tsuna · ${role}`);
    }
  });

  pi.on("before_agent_start", (event, ctx) => {
    if (ctx.agent.kind !== "main") return;
    const role = process.env.TSUNA_AGENT ?? "orchestrator";
    if (!roleInfo[role]) throw new Error(`Unknown primary role: ${role}`);
    const prompt = readFileSync(join(import.meta.dir, "../agent/prompts", `${role}.md`), "utf8");
    return { systemPrompt: [...event.systemPrompt, `## Tsuna role: ${role}\n${prompt}`] };
  });

  pi.registerCommand("tsuna-agent", {
    description: "Select an imported primary agent (orchestrator, scholar, plan).",
    async handler(args, ctx) {
      const name = args.trim();
      if (!name) { ctx.ui.notify(Object.entries(roleInfo).filter(([, r]) => r.mode !== "subagent").map(([n]) => n).join(", "), "info"); return; }
      if (!roleInfo[name] || roleInfo[name].mode === "subagent") throw new Error("Select a primary-capable role");
      await selectRole(name, ctx);
    },
  });

  for (const [name, command] of Object.entries(commands)) {
    const handler = async (args: string, ctx: ExtensionContext) => {
      if (roleInfo[command.agent]?.mode !== "subagent") await selectRole(command.agent, ctx);
      const body = command.body.replaceAll("$ARGUMENTS", args);
      const prompt = roleInfo[command.agent]?.mode === "subagent"
        ? `Use the ${command.agent} task agent for this explicitly requested workflow:\n\n${body}`
        : body;
      pi.sendUserMessage(prompt);
    };
    pi.registerCommand(`tsuna-${name}`, { description: command.description, handler });
    if (!BUILTIN_SLASH_COMMAND_RESERVED_NAMES.has(name)) pi.registerCommand(name, { description: command.description, handler });
  }

  pi.registerTool({
    name: "subagent_list", label: "Subagent list", description: "List this session's direct children, with live status and activity.",
    parameters: z.object({ running_only: z.boolean().optional() }), approval: "read",
    async execute(_id, raw, _signal, _update, ctx) {
      const params = raw as { running_only?: boolean };
      const children = AgentRegistry.global().list().filter(ref => ref.parentId === ctx.agent.id && (!params.running_only || ref.status === "running"));
      const rows = children.map(ref => ({ sessionID: ref.id, agent: ref.history?.agent, title: ref.displayName, status: ref.status, activity: ref.activity, lastActivity: ref.lastActivity, model: ref.history?.resolvedModel, metrics: ref.history?.metrics }));
      return { content: [{ type: "text", text: JSON.stringify(rows) }], details: { children: rows } };
    },
  });
  pi.registerTool({
    name: "subagent_stop", label: "Stop subagent", description: "Interrupt one direct child while preserving its session.",
    parameters: z.object({ sessionID: z.string(), reason: z.string().optional() }), approval: "write",
    async execute(_id, raw, _signal, _update, ctx) {
      const params = raw as { sessionID: string; reason?: string };
      const child = AgentRegistry.global().get(params.sessionID);
      if (!child || child.parentId !== ctx.agent.id) throw new Error("Can only interrupt this session's direct children");
      if (!child.session) throw new Error("Child is parked or no longer live");
      await child.session.abort();
      return { content: [{ type: "text", text: `Interrupted ${child.id}; session retained. ${params.reason ?? ""}` }], details: { sessionID: child.id } };
    },
  });
  pi.registerTool({
    name: "compress", label: "Compress tool results",
    description: "Replace selected old tool results in outgoing context with a concise accurate summary. Keeps all user instructions, assistant messages and stored history. Primary agents only; no automatic pruning.",
    parameters: z.object({ toolCallIds: z.array(z.string()).min(1), summary: z.string().min(1) }), approval: "write",
    async execute(_id, raw, _signal, _update, ctx) {
      const params = raw as Compression;
      if (ctx.agent.kind !== "main") throw new Error("Compression is only available to primary sessions");
      const known = new Set(ctx.sessionManager.getBranch().flatMap(e => e.type === "message" && e.message.role === "toolResult" ? [e.message.toolCallId] : []));
      if (params.toolCallIds.some(id => !known.has(id))) throw new Error("Unknown tool result ID; compression was not applied");
      for (const id of params.toolCallIds) summaries.set(id, params.summary);
      pi.appendEntry("tsuna-compress", params);
      return { content: [{ type: "text", text: `Compressed ${params.toolCallIds.length} tool results. User messages are unchanged.` }], details: { compressed: params.toolCallIds.length } };
    },
  });
  pi.on("context", event => summaries.size ? { messages: compressToolResults(event.messages, summaries) } : undefined);
}
