/**
 * Stage 5: permission engine, gate and runtime enforcement.
 */
import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, realpathSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { loadDefinitions } from "../src/agents/definitions.ts";
import { canSurfaceTool, decide, type PermissionRule, type PolicyContext } from "../src/policy/engine.ts";
import { PermissionGate, type ApprovalRequest, type GateSession, type ReviewVerdict } from "../src/policy/gate.ts";
import { SAMPLE_PACK, setScript, startRig, tempDir, type TestRig } from "./helpers/harness.ts";

const rigs: TestRig[] = [];
afterEach(async () => {
	for (const rig of rigs.splice(0)) await rig.harness.shutdown();
});

async function rig(opts: Parameters<typeof startRig>[0] = {}) {
	const r = await startRig(opts);
	rigs.push(r);
	return r;
}

const SAMPLE = loadDefinitions([SAMPLE_PACK]);
const rulesOf = (role: string) => SAMPLE.require(role).permissions;

function R(...rules: [string, string, string][]): PermissionRule[] {
	return rules.map(([effect, action, resource]) => ({ effect: effect as PermissionRule["effect"], action, resource }));
}

function realTemp(prefix: string): string {
	return realpathSync(tempDir(prefix));
}

function ctx(rules: PermissionRule[], extra: Partial<PolicyContext> = {}): PolicyContext {
	return { role: "tester-role", rules, cwd: extra.cwd ?? realTemp("tsuna-pol-"), approvalMode: "source", mcpServers: new Set(), ...extra };
}

const shell = (command: string) => ({ toolName: "bash", input: { command } });
const read = (path: string) => ({ toolName: "read", input: { path } });

