import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { McpConfig } from "../src/config.ts";
import { startHttpFixture } from "../fixtures/mcp/http-server.ts";
import { EXPERIMENT, setScript, startRig, tempDir, testConfig, type TestRig } from "./helpers/harness.ts";
import { ORCH_RULES, ORCH_TOOLS, writePack } from "./helpers/pack.ts";

const STDIO = join(EXPERIMENT, "fixtures/mcp/stdio-server.ts");
const rigs: TestRig[] = [];
const stops: (() => void)[] = [];
afterEach(async () => {
	for (const r of rigs.splice(0)) await r.harness.shutdown();
	for (const s of stops.splice(0)) s();
});

function mcpPack() {
	const dir = tempDir("tsuna-mcp-pack-");
	return writePack(dir, "mcp", [
		{
			name: "boss",
			model: "primary",
			primary: true,
			tools: [...ORCH_TOOLS, "batch", "read", "mcp__*", "mcp_resource"],
			spawns: ["helper"],
			permissions: [...ORCH_RULES, ["allow", "fixture_echo", "*"], ["allow", "fixture_roots", "*"], ["deny", "fixture_slow", "*"], ["allow", "workplan_*", "*"], ["ask", "workplan_compact", "*"], ["allow", "http_*", "*"], ["allow", "mcp_resource", "fixture"]],
		},
		{
			name: "helper",
			tools: ["read", "mcp__*", "batch"],
			// Rules allow every workplan action, yet ownership still denies lifecycle writes.
			permissions: [["deny", "*", "*"], ["allow", "workplan_*", "*"], ["allow", "fixture_echo", "*"]],
		},
	]);
}

function servers(log: string, extra: McpConfig["servers"] = {}): McpConfig {
	return {
		servers: {
			fixture: { type: "stdio", enabled: true, command: process.execPath, args: [STDIO, "basic"], env: { FIXTURE_LOG: log } },
			workplan: { type: "stdio", enabled: true, command: process.execPath, args: [STDIO, "workplan"], env: { FIXTURE_LOG: log } },
			never: { type: "stdio", enabled: false, command: process.execPath, args: [STDIO, "basic"], env: { FIXTURE_LOG: `${log}.never` } },
			...extra,
		},
	};
}

async function rig(extra: Parameters<typeof startRig>[0] = {}, mcpExtra: McpConfig["servers"] = {}) {
	const log = join(tempDir("tsuna-mcp-log-"), "calls.log");
	const config = testConfig({ agentPacks: [mcpPack()], primaryRole: "boss", mcp: servers(log, mcpExtra) });
	const r = await startRig({ config, ...extra });
	rigs.push(r);
	return { ...r, log };
}

const calls = (log: string) => (existsSync(log) ? readFileSync(log, "utf8").split("\n").filter(l => l.startsWith("call ") || l.startsWith("resource ")) : []);

