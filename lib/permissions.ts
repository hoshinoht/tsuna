/**
 * OpenCode v2 permission compatibility for the OMP trial.
 *
 * The source rules are ordered and last-match-wins. Keep evaluation here
 * rather than translating them into OMP approval globs so MCP routes and
 * subagent identities are checked at the point of execution.
 */
import { readFileSync, realpathSync, statSync } from "node:fs";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { extractLiteralAndChainSegments } from "@oh-my-pi/pi-coding-agent/tools/shell-tokenize";
import { coordinationTarget, coordinationReason, type CoordinationScope, type CoordinationTarget } from "./coordination.ts";

export type PermissionEffect = "allow" | "ask" | "deny";

export interface PermissionRule {
	action: string;
	resource: string;
	effect: PermissionEffect;
}

export type PermissionPolicies = Record<string, readonly PermissionRule[]>;

export interface ToolCall {
	toolName: string;
	input: Record<string, unknown>;
}

export interface PermissionIntent {
	action: string;
	resource: string;
	resources?: readonly string[];
	unsafeChannel?: string;
	approvalReason?: string;
	coordinationTarget?: CoordinationTarget;
}

export interface PermissionDecision extends PermissionIntent {
	effect: PermissionEffect;
	role: string;
	rule?: PermissionRule;
	reason?: string;
	/** Specific asks from any resource/segment survive the contextual reviewer. */
	humanApprovalRequired?: boolean;
}

const KNOWN_MCP_SERVERS: readonly [string, string][] = [
	["researcher_mcp", "researcher-mcp"],
	["ats_tailor", "ats-tailor"],
	["lsp_tools", "lsp-tools"],
	["grep_app", "grep_app"],
	["deepwiki", "deepwiki"],
	["context7", "context7"],
	["gofetch", "gofetch"],
	["workplan", "workplan"],
];

const UNSAFE_TOOLS = new Set([
	"checkpoint",
	"rewind",
	"memory_edit",
	"retain",
	"learn",
	"github",
	"browser",
	"computer",
]);

const LSP_READ_ACTIONS = new Set([
	"diagnostics", "definition", "type_definition", "implementation", "references", "hover", "symbols", "status", "capabilities",
]);
const DEBUG_READ_ACTIONS = new Set([
	"output", "threads", "stack_trace", "scopes", "variables", "disassemble", "read_memory", "loaded_sources", "modules", "sessions",
]);