describe("decide", () => {
	test("last match wins; an explicit deny after an allow denies", () => {
		const c = ctx(R(["deny", "*", "*"], ["allow", "read", "*"]));
		expect(decide(c, read("a.txt")).effect).toBe("allow");
		const denied = ctx(R(["allow", "read", "*"], ["deny", "read", "*secret*"]));
		expect(decide(denied, read("a.txt")).effect).toBe("allow");
		const d = decide(denied, read("my-secret.txt"));
		expect(d.effect).toBe("deny");
		expect(d.reason).toBe("Denied by permission rule");
		// Later allow re-opens what an earlier deny closed.
		const reopened = ctx(R(["deny", "read", "*secret*"], ["allow", "read", "*"]));
		expect(decide(reopened, read("my-secret.txt")).effect).toBe("allow");
	});

	test("no matching rule denies", () => {
		const d = decide(ctx(R(["allow", "grep", "*"])), read("a.txt"));
		expect(d.effect).toBe("deny");
		expect(d.reason).toBe("No matching permission rule");
	});

	test("harness denies override role allows", () => {
		const c = ctx(R(["allow", "*", "*"]), { harnessDenies: R(["deny", "read", "*secret*"], ["deny", "shell", "curl *"]) });
		const d = decide(c, read("secret.txt"));
		expect(d.effect).toBe("deny");
		expect(d.reason).toBe("Denied by harness policy");
		expect(decide(c, read("ok.txt")).effect).toBe("allow");
		expect(decide(c, shell("curl http://x")).reason).toBe("Denied by harness policy");
		// Also checked per && segment.
		expect(decide(c, shell("echo a && curl http://x")).reason).toBe("Denied by harness policy");
		// A harness "allow" is never honoured as a widening.
		const widen = ctx(R(["deny", "*", "*"]), { harnessDenies: R(["allow", "read", "*"]) });
		expect(decide(widen, read("ok.txt")).effect).toBe("deny");
	});

	test("unknown tools and unconfigured MCP servers are denied", () => {
		const c = ctx(R(["allow", "*", "*"]), { mcpServers: new Set(["lsp-tools"]) });
		const unknown = decide(c, { toolName: "frobnicate", input: {} });
		expect(unknown.effect).toBe("deny");
		expect(unknown.reason).toContain("unmapped tool");
		const mcp = decide(c, { toolName: "mcp__unknownserver__x", input: {} });
		expect(mcp.effect).toBe("deny");
		expect(mcp.reason).toContain("unrecognized MCP origin");
		expect(decide(c, { toolName: "mcp__malformed", input: {} }).effect).toBe("deny");
		expect(decide(c, { toolName: "mcp_resource", input: { server: "unknownserver" } }).effect).toBe("deny");
	});

	test("configured MCP tools map to <server>_<tool> and follow rules", () => {
		const c = ctx(R(["deny", "*", "*"], ["allow", "lsp-tools_*", "*"], ["deny", "lsp-tools_rename", "*"]), { mcpServers: new Set(["lsp-tools"]) });
		const hover = decide(c, { toolName: "mcp__lsp-tools__hover", input: {} });
		expect(hover.action).toBe("lsp-tools_hover");
		expect(hover.effect).toBe("allow");
		const rename = decide(c, { toolName: "mcp__lsp-tools__rename", input: {} });
		expect(rename.action).toBe("lsp-tools_rename");
		expect(rename.effect).toBe("deny");
	});

	test("external_directory: outside the cwd git repo requires an external_directory rule", () => {
		const repo = realTemp("tsuna-repo-");
		mkdirSync(join(repo, ".git"));
		writeFileSync(join(repo, "inside.txt"), "in");
		const outside = realTemp("tsuna-out-");
		writeFileSync(join(outside, "f.txt"), "out");
		const sub = join(repo, "pkg");
		mkdirSync(sub);
		const base = R(["deny", "*", "*"], ["allow", "read", "*"]);
		const noRule = ctx(base, { cwd: sub });
		// Inside the repo (above cwd but within the git root) is not external.
		expect(decide(noRule, read(join(repo, "inside.txt"))).effect).toBe("allow");
		expect(decide(noRule, read("../inside.txt")).effect).toBe("allow");
		const denied = decide(noRule, read(join(outside, "f.txt")));
		expect(denied.effect).toBe("deny");
		expect(denied.reason).toBe("External directory access is not permitted");
		// Traversal is canonicalised.
		expect(decide(noRule, read(`../../${outside.split("/").pop()}/f.txt`)).effect).toBe("deny");
		const allowed = ctx([...base, ...R(["allow", "external_directory", `${outside}/*`])], { cwd: sub });
		expect(decide(allowed, read(join(outside, "f.txt"))).effect).toBe("allow");
		const asks = ctx([...base, ...R(["ask", "external_directory", "*"])], { cwd: sub });
		expect(decide(asks, read(join(outside, "f.txt"))).effect).toBe("ask");
		// grep/glob paths are checked too.
		expect(decide(noRule, { toolName: "grep", input: { pattern: "x", path: outside } }).effect).toBe("deny");
	});

	test("~/ is expanded in both rules and paths", () => {
		const home = process.env.HOME!;
		mkdirSync(join(home, "notes"), { recursive: true });
		writeFileSync(join(home, "notes", "a.txt"), "a");
		writeFileSync(join(home, "other.txt"), "o");
		const c = ctx(R(["deny", "*", "*"], ["allow", "read", "~/notes/*"], ["allow", "external_directory", "~/notes/*"]));
		const d = decide(c, read("~/notes/a.txt"));
		expect(d.effect).toBe("allow");
		expect(decide(c, read(join(realpathSync(home), "notes", "a.txt"))).effect).toBe("allow");
		expect(decide(c, read("~/other.txt")).effect).toBe("deny");
	});

	test(".env asks for orchestrator rules while .env.example is allowed", () => {
		const ws = realTemp("tsuna-env-");
		writeFileSync(join(ws, ".env"), "SECRET=1");
		writeFileSync(join(ws, ".env.example"), "SECRET=");
		const c = ctx(rulesOf("orchestrator"), { role: "orchestrator", cwd: ws });
		const env = decide(c, read(".env"));
		expect(env.effect).toBe("ask");
		expect(env.genericAsk).toBe(false);
		expect(decide(c, read(".env.local")).effect).toBe("ask");
		expect(decide(c, read(".env.example")).effect).toBe("allow");
		expect(decide(c, read("README.md")).effect).toBe("allow");
	});

	test("shell && chains are checked per segment", () => {
		const c = ctx(R(["deny", "*", "*"], ["allow", "shell", "echo *"]));
		expect(decide(c, shell("echo a")).effect).toBe("allow");
		expect(decide(c, shell("echo a && echo b")).effect).toBe("allow");
		const bad = decide(c, shell("echo a && rm x"));
		expect(bad.effect).toBe("deny");
		expect(bad.reason).toBe("Shell segment is not permitted");
		// A whole-string glob does not admit a trailing chained command.
		expect(decide(c, shell("echo a && echo b && rm -rf /")).effect).toBe("deny");
	});

	test("complex shell asks in source mode, allows in auto mode unless guarded", () => {
		const rules = R(["deny", "*", "*"], ["allow", "shell", "*"]);
		const source = ctx(rules);
		const auto = ctx(rules, { approvalMode: "auto" });
		const pipeline = "echo a | grep a";
		const s = decide(source, shell(pipeline));
		expect(s.effect).toBe("ask");
		expect(s.genericAsk).toBe(true);
		expect(s.reason).toContain("Complex shell command");
		expect(decide(auto, shell(pipeline)).effect).toBe("allow");
		for (const [cmd, reason] of [
			["rm -rf build | cat", /Recursive deletion/],
			["git push origin main; echo done", /Git publishing/],
			["git reset --hard HEAD~1 | cat", /hard reset/],
			["git clean -fdx || true", /Git publishing or cleanup/],
		] as const) {
			const d = decide(auto, shell(cmd));
			expect(d.effect).toBe("ask");
			expect(d.reason).toMatch(reason);
			expect(d.genericAsk).toBe(false);
		}
	});

	test("auto mode drops generic asks but keeps specific asks", () => {
		const rules = R(["ask", "*", "*"], ["allow", "shell", "*"], ["ask", "shell", "git push*"]);
		const auto = ctx(rules, { approvalMode: "auto" });
		expect(decide(auto, { toolName: "glob", input: {} }).effect).toBe("allow");
		expect(decide(auto, shell("git push origin x")).effect).toBe("ask");
		expect(decide(ctx(rules), { toolName: "glob", input: {} }).effect).toBe("ask");
	});

	test("exec-capable flags require approval even when the program is allowed", () => {
		const rules = R(["deny", "*", "*"], ["allow", "shell", "rg *"], ["allow", "shell", "fd *"], ["allow", "shell", "find *"], ["allow", "shell", "sed *"], ["allow", "shell", "xargs *"]);
		for (const mode of ["source", "auto"] as const) {
			const c = ctx(rules, { approvalMode: mode });
			expect(decide(c, shell("rg foo src")).effect).toBe("allow");
			for (const cmd of ["rg --pre cat foo", "rg --pre=cat foo", "rg -z foo", "fd -x rm", "fd --exec rm {}", "find . -exec rm {} ;", "find . -delete", "sed -i s/a/b/ f.txt", "sed -n 1e f", "xargs rm"]) {
				const d = decide(c, shell(cmd));
				expect([cmd, d.effect]).toEqual([cmd, "ask"]);
				expect(d.genericAsk).toBe(false);
			}
		}
		expect(decide(ctx(rules), shell("sed -n 1p f.txt")).effect).toBe("allow");
	});

	test("workplan ownership is enforced by role, regardless of allow rules", () => {
		const all = R(["allow", "*", "*"]);
		const servers = new Set(["workplan", "other"]);
		const as = (role: string) => ctx(all, { role, mcpServers: servers });
		const wp = (tool: string, input: Record<string, unknown> = {}) => ({ toolName: `mcp__workplan__${tool}`, input });
		const create = decide(as("code-engineer"), wp("create"));
		expect(create.effect).toBe("deny");
		expect(create.reason).toContain("Only plan and orchestrator");
		expect(decide(as("plan"), wp("create")).effect).toBe("allow");
		expect(decide(as("orchestrator"), wp("create")).effect).toBe("allow");
		expect(decide(as("plan"), wp("checkpoint")).reason).toContain("Only orchestrator may update workplan checkpoints");
		expect(decide(as("orchestrator"), wp("checkpoint")).effect).toBe("allow");
		expect(decide(as("plan"), wp("update")).effect).toBe("allow");
		expect(decide(as("plan"), wp("update", { recovery: { tx: 1 } })).reason).toContain("Only orchestrator may recover");
		expect(decide(as("orchestrator"), wp("update", { recovery: { tx: 1 } })).effect).toBe("allow");
		expect(decide(as("plan"), wp("compact", { mode: "apply" })).effect).toBe("deny");
		expect(decide(as("plan"), { toolName: "mcp_resource", input: { server: "workplan" } }).effect).toBe("allow");
		expect(decide(as("plan"), { toolName: "mcp_resource", input: { server: "other" } }).reason).toContain("plan agent may only read MCP resources from the workplan server");
		expect(decide(as("orchestrator"), { toolName: "mcp_resource", input: { server: "other" } }).effect).toBe("allow");
		// The sample code-engineer rules also deny it.
		expect(decide(ctx(rulesOf("code-engineer"), { role: "code-engineer", mcpServers: servers }), wp("create")).effect).toBe("deny");
	});

	test("canSurfaceTool hides tools whose action's last rule is deny", () => {
		const c = ctx(R(["allow", "*", "*"], ["deny", "shell", "*"]));
		expect(canSurfaceTool(c, "bash")).toBe(false);
		expect(canSurfaceTool(c, "read")).toBe(true);
		expect(canSurfaceTool(c, "yield")).toBe(true);
		expect(canSurfaceTool(c, "frobnicate")).toBe(false);
		const closed = ctx(R(["deny", "*", "*"], ["allow", "read", "*"]));
		expect(canSurfaceTool(closed, "edit")).toBe(false);
		expect(canSurfaceTool(closed, "read")).toBe(true);
		// A resource-specific allow keeps the tool visible (execution-time decide remains authoritative).
		expect(canSurfaceTool(ctx(R(["deny", "*", "*"], ["allow", "shell", "echo *"])), "bash")).toBe(true);
		expect(canSurfaceTool(ctx(rulesOf("explore")), "bash")).toBe(false);
	});
});

