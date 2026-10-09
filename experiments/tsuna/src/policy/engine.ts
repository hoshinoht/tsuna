/**
 * Tsuna policy evaluation for the harness tool surface.
 *
 * Evaluation semantics (ordered last-match-wins rules, explicit denies,
 * external-directory checks, Shiori workplan ownership, shell chain checks,
 * auto-mode generic asks) are adapted from Tsuna `lib/permissions.ts` at
 * 92c4728. Host mapping is rewritten for this harness's own tool names; the
 * engine never prompts. Presentation and approval live in `gate.ts`.
 */
import { canonicalPath, evaluateAction, evaluateRules, isExternal, type PermissionEffect, type PermissionRule } from "./rules.ts";
import { extractLiteralAndChainSegments } from "./shell-tokenize.ts";

export type { PermissionEffect, PermissionRule };

export interface ToolCall {
	toolName: string;
	input: Record<string, unknown>;
}

export interface PermissionIntent {
	action: string;
	resource: string;
	resources?: readonly string[];
	/** Path-like resources are canonicalised and checked against the workspace. */
	pathLike?: boolean;
	unsafeChannel?: string;
}

export interface PermissionDecision extends PermissionIntent {
	effect: PermissionEffect;
	role: string;
	rule?: PermissionRule;
	reason?: string;
	/** True when the ask came only from a generic (`*`) rule. */
	genericAsk?: boolean;
}

