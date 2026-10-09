import { describe, expect, it } from "bun:test";
import { resolve } from "node:path";
import { loadExtensions } from "@oh-my-pi/pi-coding-agent";
import {
	canSurfaceTool,
	decidePermission,
	evaluateRules,
	intentForToolCall,
	roleForAgent,
	surfaceTools,
	type PermissionPolicies,
} from "../lib/permissions.ts";

const policies: PermissionPolicies = {
	build: [
		{ action: "*", resource: "*", effect: "ask" },
		{ action: "docs_*", resource: "*", effect: "deny" },
		{ action: "read", resource: "*", effect: "allow" },
		{ action: "shell", resource: "*", effect: "allow" },
		{ action: "shell", resource: "git push*", effect: "ask" },
		{ action: "subagent", resource: "*", effect: "deny" },
		{ action: "subagent", resource: "explore", effect: "allow" },
		{ action: "workplan_*", resource: "*", effect: "deny" },
	],
	plan: [
		{ action: "*", resource: "*", effect: "deny" },
		{ action: "read", resource: "*", effect: "allow" },
		{ action: "edit", resource: "*/.opencode/workplan/*", effect: "allow" },
		{ action: "edit", resource: ".opencode/workplan/*", effect: "allow" },
		{ action: "workplan_read", resource: "*", effect: "allow" },
		{ action: "workplan_create", resource: "*", effect: "allow" },
		{ action: "workplan_update", resource: "*", effect: "allow" },
		{ action: "workplan_patch", resource: "*", effect: "allow" },
		{ action: "workplan_reset", resource: "*", effect: "ask" },
	],
	orchestrator: [
		{ action: "*", resource: "*", effect: "ask" },
		{ action: "workplan_create", resource: "*", effect: "allow" },
		{ action: "workplan_update", resource: "*", effect: "allow" },
		{ action: "workplan_patch", resource: "*", effect: "allow" },
		{ action: "workplan_checkpoint", resource: "*", effect: "allow" },
		{ action: "workplan_compact", resource: "*", effect: "ask" },
	],
	tester: [
		{ action: "*", resource: "*", effect: "deny" },
		{ action: "shell", resource: "bun test*", effect: "allow" },
	],
};