describe("PermissionGate", () => {
	function session(rules: PermissionRule[], interactive: boolean, extra: Partial<PolicyContext> = {}): GateSession {
		return { agentId: "a-1", interactive, policy: ctx(rules, { role: "orchestrator", ...extra }) };
	}
	const genericAsk = R(["ask", "*", "*"]);
	const specificAsk = R(["allow", "shell", "*"], ["ask", "shell", "git push*"]);
	const glob = { toolName: "glob", input: { pattern: "*" } };

	test("headless sessions fail closed on ask", async () => {
		let asked = 0;
		const gate = new PermissionGate({ approver: async () => (asked++, true) });
		const r = await gate.authorize(session(genericAsk, false), glob);
		expect(r.allowed).toBe(false);
		if (!r.allowed) expect(r.reason).toContain("interactive primary session");
		expect(asked).toBe(0);
		expect(gate.audit.at(-1)).toMatchObject({ effect: "deny", source: "headless", toolName: "glob", action: "glob" });
	});

	test("interactive sessions consult the approver and honour its answer", async () => {
		const requests: ApprovalRequest[] = [];
		let answer = true;
		const gate = new PermissionGate({ approver: async req => (requests.push(req), answer) });
		const s = session(specificAsk, true);
		const yes = await gate.authorize(s, shell("git push origin x"));
		expect(yes.allowed).toBe(true);
		expect(gate.audit.at(-1)).toMatchObject({ effect: "allow", source: "human", action: "shell", resource: "git push origin x" });
		answer = false;
		const no = await gate.authorize(s, shell("git push origin x"));
		expect(no.allowed).toBe(false);
		if (!no.allowed) expect(no.reason).toBe("Denied by user");
		expect(gate.audit.at(-1)).toMatchObject({ effect: "deny", source: "human" });
		expect(requests).toHaveLength(2);
		expect(requests[0]).toMatchObject({ agentId: "a-1", role: "orchestrator", toolName: "bash" });
		// Allowed calls never reach the approver.
		const allowed = await gate.authorize(s, shell("ls"));
		expect(allowed.allowed).toBe(true);
		expect(requests).toHaveLength(2);
		expect(gate.audit.at(-1)!.source).toBe("policy");
	});

	test("approver failure or absence denies", async () => {
		const throwing = new PermissionGate({ approver: async () => { throw new Error("ui crashed"); } });
		expect((await throwing.authorize(session(genericAsk, true), glob)).allowed).toBe(false);
		const none = new PermissionGate();
		const r = await none.authorize(session(genericAsk, true), glob);
		expect(r.allowed).toBe(false);
		if (!r.allowed) expect(r.reason).toContain("no interactive approver");
	});

	test("reviewer waives only generic, low-risk, within-request asks", async () => {
		const reviewed: ApprovalRequest[] = [];
		let verdict: ReviewVerdict = { risk: "low", scope: "within-request", reason: "harmless" };
		let approved = 0;
		const gate = new PermissionGate({
			reviewer: async req => (reviewed.push(req), verdict),
			approver: async () => (approved++, false),
		});
		const waived = await gate.authorize(session(genericAsk, true), glob);
		expect(waived.allowed).toBe(true);
		expect(gate.audit.at(-1)).toMatchObject({ effect: "allow", source: "reviewer", reason: "harmless" });
		expect(approved).toBe(0);

		verdict = { risk: "medium", scope: "within-request", reason: "hmm" };
		expect((await gate.authorize(session(genericAsk, true), glob)).allowed).toBe(false);
		expect(approved).toBe(1);
		verdict = { risk: "low", scope: "outside-request", reason: "scope" };
		expect((await gate.authorize(session(genericAsk, true), glob)).allowed).toBe(false);
		expect(approved).toBe(2);

		// A specific ask is never sent to the reviewer, even if it would say low risk.
		verdict = { risk: "low", scope: "within-request", reason: "fine" };
		const before = reviewed.length;
		const push = await gate.authorize(session(specificAsk, true), shell("git push origin main"));
		expect(push.allowed).toBe(false);
		expect(reviewed.length).toBe(before);
		expect(approved).toBe(3);
	});

	test("reviewer never overrides a deny and its failure preserves the ask", async () => {
		let reviewerCalls = 0;
		let approverCalls = 0;
		const gate = new PermissionGate({
			reviewer: async () => {
				reviewerCalls++;
				throw new Error("reviewer down");
			},
			approver: async () => (approverCalls++, true),
		});
		const denied = await gate.authorize(session(R(["deny", "*", "*"]), true), glob);
		expect(denied.allowed).toBe(false);
		expect(reviewerCalls).toBe(0);
		expect(approverCalls).toBe(0);
		expect(gate.audit.at(-1)).toMatchObject({ effect: "deny", source: "policy" });
		const asked = await gate.authorize(session(genericAsk, true), glob);
		expect(reviewerCalls).toBe(1);
		expect(approverCalls).toBe(1);
		expect(asked.allowed).toBe(true);
		expect(gate.audit.at(-1)).toMatchObject({ effect: "allow", source: "human" });
	});

	test("onAudit receives every entry", async () => {
		const seen: string[] = [];
		const gate = new PermissionGate({ onAudit: e => seen.push(`${e.effect}:${e.source}`) });
		await gate.authorize(session(R(["allow", "*", "*"]), false), glob);
		await gate.authorize(session(R(["deny", "*", "*"]), false), glob);
		expect(seen).toEqual(["allow:policy", "deny:policy"]);
	});
});