export interface PolicyContext {
	/** Trusted role bound to the runtime session, never taken from tool input. */
	role: string;
	rules: readonly PermissionRule[];
	cwd: string;
	approvalMode: "source" | "auto";
	/** Configured, enabled MCP servers (name -> true). */
	mcpServers: ReadonlySet<string>;
	/** Harness-wide rules evaluated after role rules; only `deny` is honoured. */
	harnessDenies?: readonly PermissionRule[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function str(input: Record<string, unknown>, ...keys: string[]): string | undefined {
	for (const key of keys) {
		const value = input[key];
		if (typeof value === "string" && value.length > 0) return value;
	}
	return undefined;
}

/**
 * MCP tools are exposed as `mcp__<server>__<tool>` (double separator, so
 * server names may contain single underscores or hyphens). Policy actions use
 * Tsuna's source convention `<server>_<tool>` (e.g. `workplan_create`).
 */
export function mcpToolName(server: string, tool: string): string {
	return `mcp__${server}__${tool}`;
}

export function parseMcpToolName(name: string): { server: string; tool: string } | undefined {
	if (!name.startsWith("mcp__")) return undefined;
	const rest = name.slice(5);
	const split = rest.indexOf("__");
	if (split <= 0 || split + 2 >= rest.length) return undefined;
	return { server: rest.slice(0, split), tool: rest.slice(split + 2) };
}

/** Map a harness tool call to the policy intent it represents. */
export function intentForToolCall(call: ToolCall, mcpServers: ReadonlySet<string>): PermissionIntent {
	const { toolName, input } = call;
	if (toolName.startsWith("mcp__")) {
		const parsed = parseMcpToolName(toolName);
		if (!parsed || !mcpServers.has(parsed.server)) {
			return { action: toolName, resource: "*", unsafeChannel: "unrecognized MCP origin" };
		}
		return { action: `${parsed.server}_${parsed.tool}`, resource: "*" };
	}
	switch (toolName) {
		case "read":
			return { action: "read", resource: str(input, "path") ?? "<missing-path>", pathLike: true };
		case "write":
		case "edit":
			return { action: "edit", resource: str(input, "path") ?? "<missing-path>", pathLike: true };
		case "grep":
			return { action: "grep", resource: str(input, "path") ?? ".", pathLike: true };
		case "glob":
			return { action: "glob", resource: str(input, "path") ?? ".", pathLike: true };
		case "bash":
			return { action: "shell", resource: str(input, "command") ?? "<missing-command>" };
		case "job_status":
		case "job_wait":
			return { action: "coordination", resource: "*" };
		case "job_cancel":
			return { action: "job_cancel", resource: "*" };
		case "mcp_resource": {
			const server = str(input, "server") ?? "<missing-server>";
			if (!mcpServers.has(server)) {
				return { action: "mcp_resource", resource: server, unsafeChannel: "unrecognized MCP resource route" };
			}
			return { action: "mcp_resource", resource: server };
		}
		case "task": {
			const agent = str(input, "agent") ?? "<missing-agent>";
			return { action: "subagent", resource: agent, resources: [agent] };
		}
		case "dispatch": {
			const batch = Array.isArray(input.tasks) ? input.tasks : [];
			const agents = batch.map(item => (isRecord(item) ? str(item, "agent") : undefined) ?? "<missing-agent>");
			if (agents.length === 0) return { action: "subagent", resource: "<empty-dispatch>", unsafeChannel: "empty dispatch" };
			return { action: "subagent", resource: agents[0]!, resources: agents };
		}
		case "agent_list":
		case "agent_read":
			return { action: "subagent_list", resource: str(input, "id") ?? "*" };
		case "agent_send":
			return { action: "subagent_message", resource: str(input, "id") ?? "<missing-id>" };
		case "agent_interrupt":
		case "agent_cancel":
			return { action: "subagent_stop", resource: str(input, "id") ?? "<missing-id>" };
		case "agent_resume":
			return { action: "subagent_resume", resource: str(input, "id") ?? "<missing-id>" };
		case "wait":
		case "yield":
		case "batch":
			// Scope is enforced by the runtime (owned work only); nested batch
			// calls are each evaluated separately.
			return { action: "coordination", resource: "*" };
		case "compress":
			return { action: "compress", resource: "*" };
		default:
			return { action: toolName, resource: "*", unsafeChannel: "unmapped tool" };
	}
}

function enforceWorkplanOwnership(action: string, input: Record<string, unknown>, role: string, resource: string): string | undefined {
	if (["workplan_create", "workplan_update", "workplan_patch", "workplan_reset"].includes(action) && role !== "plan" && role !== "orchestrator") {
		return "Only plan and orchestrator may author workplan lifecycle changes";
	}
	if (action === "workplan_checkpoint" && role !== "orchestrator") return "Only orchestrator may update workplan checkpoints";
	if (action === "workplan_update" && input.recovery !== undefined && role !== "orchestrator") {
		return "Only orchestrator may recover a workplan transaction";
	}
	if (action === "workplan_compact" && input.mode === "apply" && role !== "orchestrator") {
		return "Only orchestrator may apply workplan compaction";
	}
	if (action === "mcp_resource" && role === "plan" && resource !== "workplan") {
		return "The plan agent may only read MCP resources from the workplan server";
	}
}

/**
 * Exec-capable flags turn a "search" or "edit" command into arbitrary
 * execution or mutation (`rg --pre`, `fd -x`, `find -exec`, `sed -i`). A
 * whole-string rule such as `shell rg *` would otherwise admit them, so they
 * always require approval (and are therefore denied in headless children).
 */
const EXEC_CAPABLE: readonly [RegExp, RegExp, string][] = [
	[/^(?:\S*\/)?rg$/, /^--pre(?:=|$)|^--pre-glob(?:=|$)|^--search-zip$|^-z$/, "rg preprocessor/decompression flags run external programs"],
	[/^(?:\S*\/)?(?:fd|fdfind)$/, /^-x$|^-X$|^--exec(?:-batch)?(?:=|$)/, "fd --exec runs commands"],
	[/^(?:\S*\/)?find$/, /^-(?:exec|execdir|ok|okdir|delete|fprint|fprintf|fls)$/, "find actions execute or mutate"],
	[/^(?:\S*\/)?(?:sed|gsed)$/, /^-i|^--in-place|^-[A-Za-z]*i/, "sed in-place editing mutates files"],
	[/^(?:\S*\/)?xargs$/, /.*/, "xargs executes commands"],
];

export function execCapableReason(command: string): string | undefined {
	for (const words of command.split(/\|\||&&|[;|\n]/).map(part => part.trim().split(/\s+/).filter(Boolean))) {
		const [program, ...args] = words;
		if (!program) continue;
		for (const [name, flag, reason] of EXEC_CAPABLE) {
			if (!name.test(program)) continue;
			if (args.some(arg => flag.test(arg.replace(/^['"]|['"]$/g, "")))) return reason;
			if (program.endsWith("sed") && args.some(arg => /(^|;)\s*[0-9,$/]*[ew]\b/.test(arg.replace(/^['"]|['"]$/g, "")))) {
				return "sed e/w commands execute or write files";
			}
		}
	}
	return undefined;
}

/** Resolve a call without prompting. Unknown routes fail closed. */
export function decide(ctx: PolicyContext, call: ToolCall): PermissionDecision {
	const intent = intentForToolCall(call, ctx.mcpServers);
	const role = ctx.role;
	if (intent.unsafeChannel) return { ...intent, effect: "deny", role, reason: `Denied ${intent.unsafeChannel}` };
	const harness = ctx.harnessDenies ?? [];
	const harnessDeny = (action: string, resource: string) => {
		const rule = evaluateRules(harness, action, resource);
		return rule?.effect === "deny" ? rule : undefined;
	};
	if (intent.action === "coordination") return { ...intent, effect: "allow", role };
	const rules = ctx.rules;
	const resources = intent.resources ?? [intent.resource];
	let finalRule: PermissionRule | undefined;
	let asks = false;
	let specificAsk = false;
	const requiresAsk = (rule: PermissionRule) => {
		if (rule.effect !== "ask") return false;
		const generic = rule.action === "*" || rule.resource === "*";
		if (ctx.approvalMode === "auto" && generic) return false;
		if (!generic) specificAsk = true;
		return true;
	};
	for (const rawResource of resources) {
		const resource = intent.pathLike ? canonicalPath(rawResource, ctx.cwd) : rawResource;
		if (!resource) return { ...intent, effect: "deny", role, reason: "Path could not be safely resolved" };
		const denied = harnessDeny(intent.action, resource);
		if (denied) return { ...intent, effect: "deny", role, rule: denied, reason: "Denied by harness policy" };
		const rule = evaluateRules(rules, intent.action, resource);
		if (!rule || rule.effect === "deny") {
			return { ...intent, effect: "deny", role, rule, reason: rule ? "Denied by permission rule" : "No matching permission rule" };
		}
		finalRule = rule;
		if (requiresAsk(rule)) asks = true;
		if (intent.pathLike && isExternal(resource, ctx.cwd)) {
			const externalRule = evaluateRules(rules, "external_directory", resource);
			if (!externalRule || externalRule.effect === "deny") {
				return { ...intent, effect: "deny", role, rule: externalRule, reason: "External directory access is not permitted" };
			}
			if (requiresAsk(externalRule)) asks = true;
		}
	}
	const ownership = enforceWorkplanOwnership(intent.action, call.input, role, intent.resource);
	if (ownership) return { ...intent, effect: "deny", role, rule: finalRule, reason: ownership };
	if (intent.action === "shell") {
		const execReason = execCapableReason(intent.resource);
		if (execReason) return { ...intent, effect: "ask", role, rule: finalRule, reason: execReason, genericAsk: false };
	}
	if (intent.action === "shell" && /[;&|()$`<>\n]/.test(intent.resource)) {
		const segments = extractLiteralAndChainSegments(intent.resource);
		if (!segments) {
			const recursiveDelete = /\brm\s+(?:(?:-[A-Za-z]*r[A-Za-z]*|--recursive)\b)/.test(intent.resource);
			const hardReset = /\bgit\s+reset\b[^\n;|&]*--hard\b/.test(intent.resource);
			const publishOrClean = /\bgit\s+(?:-[^\n;|&]+\s+)?(?:push|clean)\b/.test(intent.resource);
			const guarded = recursiveDelete || hardReset || publishOrClean;
			const reason = recursiveDelete
				? "Recursive deletion (rm -r / --recursive) requires approval"
				: hardReset
					? "Git hard reset requires approval"
					: publishOrClean
						? "Git publishing or cleanup requires approval"
						: "Complex shell command requires confirmation";
			const effect = ctx.approvalMode === "auto" && !guarded && !asks ? "allow" : "ask";
			return { ...intent, effect, role, rule: finalRule, reason, genericAsk: effect === "ask" && !guarded && !specificAsk };
		}
		for (const segment of segments) {
			const denied = harnessDeny("shell", segment.text);
			if (denied) return { ...intent, effect: "deny", role, rule: denied, reason: "Denied by harness policy" };
			const segmentRule = evaluateRules(rules, "shell", segment.text);
			if (!segmentRule || segmentRule.effect === "deny") {
				return { ...intent, effect: "deny", role, rule: segmentRule, reason: "Shell segment is not permitted" };
			}
			if (requiresAsk(segmentRule)) asks = true;
		}
	}
	return { ...intent, effect: asks ? "ask" : "allow", role, rule: finalRule, genericAsk: asks && !specificAsk };
}

/** Catalog filtering only. Execution-time `decide` remains authoritative. */
export function canSurfaceTool(ctx: PolicyContext, toolName: string): boolean {
	const intent = intentForToolCall({ toolName, input: {} }, ctx.mcpServers);
	// With no concrete input, only an unknown tool or unknown MCP origin is
	// unsafe by name; input-dependent problems are judged at execution time.
	if (intent.unsafeChannel === "unmapped tool" || intent.unsafeChannel === "unrecognized MCP origin") return false;
	if (intent.action === "coordination") return true;
	const rule = evaluateAction(ctx.rules, intent.action);
	return rule !== undefined && rule.effect !== "deny";
}