describe("MCP integration", () => {
	test("enabled servers connect; disabled ones are preserved but never started; roots bind the workspace", async () => {
		const r = await rig();
		const states = Object.fromEntries([...r.harness.mcp.states.values()].map(s => [s.name, s.status]));
		expect(states).toEqual({ fixture: "connected", workplan: "connected", never: "disabled" });
		expect(existsSync(`${r.log}.never`)).toBe(false);
		const rt = r.harness.runtime;
		expect(rt.record(rt.primaryId)!.contract.tools).toContain("mcp__fixture__echo");
		expect(rt.record(rt.primaryId)!.contract.tools).not.toContain("mcp__never__echo");
		const roots = await rt.invokeTool(rt.primaryId, "mcp__fixture__roots", "c1", {});
		expect(JSON.parse(roots.text)).toEqual([{ uri: `file://${r.workspace}`, name: "workspace" }]);
	});

	test("every MCP call and resource read traverses the permission gate, including nested batch calls", async () => {
		const r = await rig();
		const rt = r.harness.runtime;
		expect((await rt.invokeTool(rt.primaryId, "mcp__fixture__echo", "c1", { text: "hi" })).text).toBe("echo: hi");
		// Catalog filtering hides a denied tool...
		const hidden = await rt.invokeTool(rt.primaryId, "mcp__fixture__slow", "c2", { ms: 10 });
		expect(hidden.text).toContain("not in this agent's catalog");
		// ...and the execution-time gate still denies a catalogued tool whose rule now denies it.
		const nestedOk = await rt.invokeTool(rt.primaryId, "batch", "c3", { calls: [{ tool: "mcp__fixture__echo", input: { text: "nested" } }] });
		expect(nestedOk.text).toContain("echo: nested");
		rt.record(rt.primaryId)!.contract.permissions.push({ effect: "deny", action: "fixture_echo", resource: "*" });
		const nestedDenied = await rt.invokeTool(rt.primaryId, "batch", "c3b", { calls: [{ tool: "mcp__fixture__roots", input: {} }, { tool: "mcp__fixture__echo", input: { text: "blocked" } }] });
		expect(nestedDenied.text).toContain("mcp__fixture__echo (error): Permission denied");
		const direct = await rt.invokeTool(rt.primaryId, "mcp__fixture__echo", "c3c", { text: "blocked" });
		expect(direct.text).toContain("Permission denied");
		expect((await rt.invokeTool(rt.primaryId, "mcp_resource", "c4", { server: "fixture", uri: "fixture://note" })).text).toContain("resource fixture://note");
		const otherServer = await rt.invokeTool(rt.primaryId, "mcp_resource", "c5", { server: "workplan", uri: "workplan://x" });
		expect(otherServer.text).toContain("Permission denied");
		const unconfigured = await rt.invokeTool(rt.primaryId, "mcp_resource", "c6", { server: "never", uri: "x" });
		expect(unconfigured.text).toContain("Permission denied");
		// The server saw exactly the allowed calls.
		expect(calls(r.log)).toEqual([`call echo {"text":"hi"}`, `call echo {"text":"nested"}`, "call roots {}", "resource fixture://note"]);
		expect(r.harness.gate.audit.filter(a => a.effect === "deny").length).toBeGreaterThanOrEqual(3);
	});

	test("Shiori-style client write approval is authorized only by the Tsuna gate and role ownership", async () => {
		setScript(() => ({ toolCalls: [{ name: "yield", arguments: { data: {} } }] }));
		const approvals: string[] = [];
		const r = await rig({ interactive: true, approver: async req => (approvals.push(`${req.toolName}`), true) });
		const rt = r.harness.runtime;
		const wpLog = join(r.workspace, ".tsuna-fixture-workplan.log");
		// Primary (owner role "boss" is not orchestrator/plan): lifecycle writes denied by ownership.
		const create = await rt.invokeTool(rt.primaryId, "mcp__workplan__create", "w1", { id: "p1" });
		expect(create.text).toContain("Only plan and orchestrator");
		// Read-only actions pass.
		expect((await rt.invokeTool(rt.primaryId, "mcp__workplan__read", "w2", { id: "p1" })).text).toContain("read ok");
		// An `ask` rule goes to the interactive approver.
		expect((await rt.invokeTool(rt.primaryId, "mcp__workplan__compact", "w3", { id: "p1" })).text).toContain("compact ok");
		expect(approvals).toEqual(["mcp__workplan__compact"]);
		// A headless child with allow rules is still denied lifecycle writes.
		const { id, done } = rt.spawn(rt.primaryId, { agent: "helper", task: "t" });
		await done;
		for (const tool of ["create", "update", "checkpoint", "reset"]) {
			const out = await rt.invokeTool(id, `mcp__workplan__${tool}`, `h-${tool}`, { id: "p1" });
			expect(out.text).toContain("Permission denied");
		}
		const writes = existsSync(wpLog) ? readFileSync(wpLog, "utf8").trim().split("\n") : [];
		expect(writes).toEqual([`compact {"id":"p1"}`]);
	});

	test("an orchestrator-role primary may create workplans; non-interactive asks fail closed", async () => {
		const dir = tempDir("tsuna-orch-pack-");
		const pack = writePack(dir, "orch", [
			{ name: "orchestrator", model: "primary", primary: true, tools: ["mcp__*"], permissions: [["deny", "*", "*"], ["allow", "workplan_*", "*"], ["ask", "workplan_compact", "*"]] },
		]);
		const log = join(tempDir("tsuna-mcp-log-"), "calls.log");
		const config = testConfig({ agentPacks: [pack], primaryRole: "orchestrator", mcp: servers(log) });
		const r = await startRig({ config });
		rigs.push(r);
		const rt = r.harness.runtime;
		expect((await rt.invokeTool(rt.primaryId, "mcp__workplan__create", "o1", { id: "p2" })).text).toContain("create ok");
		const compact = await rt.invokeTool(rt.primaryId, "mcp__workplan__compact", "o2", { id: "p2" });
		expect(compact.text).toContain("interactive primary session");
		expect(readFileSync(join(r.workspace, ".tsuna-fixture-workplan.log"), "utf8").trim().split("\n")).toEqual([`create {"id":"p2"}`]);
	});

	test("cancellation reaches the server and shutdown closes owned connections", async () => {
		const r = await rig();
		const rt = r.harness.runtime;
		const controller = new AbortController();
		// Catalog `slow` for this check (contract edited in memory only).
		const boss = rt.record(rt.primaryId)!;
		boss.contract.permissions.push({ effect: "allow", action: "fixture_slow", resource: "*" });
		boss.contract.tools.push("mcp__fixture__slow");
		await rt.park(rt.primaryId).catch(() => undefined);
		const t0 = Date.now();
		const pending = rt.invokeTool(rt.primaryId, "mcp__fixture__slow", "s1", { ms: 5000 }, controller.signal);
		setTimeout(() => controller.abort(), 100);
		const out = await pending;
		expect(Date.now() - t0).toBeLessThan(2500);
		expect(out.isError).toBe(true);
		const pids = readFileSync(r.log, "utf8").split("\n").filter(l => l.startsWith("start ")).map(l => Number(l.split(" pid ")[1]));
		expect(pids).toHaveLength(2);
		await r.harness.shutdown();
		rigs.splice(rigs.indexOf(r), 1);
		await Bun.sleep(100);
		for (const pid of pids) {
			let alive = true;
			try {
				process.kill(pid, 0);
			} catch {
				alive = false;
			}
			expect(alive).toBe(false);
		}
		expect([...r.harness.mcp.states.values()].filter(s => s.status === "connected")).toHaveLength(0);
	});

	test("streamable HTTP servers work; credentials come only from allowlisted environment variables", async () => {
		const http = startHttpFixture({ token: "s3cret" });
		stops.push(http.stop);
		process.env.TSUNA_TEST_HTTP_TOKEN = "s3cret";
		const r = await rig({}, {
			http: { type: "http", enabled: true, url: http.url, headers: { authorization: "Bearer ${TSUNA_TEST_HTTP_TOKEN}" }, envAllow: ["TSUNA_TEST_HTTP_TOKEN"] },
			leaky: { type: "http", enabled: true, url: http.url, headers: { authorization: "Bearer ${TSUNA_TEST_HTTP_TOKEN}" } },
		});
		const rt = r.harness.runtime;
		expect((await rt.invokeTool(rt.primaryId, "mcp__http__lookup", "h1", { q: "react" })).text).toBe("lookup:react");
		expect(r.harness.mcp.states.get("leaky")!.status).toBe("failed");
		expect(r.harness.mcp.states.get("leaky")!.error).toContain("not in envAllow");
		expect(http.calls).toHaveLength(1);
		delete process.env.TSUNA_TEST_HTTP_TOKEN;
	});
});