describe("enforcement through the runtime", () => {
	const yieldNow = () => ({ toolCalls: [{ name: "yield", arguments: { data: { answer: "x", findings: [] } } }] });

	test("explore child: external reads denied, bash/edit absent, batch calls gated individually", async () => {
		setScript(yieldNow);
		const r = await rig();
		writeFileSync(join(r.workspace, "inside.txt"), "hello inside\n");
		const outside = realTemp("tsuna-outside-");
		writeFileSync(join(outside, "secret.txt"), "top secret\n");
		const rt = r.harness.runtime;
		const child = rt.spawn(rt.primaryId, { agent: "explore", task: "look around" });
		await child.done;
		expect(rt.record(child.id)!.status).toBe("idle");

		const inside = await rt.invokeTool(child.id, "read", "c1", { path: "inside.txt" });
		expect(inside.isError).toBeFalsy();
		expect(inside.text).toContain("hello inside");

		const ext = await rt.invokeTool(child.id, "read", "c2", { path: join(outside, "secret.txt") });
		expect(ext.isError).toBe(true);
		expect(ext.text).toContain("Permission denied");
		expect(ext.text).not.toContain("top secret");

		const bash = await rt.invokeTool(child.id, "bash", "c3", { command: "echo hi" });
		expect(bash.isError).toBe(true);
		expect(bash.text).toContain("not in this agent's catalog");
		const edit = await rt.invokeTool(child.id, "edit", "c4", { path: "inside.txt", expected_tag: "x", edits: [{ old_text: "a", new_text: "b" }] });
		expect(edit.text).toContain("not in this agent's catalog");

		const batch = await rt.invokeTool(child.id, "batch", "c5", {
			calls: [
				{ tool: "read", input: { path: "inside.txt" } },
				{ tool: "read", input: { path: join(outside, "secret.txt") } },
				{ tool: "bash", input: { command: "echo nested" } },
			],
		});
		expect(batch.text.match(/\(error\)/g)).toHaveLength(2);
		expect(batch.text).toContain("hello inside");
		expect(batch.text).toContain("Permission denied");
		expect(batch.text).not.toContain("top secret");
		expect(batch.isError).toBe(false);
		const permissionEvents = r.events.filter(e => e.type === "permission" && e.agentId === child.id);
		expect(permissionEvents.length).toBeGreaterThanOrEqual(4);
	});

	test("headless orchestrator primary: ask becomes deny", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		const out = await rt.invokeTool(rt.primaryId, "bash", "p1", { command: "git push origin x" });
		expect(out.isError).toBe(true);
		expect(out.text).toContain("Permission denied");
		expect(out.text).toContain("interactive primary session");
		expect(r.harness.gate.audit.at(-1)).toMatchObject({ source: "headless", action: "shell", resource: "git push origin x" });
	});

	test("interactive orchestrator primary: approver is consulted and honoured", async () => {
		setScript(yieldNow);
		const requests: ApprovalRequest[] = [];
		let answer = false;
		const r = await rig({ interactive: true, approver: async req => (requests.push(req), answer) });
		writeFileSync(join(r.workspace, ".env"), "TOKEN=abc\n");
		const rt = r.harness.runtime;
		const push = await rt.invokeTool(rt.primaryId, "bash", "p1", { command: "git push origin x" });
		expect(push.text).toContain("Permission denied: Denied by user");
		expect(requests.at(-1)).toMatchObject({ toolName: "bash", role: "orchestrator", agentId: rt.primaryId });
		expect(requests.at(-1)!.decision.resource).toBe("git push origin x");

		const denied = await rt.invokeTool(rt.primaryId, "read", "p2", { path: ".env" });
		expect(denied.text).toContain("Denied by user");
		answer = true;
		const allowed = await rt.invokeTool(rt.primaryId, "read", "p3", { path: ".env" });
		expect(allowed.isError).toBeFalsy();
		expect(allowed.text).toContain("TOKEN=abc");
		expect(requests).toHaveLength(3);
		expect(requests[2]!.decision.action).toBe("read");
		// Children stay headless even under an interactive primary.
		const child = rt.spawn(rt.primaryId, { agent: "explore", task: "x" });
		await child.done;
		const childEnv = await rt.invokeTool(child.id, "read", "c1", { path: ".env" });
		expect(childEnv.text).toContain("interactive primary session");
		expect(requests).toHaveLength(3);
	});
});
