/**
 * Human command layer shared by the interactive REPL and scripted headless
 * runs. Agents can be named by id or by `@role#n` (n-th agent of that role in
 * creation order). Plain text goes to the primary agent (or steers it while it
 * is running).
 */
import type { Harness } from "../harness.ts";
import type { AgentRecord } from "../orchestration/types.ts";

export interface CommandOutput {
	kind: "info" | "error" | "agents" | "transcript" | "audit" | "mcp" | "quit";
	text: string;
	data?: unknown;
}

export const HELP = `Commands:
  <text>                    prompt the primary agent (steers it if it is running)
  /agents                   agent tree with lifecycle status and activity
  /read <agent> [n]         accepted results, outputs and the last n transcript messages
  /steer <agent> <message>  steer a running agent (queued if it is idle)
  /followup <agent> <msg>   give an agent a follow-up assignment (revives a parked one)
  /interrupt <agent>        stop the current run, keep the session
  /cancel <agent>           cancel permanently (with descendants)
  /resume <agent> [msg]     revive an idle/parked agent with its original contract
  /park <agent>             dispose an idle agent's session (revivable)
  /wait [seconds]           wait until no agent is running (default 30)
  /audit [n]                recent permission decisions
  /mcp                      MCP server states
  /root                     root session id (use with --resume)
  /help, /quit
Agents: an id, or @role#n (e.g. @explore#1).`;