function globMatches(pattern: string, value: string): boolean {
	if (pattern.startsWith("~/") && process.env.HOME) pattern = `${process.env.HOME}${pattern.slice(1)}`;
	const sourceSkills = `${process.env.HOME ?? "~"}/.config/opencode/skills/`;
	const importedSkills = `${process.env.TSUNA_ROOT ?? resolve(import.meta.dir, "..")}/agent/skills/`;
	if (pattern.startsWith(sourceSkills) && value.startsWith(importedSkills)) {
		pattern = `${importedSkills}${pattern.slice(sourceSkills.length)}`;
	}
	let source = "^";
	for (const character of pattern) {
		if (character === "*") source += ".*";
		else if (character === "?") source += ".";
		else source += character.replace(/[|\\{}()[\]^$+?.]/g, "\\$&");
	}
	return new RegExp(`${source}$`, "s").test(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringInput(input: Record<string, unknown>, ...keys: string[]): string | undefined {
	for (const key of keys) {
		const value = input[key];
		if (typeof value === "string" && value.length > 0) return value;
	}
	return undefined;
}

function stringInputs(input: Record<string, unknown>, ...keys: string[]): string[] {
	const values: string[] = [];
	for (const key of keys) {
		const value = input[key];
		if (typeof value === "string" && value.length > 0) values.push(value);
		if (Array.isArray(value)) values.push(...value.filter((item): item is string => typeof item === "string" && item.length > 0));
	}
	return [...new Set(values)];
}

function patchPaths(input: string): string[] {
	const paths: string[] = [];
	for (const line of input.split("\n")) {
		const match = /^\*\*\* (?:Add|Update|Delete) File: (.+)$|^\*\*\* Move to: (.+)$/.exec(line);
		const path = match?.[1] ?? match?.[2];
		if (path?.trim()) paths.push(path.trim());
	}
	return [...new Set(paths)];
}

function editPaths(input: Record<string, unknown>): string[] {
	const paths = stringInputs(input, "path", "file", "paths", "files");
	if (Array.isArray(input.edits)) {
		for (const edit of input.edits) {
			if (isRecord(edit)) paths.push(...stringInputs(edit, "rename", "path"));
		}
	}
	return [...new Set(paths)];
}

function mcpIntent(toolName: string): PermissionIntent | undefined {
	if (!toolName.startsWith("mcp__")) return undefined;
	const suffix = toolName.slice("mcp__".length);
	for (const [ompServer, sourceServer] of KNOWN_MCP_SERVERS) {
		const prefix = `${ompServer}_`;
		if (!suffix.startsWith(prefix)) continue;
		const tool = suffix.slice(prefix.length);
		if (tool.length === 0) return undefined;
		return { action: `${sourceServer}_${tool}`, resource: "*" };
	}
	return undefined;
}

/** Converts an OMP tool execution into the closest source permission intent. */
export function intentForToolCall(call: ToolCall): PermissionIntent {
	const { toolName, input } = call;
	const mcp = mcpIntent(toolName);
	if (mcp) return mcp;
	if (toolName.startsWith("mcp__")) {
		return { action: toolName, resource: "*", unsafeChannel: "unrecognized MCP origin" };
	}
	if (toolName.startsWith("docs_")) return { action: toolName, resource: "*" };

	switch (toolName) {
	case "read": {
			const path = stringInput(input, "path", "file") ?? "<missing-path>";
			const target = coordinationTarget(path);
			if (target) return { action: "subagent_list", resource: path, coordinationTarget: target };
			if (/^https?:\/\//i.test(path)) return { action: "webfetch", resource: path };
			if (path.startsWith("skill://")) return { action: "skill", resource: path.slice("skill://".length).split("/")[0] || "*" };
			if (path.startsWith("workplan://") || path.startsWith("mcp://workplan://")) {
				return { action: "opencode_read_mcp_resource", resource: "workplan" };
			}
			if (path.startsWith("mcp://")) {
				return { action: "opencode_read_mcp_resource", resource: "*", unsafeChannel: "unrecognized MCP resource route" };
			}
			return { action: "read", resource: path };
		}
		case "glob":
			return { action: "glob", resource: stringInput(input, "pattern", "path") ?? "*" };
		case "grep":
		case "find":
		case "ast_grep":
			return { action: "grep", resource: stringInput(input, "path", "glob") ?? "*" };
		case "edit":
		case "write": {
			const paths = editPaths(input);
			const path = paths[0] ?? "<missing-path>";
			const target = coordinationTarget(path);
			if (target) {
				// Messaging uses the source's parent-control permission, not filesystem edit access.
				return { action: "subagent_stop", resource: path, coordinationTarget: target,
					unsafeChannel: toolName !== "write" || paths.length !== 1 || !/^agent:\//i.test(path) ? "unsupported coordination mutation" : undefined };
			}
			if (path.startsWith("xd://")) {
				return { action: "edit", resource: path, unsafeChannel: "xd:// device dispatch" };
			}
			return { action: "edit", resource: path, resources: paths.length > 0 ? paths : [path] };
		}
		case "apply_patch": {
			const paths = typeof input.input === "string" ? patchPaths(input.input) : [];
			if (paths.length === 0) return { action: "edit", resource: "<missing-patch-path>", unsafeChannel: "unparseable apply_patch input" };
			return { action: "edit", resource: paths[0]!, resources: paths };
		}
		case "ast_edit":
			return { action: "edit", resource: stringInput(input, "path", "file") ?? "<missing-path>" };
		case "bash":
			return { action: "shell", resource: stringInput(input, "command") ?? "<missing-command>" };
		case "lsp": {
			const operation = stringInput(input, "action") ?? "<missing-operation>";
			const file = stringInput(input, "file") ?? ".";
			const resource = file === "*" ? "." : file;
			const preview = (operation === "rename" || operation === "rename_file") && input.apply === false;
			if (LSP_READ_ACTIONS.has(operation) || preview || (operation === "code_actions" && input.apply !== true)) {
				return { action: "read", resource };
			}
			if (operation === "rename" || operation === "rename_file" || operation === "code_actions") {
				const resources = operation === "rename_file" ? [resource, stringInput(input, "new_name") ?? "<missing-path>"] : [resource];
				// The server can return edits or commands beyond the initial file; the bridge cannot preflight those targets.
				return { action: "edit", resource, resources, approvalReason: "LSP workspace mutations require confirmation of the full operation" };
			}
			if (operation === "request" || operation === "reload") {
				return { action: "lsp", resource: JSON.stringify(input), approvalReason: "Raw LSP requests and server reloads require confirmation" };
			}
			return { action: "lsp", resource: operation, unsafeChannel: "unrecognized LSP operation" };
		}
		case "debug": {
			const operation = stringInput(input, "action") ?? "<missing-operation>";
			if (DEBUG_READ_ACTIONS.has(operation)) {
				const paths = stringInputs(input, "file", "program", "cwd");
				return { action: "read", resource: paths[0] ?? ".", resources: paths.length > 0 ? paths : ["."] };
			}
			return { action: "debug", resource: JSON.stringify(input), approvalReason: "Debugger execution and process mutations require confirmation" };
		}
		case "eval":
			return { action: "eval", resource: JSON.stringify(input), approvalReason: "Eval executes arbitrary code and requires confirmation" };
		case "task": {
			const batch = Array.isArray(input.tasks) ? input.tasks : [];
			const agents = batch
				.map(item => isRecord(item) ? stringInput(item, "agent") : undefined)
				.map(agent => agent ?? "task");
			const single = stringInput(input, "agent") ?? "task";
			return { action: "subagent", resource: agents[0] ?? single, resources: agents.length > 0 ? agents : [single] };
		}
		case "wait":
		case "yield":
		case "todo":
			return { action: "coordination", resource: "*" };
		case "goal":
			// GoalTool only operates on the current session's already-enabled goal runtime.
			// Reading/completing that objective must not be blocked as an unknown execution tool.
			return input.op === "get" || input.op === "complete"
				? { action: "coordination", resource: "*" }
				: { action: "goal", resource: stringInput(input, "op") ?? "<missing-operation>" };
		case "ask":
			return { action: "question", resource: "*" };
		case "web_search":
			return { action: "websearch", resource: "*" };
		case "manage_skill":
			return { action: "manage_skill", resource: "*" };
		case "new_context":
			return { action: "compress", resource: "*" };
		case "subagent_list":
		case "subagent_stop":
		case "compress":
		case "cache_guard_status":
		case "reasoning_router_status":
			return { action: toolName, resource: "*" };
		default:
			return {
				action: toolName,
				resource: "*",
				unsafeChannel: UNSAFE_TOOLS.has(toolName) ? "unmapped mutation channel" : "unmapped OMP tool",
			};
	}
}

/** Applies the original OpenCode last-match-wins rule semantics. */
export function evaluateRules(rules: readonly PermissionRule[], action: string, resource: string): PermissionRule | undefined {
	let matched: PermissionRule | undefined;
	for (const rule of rules) {
		if (globMatches(rule.action, action) && globMatches(rule.resource, resource)) matched = rule;
	}
	return matched;
}

export function canonicalPath(path: string, cwd: string): string | undefined {
	if (path.startsWith("file://")) {
		try {
			path = fileURLToPath(path);
		} catch {
			return undefined;
		}
	}
	if (path.includes("://")) return undefined;
	const expanded = path.startsWith("~/") && process.env.HOME ? `${process.env.HOME}${path.slice(1)}` : path;
	const absolute = isAbsolute(expanded) ? resolve(expanded) : resolve(cwd, expanded);
	const tail: string[] = [];
	for (let current = absolute; ; current = dirname(current)) {
		try {
			return resolve(realpathSync.native(current), ...tail.reverse());
		} catch {
			if (dirname(current) === current) return undefined;
			tail.push(current.slice(dirname(current).length + 1));
		}
	}
}

function gitBoundary(path: string): { root: string; commonDir: string } | undefined {
	let directory = path;
	try { if (!statSync(directory).isDirectory()) directory = dirname(directory); } catch { directory = dirname(directory); }
	for (; ; directory = dirname(directory)) {
		const marker = join(directory, ".git");
		try {
			const info = statSync(marker);
			if (info.isDirectory()) return { root: directory, commonDir: realpathSync.native(marker) };
			if (info.isFile()) {
				const match = /^gitdir:\s*(.+)\s*$/m.exec(readFileSync(marker, "utf8"));
				if (!match) return undefined;
				const gitDir = realpathSync.native(resolve(directory, match[1].trim()));
				let commonDir = gitDir;
				try { commonDir = realpathSync.native(resolve(gitDir, readFileSync(join(gitDir, "commondir"), "utf8").trim())); } catch {}
				// Linked worktree metadata must point back to this checkout.
				if (commonDir !== gitDir) {
					const backPointer = readFileSync(join(gitDir, "gitdir"), "utf8").trim();
					if (realpathSync.native(backPointer) !== realpathSync.native(marker)) return undefined;
				}
				return { root: directory, commonDir };
			}
		} catch {}
		if (dirname(directory) === directory) return undefined;
	}
}

function isExternal(path: string, cwd: string): boolean {
	const canonicalCwd = canonicalPath(cwd, cwd);
	if (!canonicalCwd) return true;
	const currentRepo = gitBoundary(canonicalCwd);
	const root = currentRepo?.root ?? canonicalCwd;
	const delta = relative(root, path);
	if (delta !== ".." && !delta.startsWith(`..${process.platform === "win32" ? "\\" : "/"}`) && !isAbsolute(delta)) return false;
	const targetRepo = currentRepo ? gitBoundary(path) : undefined;
	return !targetRepo || targetRepo.commonDir !== currentRepo?.commonDir;
}

function enforceWorkplanOwnership(intent: PermissionIntent, input: Record<string, unknown>, role: string): string | undefined {
	const { action } = intent;
	if (
		["workplan_create", "workplan_update", "workplan_patch", "workplan_reset"].includes(action) &&
		role !== "plan" &&
		role !== "orchestrator"
	) {
		return "Only plan and orchestrator may author workplan lifecycle changes";
	}
	if (action === "workplan_checkpoint" && role !== "orchestrator") {
		return "Only orchestrator may update workplan checkpoints";
	}
	if (action === "workplan_update" && input.recovery !== undefined && role !== "orchestrator") {
		return "Only orchestrator may recover a workplan transaction";
	}
	if (action === "workplan_compact" && input.mode === "apply" && role !== "orchestrator") {
		return "Only orchestrator may apply workplan compaction";
	}
	if (action === "opencode_read_mcp_resource" && role === "plan" && intent.resource !== "workplan" && input.server !== "workplan") {
		return "The plan agent may only read MCP resources from the workplan server";
	}
}

/** Resolves a call without prompting. Unknown routes and device dispatch fail closed. */
export function decidePermission(
	policies: PermissionPolicies,
	role: string,
	call: ToolCall,
	options: { cwd?: string; approvalMode?: "source" | "auto"; coordinationScope?: CoordinationScope } = {},
): PermissionDecision {
	const intent = intentForToolCall(call);
	if (intent.action === "coordination") return { ...intent, effect: "allow", role };
	if (intent.unsafeChannel) {
		return { ...intent, effect: "deny", role, reason: `Denied ${intent.unsafeChannel}` };
	}
	if (intent.coordinationTarget) {
		const reason = coordinationReason(intent.coordinationTarget, options.coordinationScope, call.toolName === "write");
		if (reason) return { ...intent, effect: "deny", role, reason };
	}
	const rules = policies[role] ?? [];
	const resources = intent.resources ?? [intent.resource];
	let finalRule: PermissionRule | undefined;
	let asks = false;
	let humanApprovalRequired = false;
	const requiresAsk = (rule: PermissionRule) => rule.effect === "ask" &&
		(options.approvalMode !== "auto" || (rule.action !== "*" && rule.resource !== "*"));
	const recordAsk = (rule: PermissionRule) => {
		if (requiresAsk(rule)) asks = true;
		if (rule.effect === "ask" && rule.action !== "*" && rule.resource !== "*") humanApprovalRequired = true;
	};
	for (const rawResource of resources) {
		const resource = options.cwd && (intent.action === "read" || intent.action === "edit")
			? canonicalPath(rawResource, options.cwd)
			: rawResource;
		if (!resource) return { ...intent, effect: "deny", role, reason: "Path could not be safely resolved" };
		const rule = evaluateRules(rules, intent.action, resource);
		if (!rule || rule.effect === "deny") return { ...intent, effect: "deny", role, rule, reason: rule ? "Denied by permission rule" : "No matching permission rule" };
		finalRule = rule;
		recordAsk(rule);
		if (options.cwd && (intent.action === "read" || intent.action === "edit") && isExternal(resource, options.cwd)) {
			const externalRule = evaluateRules(rules, "external_directory", resource);
			if (!externalRule || externalRule.effect === "deny") {
				return { ...intent, effect: "deny", role, rule: externalRule, reason: "External directory access is not permitted" };
			}
			recordAsk(externalRule);
		}
	}
	const workplanReason = enforceWorkplanOwnership(intent, call.input, role);
	if (workplanReason) return { ...intent, effect: "deny", role, rule: finalRule, reason: workplanReason };
	if (intent.approvalReason) return { ...intent, effect: "ask", role, rule: finalRule, reason: intent.approvalReason, humanApprovalRequired };
	if (intent.action === "shell") {
		if (/[;&|()$`<>\n]/.test(intent.resource)) {
			const segments = extractLiteralAndChainSegments(intent.resource);
			if (!segments) {
				// Auto mode approves ordinary pipelines, heredocs and substitutions.
				// Keep the imported destructive/publishing guards even inside a chain.
				const recursiveDelete = /\brm\s+(?:(?:-[A-Za-z]*r[A-Za-z]*|--recursive)\b)/.test(intent.resource);
				const hardReset = /\bgit\s+reset\b[^\n;|&]*--hard\b/.test(intent.resource);
				const publishOrClean = /\bgit\s+(?:-[^\n;|&]+\s+)?(?:push|clean)\b/.test(intent.resource);
				const guarded = recursiveDelete || hardReset || publishOrClean;
				const reason = recursiveDelete ? "Recursive deletion (rm -r / --recursive) requires approval"
					: hardReset ? "Git hard reset requires approval"
					: publishOrClean ? "Git publishing or cleanup requires approval"
					: "Complex shell command requires confirmation";
				return { ...intent, effect: options.approvalMode === "auto" && !guarded && !asks ? "allow" : "ask", role, rule: finalRule, reason, humanApprovalRequired: humanApprovalRequired || guarded };
			}
			for (const segment of segments) {
				const segmentRule = evaluateRules(rules, "shell", segment.text);
				if (!segmentRule || segmentRule.effect === "deny") return { ...intent, effect: "deny", role, rule: segmentRule, reason: "Shell segment is not permitted" };
				recordAsk(segmentRule);
			}
		}
	}
	return { ...intent, effect: asks ? "ask" : "allow", role, rule: finalRule, humanApprovalRequired };
}

/** Whether a tool should be exposed in the catalog before its concrete resource is known. */
export function canSurfaceTool(policies: PermissionPolicies, role: string, toolName: string): boolean {
	const input = toolName === "lsp" ? { action: "status" } : toolName === "debug" ? { action: "sessions" } : {};
	const intent = intentForToolCall({ toolName, input });
	if (intent.action === "coordination") return true;
	if (intent.unsafeChannel) return false;
	const rules = policies[role] ?? [];
	// A catalog has no concrete path or command yet. Use the last action match
	// as the conservative summary: a later explicit action deny must hide the
	// tool, while a role such as tester can still receive bash for its permitted
	// command subset and have the concrete command checked at execution time.
	let matched: PermissionRule | undefined;
	for (const rule of rules) {
		if (globMatches(rule.action, intent.action)) matched = rule;
	}
	return matched?.effect !== undefined && matched.effect !== "deny";
}

/** Returns the policy-safe catalog snapshot for an explicit interactive refresh. */
export function surfaceTools(policies: PermissionPolicies, role: string, toolNames: Iterable<string>): string[] {
	return [...toolNames].filter(toolName => canSurfaceTool(policies, role, toolName));
}

export function roleForAgent(agent: { kind: "main" | "sub"; name: string }, primaryRole = process.env.TSUNA_AGENT ?? "orchestrator"): string {
	return agent.kind === "sub" ? agent.name : primaryRole;
}

export function asToolCall(toolName: string, input: unknown): ToolCall {
	return { toolName, input: isRecord(input) ? input : {} };
}
