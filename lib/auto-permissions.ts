import { createHash } from "node:crypto";
import { relative, resolve, sep } from "node:path";
import { canonicalPath, type PermissionDecision, type ToolCall } from "./permissions";

export type AutoPermissionRoute = "allow" | "deny" | "ask-human" | "review";

const LOCAL_READS = new Set(["read", "glob", "grep", "skill", "subagent_list", "cache_guard_status", "reasoning_router_status"]);
const LOCAL_CONTROL = new Set(["coordination", "question", "compress"]);

function inside(path: string, root: string): boolean {
	const delta = relative(root, path);
	return delta === "" || (delta !== ".." && !delta.startsWith(`..${sep}`) && !delta.startsWith(sep));
}

/** Never let repository-local edits silently rewrite the gate that authorizes them. */
export function protectedApprovalPath(path: string): boolean {
	return /(?:^|\/)(?:\.git\/(?:hooks|config)|\.ssh|\.aws|\.gnupg)(?:\/|$)/.test(path) ||
		/(?:^|\/)(?:auth\.json|\.env(?:\.[^/]+)?)$/.test(path) ||
		/(?:^|\/)(?:\.claude|\.codex|\.omp|\.agents)\//.test(path) ||
		/(?:^|\/)(?:extensions\/permissions\.ts|lib\/(?:auto-[^/]+|permissions|permission-review)\.ts|config\/(?:permissions|permission-mode|auto-review)\.json)$/.test(path);
}

/** Source policy is evaluated first; automatic review cannot grant a denied capability. */
export function routeAutoPermission(call: ToolCall, decision: PermissionDecision, cwd: string): AutoPermissionRoute {
	if (decision.effect === "deny") return "deny";
	if (decision.humanApprovalRequired) return "ask-human";
	if (LOCAL_CONTROL.has(decision.action)) return "allow";
	if (decision.coordinationTarget) return decision.action === "subagent_list" ? "allow" : "review";
	if (decision.approvalReason) return "review";
	if (LOCAL_READS.has(decision.action)) {
		if (decision.action === "read" && (decision.resources ?? [decision.resource]).some(resource => {
			const path = canonicalPath(resource, cwd) ?? resource;
			return /(?:^|\/)(?:\.ssh|\.aws|\.gnupg)(?:\/|$)/.test(path) || /(?:^|\/)(?:auth\.json|\.env(?:\.(?!example$)[^/]+)?)$/.test(path);
		})) return "ask-human";
		return "allow";
	}
	if (decision.action === "edit") {
		const root = canonicalPath(cwd, cwd) ?? resolve(cwd);
		const paths = (decision.resources ?? [decision.resource]).map(path => canonicalPath(path, cwd));
		if (paths.some(path => !path)) return "deny";
		if (paths.some(path => protectedApprovalPath(path!))) return "ask-human";
		if (paths.some(path => /(?:^|\/)(?:AGENTS|CLAUDE)\.md$/.test(path!))) return "review";
		if (call.toolName === "apply_patch" && typeof call.input.input === "string" && /^\*\*\* (?:Delete File|Move to):/m.test(call.input.input)) return "review";
		if (Array.isArray(call.input.edits) && call.input.edits.some(edit => edit && typeof edit === "object" && ("rename" in edit || ("op" in edit && edit.op === "delete")))) return "review";
		return paths.every(path => inside(path!, root)) ? "allow" : "review";
	}
	// Shell names and MCP read annotations are not proof of effects. Executable
	// wrappers, remote destinations and delegation all receive contextual review.
	return "review";
}

function stable(value: unknown): unknown {
	if (Array.isArray(value)) return value.map(stable);
	if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([key, item]) => [key, stable(item)]));
	return value;
}

export function approvalHash(value: unknown): string {
	return createHash("sha256").update(JSON.stringify(stable(value))).digest("hex");
}

/** Counters belong to one agent run, not individual model/tool turns. */
export class ReviewCircuit {
	private consecutive = 0;
	private window: boolean[] = [];
	halted = false;

	record(result: "allow" | "deny" | "unavailable"): boolean {
		if (result !== "unavailable") {
			this.consecutive = result === "deny" ? this.consecutive + 1 : 0;
			this.window.push(result === "deny");
			if (this.window.length > 50) this.window.shift();
		}
		this.halted ||= this.consecutive >= 3 || this.window.filter(Boolean).length >= 10;
		return this.halted;
	}
}