describe("OpenCode compatibility policy", () => {
	it("uses TSUNA_AGENT for primary roles", () => {
		const original = process.env.TSUNA_AGENT;
		try {
			process.env.TSUNA_AGENT = "scholar";
			expect(roleForAgent({ kind: "main", name: "main" })).toBe("scholar");
		} finally {
			if (original === undefined) delete process.env.TSUNA_AGENT; else process.env.TSUNA_AGENT = original;
		}
	});

	it("loads through OMP's extension loader", async () => {
		const loaded = await loadExtensions([resolve(import.meta.dir, "../extensions/permissions.ts")], process.cwd());
		expect(loaded.errors).toEqual([]);
		expect(loaded.extensions).toHaveLength(1);
	});

	it("keeps ordered rules last-match-wins", () => {
		const rule = evaluateRules(policies.build, "shell", "git push origin main");
		expect(rule?.effect).toBe("ask");
		expect(decidePermission(policies, "build", { toolName: "bash", input: { command: "git status --short" } }).effect).toBe("allow");
	});

	it("checks every batched task agent and each literal shell segment", () => {
		expect(
			decidePermission(policies, "build", {
				toolName: "task",
				input: { tasks: [{ task: "research", agent: "explore" }, { task: "unsafe", agent: "task" }] },
			}).effect,
		).toBe("deny");
		expect(decidePermission(policies, "build", { toolName: "bash", input: { command: "echo ok && git push origin main" } }).effect).toBe("ask");
		expect(decidePermission(policies, "tester", { toolName: "bash", input: { command: "bun test && bun install" } }).effect).toBe("deny");
		expect(decidePermission(policies, "tester", { toolName: "bash", input: { command: "bun test $(whoami)" } }).effect).toBe("ask");
	});

	it("maps OMP built-ins to source actions and resources", () => {
		expect(intentForToolCall({ toolName: "write", input: { path: "/repo/.opencode/workplan/plan.md" } })).toMatchObject({
			action: "edit",
			resource: "/repo/.opencode/workplan/plan.md",
		});
		expect(decidePermission(policies, "plan", { toolName: "write", input: { path: "/repo/other.md" } }).effect).toBe("deny");
		expect(decidePermission(policies, "plan", { toolName: "write", input: { path: "/repo/.opencode/workplan/plan.md" } }).effect).toBe("allow");
		expect(decidePermission(policies, "tester", { toolName: "bash", input: { command: "bun test tests" } }).effect).toBe("allow");
		expect(decidePermission(policies, "tester", { toolName: "bash", input: { command: "bun install" } }).effect).toBe("deny");
		expect(
			decidePermission(policies, "plan", {
				toolName: "apply_patch",
				input: { input: "*** Begin Patch\n*** Add File: .opencode/workplan/new.md\n+x\n*** Update File: .opencode/workplan/plan.md\n*** Move to: .opencode/workplan/renamed.md\n*** End Patch" },
			}).effect,
		).toBe("allow");
		expect(decidePermission(policies, "plan", { toolName: "apply_patch", input: { input: "not a patch" } }).effect).toBe("deny");
	});

	it("maps known MCP names and rejects an unrecognized MCP origin", () => {
		expect(intentForToolCall({ toolName: "mcp__workplan_create", input: {} })).toEqual({ action: "workplan_create", resource: "*" });
		expect(decidePermission(policies, "plan", { toolName: "mcp__workplan_create", input: {} }).effect).toBe("allow");
		expect(decidePermission(policies, "plan", { toolName: "mcp__untrusted_delete", input: {} })).toMatchObject({
			effect: "deny",
			reason: "Denied unrecognized MCP origin",
		});
	});

	it("preserves extension tool permissions and home-directory resource patterns", () => {
		const documentPolicies: PermissionPolicies = {
			document: [{ action: "docs_*", resource: "*", effect: "allow" }],
			reader: [{ action: "read", resource: "~/.config/opencode/skills/*", effect: "allow" }],
		};
		expect(decidePermission(documentPolicies, "document", { toolName: "docs_compile", input: {} }).effect).toBe("allow");
		expect(canSurfaceTool(documentPolicies, "document", "docs_compile")).toBe(true);
		expect(
			decidePermission(documentPolicies, "reader", {
				toolName: "read",
				input: { path: `${process.env.HOME}/.config/opencode/skills/example/SKILL.md` },
			}).effect,
		).toBe("allow");
	});

	it("maps trusted non-filesystem reads and imported extension aliases", () => {
		const routes: PermissionPolicies = {
			plan: [
				{ action: "*", resource: "*", effect: "deny" },
				{ action: "skill", resource: "*", effect: "allow" },
				{ action: "webfetch", resource: "*", effect: "allow" },
				{ action: "opencode_read_mcp_resource", resource: "*", effect: "allow" },
				{ action: "read", resource: "*", effect: "allow" },
				{ action: "external_directory", resource: "*", effect: "allow" },
				{ action: "subagent_list", resource: "*", effect: "allow" },
				{ action: "compress", resource: "*", effect: "allow" },
			],
		};
		expect(decidePermission(routes, "plan", { toolName: "read", input: { path: "https://example.test/docs" } }).effect).toBe("allow");
		expect(intentForToolCall({ toolName: "read", input: { path: "https://example.test/docs" } }).resource).toBe("https://example.test/docs");
		expect(decidePermission(routes, "plan", { toolName: "read", input: { path: "skill://implementation/SKILL.md" } }).effect).toBe("allow");
		expect(intentForToolCall({ toolName: "read", input: { path: "skill://implementation/SKILL.md" } }).resource).toBe("implementation");
		expect(decidePermission(routes, "plan", { toolName: "read", input: { path: "workplan://active" } }).effect).toBe("allow");
		expect(decidePermission(routes, "plan", { toolName: "read", input: { path: "file:///tmp" } }, { cwd: process.cwd() }).effect).toBe("allow");
		expect(decidePermission(routes, "plan", { toolName: "read", input: { path: "mcp://unknown://resource" } }).effect).toBe("deny");
		expect(decidePermission(routes, "plan", { toolName: "subagent_list", input: {} }).effect).toBe("allow");
		expect(decidePermission(routes, "plan", { toolName: "compress", input: {} }).effect).toBe("allow");
		expect(decidePermission(routes, "plan", { toolName: "manage_skill", input: {} }).effect).toBe("deny");
	});

	it("enforces Shiori role ownership after source policy permits a route", () => {
		expect(decidePermission(policies, "build", { toolName: "mcp__workplan_create", input: {} }).effect).toBe("deny");
		expect(decidePermission(policies, "plan", { toolName: "mcp__workplan_update", input: { recovery: true } })).toMatchObject({
			effect: "deny",
			reason: "Only orchestrator may recover a workplan transaction",
		});
		expect(decidePermission(policies, "orchestrator", { toolName: "mcp__workplan_checkpoint", input: {} }).effect).toBe("allow");
	});

	it("does not offer denied tools and fails closed for device and unmapped mutation tools", () => {
		expect(canSurfaceTool(policies, "tester", "bash")).toBe(true);
		expect(canSurfaceTool(policies, "tester", "write")).toBe(false);
		expect(canSurfaceTool(policies, "build", "checkpoint")).toBe(false);
		expect(surfaceTools(policies, "tester", ["bash", "write", "yield"])).toEqual(["bash", "yield"]);
		expect(canSurfaceTool(policies, "build", "mcp__workplan_create")).toBe(false);
		expect(decidePermission(policies, "build", { toolName: "write", input: { path: "xd://eval/agents" } })).toMatchObject({
			effect: "deny",
			reason: "Denied xd:// device dispatch",
		});
		expect(decidePermission(policies, "build", { toolName: "checkpoint", input: {} }).effect).toBe("deny");
		expect(decidePermission(policies, "tester", { toolName: "wait", input: {} }).effect).toBe("allow");
		expect(canSurfaceTool(policies, "tester", "yield")).toBe(true);
	});

	it("allows session todo tracking in both approval modes without exposing unknown tools", () => {
		for (const role of ["orchestrator", "tester"]) {
			for (const approvalMode of ["auto", "source"] as const) {
				for (const input of [{ op: "view" }, { op: "init", items: ["Check permissions"] }]) {
					expect(decidePermission(policies, role, { toolName: "todo", input }, { approvalMode }).effect).toBe("allow");
				}
				expect(decidePermission(policies, role, { toolName: "unknown_tool", input: {} }, { approvalMode })).toMatchObject({
					effect: "deny",
					reason: "Denied unmapped OMP tool",
				});
			}
			expect(surfaceTools(policies, role, ["todo", "unknown_tool"])).toEqual(["todo"]);
		}
	});

	it("selects the configured primary role and trusted subagent name", () => {
		expect(roleForAgent({ kind: "main", name: "main" }, "build")).toBe("build");
		expect(roleForAgent({ kind: "sub", name: "plan" }, "build")).toBe("plan");
	});
});