export function resolveAgent(harness: Harness, ref: string): AgentRecord | undefined {
	const rt = harness.runtime;
	const m = /^@([a-z0-9-]+)(?:#(\d+))?$/.exec(ref);
	if (!m) return rt.record(ref);
	const role = m[1]!;
	const n = Number(m[2] ?? 1);
	return rt.records().filter(r => r.role === role).sort((a, b) => a.createdAt - b.createdAt || a.id.localeCompare(b.id))[n - 1];
}

export function formatAgents(records: AgentRecord[]): string {
	const rows = [...records].sort((a, b) => a.depth - b.depth || a.createdAt - b.createdAt);
	const lines = ["ID                          ROLE           PARENT                      STATUS     RESULTS  ACTIVITY"];
	for (const r of rows) {
		const results = r.results.map(x => (x.status === "success" ? "✓" : x.interrupted ? "↯" : "✗")).join("") || "-";
		const notes = [r.activity ?? r.task ?? "", r.interruptedRunId ? `(interrupted ${r.interruptedRunId}; not replayed)` : "", r.failure ? `FAILED: ${r.failure}` : ""].filter(Boolean).join(" ");
		lines.push(`${"  ".repeat(r.depth)}${r.id}`.padEnd(28) + r.role.padEnd(15) + (r.parentId ?? "-").padEnd(28) + r.status.padEnd(11) + results.padEnd(9) + notes.replace(/\s+/g, " ").slice(0, 90));
	}
	return lines.join("\n");
}

export async function runCommand(harness: Harness, line: string): Promise<CommandOutput> {
	const rt = harness.runtime;
	const human = { kind: "human" as const, rootId: rt.rootId };
	const trimmed = line.trim();
	if (!trimmed) return { kind: "info", text: "" };
	if (!trimmed.startsWith("/")) {
		const primary = rt.record(rt.primaryId)!;
		if (primary.status === "running") {
			const outcome = await rt.steerPrimary(trimmed);
			return { kind: "info", text: `steer → primary: ${outcome}` };
		}
		void rt.promptPrimary(trimmed).catch(error => harness.listeners.forEach(l => l({ type: "warning", message: `primary run failed: ${(error as Error).message}` })));
		return { kind: "info", text: "prompt → primary" };
	}
	const [command, ...rest] = trimmed.split(/\s+/);
	const arg = rest[0];
	const tail = trimmed.split(/\s+/).slice(2).join(" ");
	const need = (): AgentRecord | CommandOutput => {
		if (!arg) return { kind: "error", text: `${command} needs an agent` };
		return resolveAgent(harness, arg) ?? { kind: "error", text: `unknown agent ${arg}` };
	};
	switch (command) {
		case "/help":
			return { kind: "info", text: HELP };
		case "/quit":
		case "/exit":
			return { kind: "quit", text: "bye" };
		case "/root":
			return { kind: "info", text: rt.rootId };
		case "/agents": {
			const records = rt.list(human);
			return { kind: "agents", text: formatAgents(records), data: records.map(r => ({ id: r.id, role: r.role, parentId: r.parentId, status: r.status, results: r.results.length })) };
		}
		case "/read": {
			const target = need();
			if ("kind" in target) return target;
			const view = rt.read(human, target.id, { messages: Number(rest[1] ?? 12) });
			if (typeof view === "string") return { kind: "error", text: view };
			const r = view.record;
			const lines = [`${r.id} (${r.role}) status=${r.status} model=${r.contract.provider}/${r.contract.modelId} reasoning=${r.contract.reasoning}`, `tools: ${r.contract.tools.join(", ")}`];
			for (const res of r.results) lines.push(`result ${res.resultId} ${res.status} [delivery ${res.delivery.state}${res.delivery.via ? ` via ${res.delivery.via}` : ""}]: ${JSON.stringify(res.data ?? res.error ?? res.text ?? null)}`);
			for (const o of r.outputs) lines.push(`output #${o.seq} [${o.label}] ${JSON.stringify(o.data)}`);
			lines.push("── transcript ──");
			for (const m of view.transcript) lines.push(`${m.role.padEnd(10)} ${m.text.replace(/\n/g, "\n           ").slice(0, 600)}`);
			return { kind: "transcript", text: lines.join("\n"), data: { id: r.id, results: r.results, transcript: view.transcript } };
		}
		case "/steer":
		case "/followup": {
			const target = need();
			if ("kind" in target) return target;
			if (!tail) return { kind: "error", text: `${command} needs a message` };
			const r = await rt.send(human, target.id, tail, command === "/steer" ? "steer" : "followup");
			return { kind: r.outcome === "rejected" ? "error" : "info", text: `${command.slice(1)} → ${target.id}: ${r.outcome}${r.reason ? ` (${r.reason})` : ""}`, data: r };
		}
		case "/interrupt": {
			const target = need();
			if ("kind" in target) return target;
			const r = await rt.interrupt(human, target.id);
			return { kind: r.ok ? "info" : "error", text: r.ok ? `interrupted ${target.id} (session kept)` : `not interrupted: ${r.reason}` };
		}
		case "/cancel": {
			const target = need();
			if ("kind" in target) return target;
			const r = await rt.cancel(human, target.id);
			return { kind: r.ok ? "info" : "error", text: r.ok ? `cancelled ${r.cancelled.join(", ")}` : `not cancelled: ${r.reason}` };
		}
		case "/resume": {
			const target = need();
			if ("kind" in target) return target;
			const r = await rt.resume(human, target.id, tail || undefined);
			return { kind: r.outcome === "rejected" ? "error" : "info", text: `resume ${target.id}: ${r.outcome}${r.reason ? ` (${r.reason})` : ""}`, data: r };
		}
		case "/park": {
			const target = need();
			if ("kind" in target) return target;
			const r = await rt.park(target.id, human);
			return { kind: r.ok ? "info" : "error", text: r.ok ? `parked ${target.id}` : `not parked: ${r.reason}` };
		}
		case "/wait": {
			const seconds = Number(arg ?? 30);
			const done = await rt.quiesce(seconds * 1000);
			return { kind: done ? "info" : "error", text: done ? "idle" : `still busy after ${seconds}s` };
		}
		case "/audit": {
			const n = Number(arg ?? 20);
			const entries = harness.gate.audit.slice(-n);
			return { kind: "audit", text: entries.map(e => `${new Date(e.at).toISOString().slice(11, 19)} ${e.effect.toUpperCase().padEnd(5)} ${e.agentId} ${e.toolName} → ${e.action} ${e.resource}${e.reason ? ` (${e.source}: ${e.reason})` : ` (${e.source})`}`).join("\n"), data: entries };
		}
		case "/mcp":
			return { kind: "mcp", text: [...harness.mcp.states.values()].map(s => `${s.name} ${s.type} ${s.status}${s.error ? ` (${s.error})` : ""} tools=${s.tools.length}`).join("\n") || "no MCP servers configured" };
		default:
			return { kind: "error", text: `unknown command ${command}; /help lists commands` };
	}
}
