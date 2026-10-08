import { afterEach, expect, test } from "bun:test";
import { mkdtemp, mkdir, rm, symlink, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { AgentRegistry, createAgentSession, discoverAuthStorage, ModelRegistry, SessionManager, Settings } from "@oh-my-pi/pi-coding-agent";
import { getBundledModel } from "@oh-my-pi/pi-catalog";
import { AgentLifecycleManager } from "@oh-my-pi/pi-coding-agent/registry/agent-lifecycle";
import policies from "../config/permissions.json";
import { type CoordinationScope, type CoordinationRef } from "../lib/coordination";
import { asToolCall, decidePermission, type PermissionPolicies } from "../lib/permissions";

const cleanup: string[] = [];
afterEach(async () => { for (const dir of cleanup.splice(0)) await rm(dir, { recursive: true, force: true }); });

async function fixture() {
	const dir = await mkdtemp(join(tmpdir(), "hoshi-coordination-test-"));
	cleanup.push(dir);
	const root = join(dir, "root.jsonl");
	await mkdir(join(dir, "root"));
	const files = { Main: root, Child: join(dir, "root/Child.jsonl"), Grandchild: join(dir, "root/Grandchild.jsonl"), Other: join(dir, "other.jsonl"), Advisor: join(dir, "root/Advisor.jsonl") };
	for (const [id, file] of Object.entries(files)) await writeFile(file, [
		JSON.stringify({ type: "session", version: 3, id, cwd: dir, timestamp: new Date().toISOString() }),
		JSON.stringify({ type: "message", id: `${id}-message`, parentId: null, timestamp: new Date().toISOString(), message: { role: "user", content: [{ type: "text", text: `${id} transcript fixture` }], timestamp: Date.now() } }),
	].join("\n") + "\n");
	const agents: CoordinationRef[] = Object.entries(files).map(([id, sessionFile]) => ({
		id, sessionFile, kind: id === "Main" ? "main" : id === "Advisor" ? "advisor" : "sub", status: "parked",
		parentId: id === "Grandchild" ? "Child" : "Main",
	}));
	const scope: CoordinationScope = { callerId: "Main", callerSessionFile: root, rootSessionFile: root, agents };
	return { dir, root, files, agents, scope };
}

function decision(scope: CoordinationScope | undefined, name: string, path: string, role = "orchestrator") {
	return decidePermission(policies as PermissionPolicies, role, asToolCall(name, { path, content: "Please check tests" }), { approvalMode: "auto", coordinationScope: scope });
}

test("trusted allocated session paths remain bound before the first durable transcript write", async () => {
	const { scope, dir } = await fixture();
	const rootSessionFile = join(dir, "allocated.jsonl");
	const fresh = { ...scope, callerSessionFile: rootSessionFile, rootSessionFile,
		agents: [{ id: "Child", kind: "sub", parentId: "Main", status: "running", sessionFile: join(dir, "allocated/Child.jsonl") }] };
	expect(decision(fresh, "read", "history://").effect).toBe("allow");
	expect(decision(fresh, "write", "agent://Child").effect).toBe("allow");
	expect(decision(fresh, "read", "history://Other").effect).toBe("deny");
});

test("history and output reads accept current-tree targets, selectors and aliases", async () => {
	const { scope } = await fixture();
	for (const path of ["history://", "history://:1-20", "history://Child", "history://child:raw:1-20", "history:/Child", "agent://Child", "agent://Child/reports/0", "AGENT://Child"]) {
		expect(decision(scope, "read", path).effect).toBe("allow");
	}
	for (const path of ["history://Other", "agent://Other", "history://Advisor", "history://missing", "history://Child/invalid", "history://Child?other=1"]) {
		expect(decision(scope, "read", path).effect).toBe("deny");
	}
	expect(decision(scope, "read", "history://current/full").effect).toBe("allow");
	expect(decision(undefined, "read", "history://Child").effect).toBe("deny");
	// Read-only roles cannot acquire coordinator powers through a virtual URL.
	expect(decision(scope, "read", "history://Child", "tester").effect).toBe("deny");
});

test("messages allow live or parked direct children and retain source role denials", async () => {
	const { scope, files } = await fixture();
	for (const path of ["agent://Child", "agent:/Child"]) expect(decision(scope, "write", path).effect).toBe("allow");
	for (const path of ["agent://Grandchild", "agent://Other", "agent://Advisor", "agent://all", "agent://Main", "agent://missing", "agent://Child/key", "agent://Child:1-20", "history://Child"]) {
		expect(decision(scope, "write", path).effect).toBe("deny");
	}
	expect(decision(scope, "edit", "agent://Child").effect).toBe("deny");
	expect(decision(scope, "write", "agent://Child", "document-writer").effect).toBe("deny");
	expect(decision(scope, "write", "agent://Child", "code-checker").effect).toBe("deny");
	const childScope = { ...scope, callerId: "Child", callerSessionFile: files.Child };
	expect(decision(childScope, "write", "agent://Grandchild").effect).toBe("allow");
	expect(decision(childScope, "write", "agent://Child").effect).toBe("deny");
	expect(decision({ ...scope, agents: scope.agents.map(a => a.id === "Child" ? { ...a, status: "aborted" } : a) }, "write", "agent://Child").effect).toBe("deny");
});

test("symlinked transcripts cannot cross the session boundary", async () => {
	const { scope, files, dir } = await fixture();
	const escaped = join(dir, "root/Escape.jsonl");
	await symlink(files.Other, escaped);
	const scopeWithEscape = { ...scope, agents: [...scope.agents, { id: "Escape", kind: "sub", parentId: "Main", status: "parked", sessionFile: escaped }] };
	expect(decision(scopeWithEscape, "read", "history://Escape").effect).toBe("deny");
	expect(decision(scopeWithEscape, "write", "agent://Escape").effect).toBe("deny");
});

test("native SDK tools read a scoped index and deliver a child message without model calls", async () => {
	const { dir, root, files } = await fixture();
	const agentDir = join(dir, "profile");
	await mkdir(agentDir);
	await writeFile(join(agentDir, "config.yml"), `tools:\n  approvalMode: yolo\nmemory:\n  backend: off\nextensions:\n  - ${resolve(import.meta.dir, "../extensions/permissions.ts")}\n`);
	const settings = await Settings.loadIsolated({ cwd: dir, agentDir });
	const authStorage = await discoverAuthStorage(agentDir);
	const modelRegistry = new ModelRegistry(authStorage, join(agentDir, "models.yml"), { settings });
	const sessionManager = await SessionManager.open(root);
	const { session } = await createAgentSession({ cwd: dir, agentDir, settings, authStorage, modelRegistry,
		model: getBundledModel("anthropic", "claude-opus-5-5"), sessionManager, enableMCP: false, cacheWarming: false, bindProcessState: false });
	const registry = AgentRegistry.global();
	const delivered: unknown[] = [];
	try {
		const childSession = { messages: [{ role: "user", content: [{ type: "text", text: "Child transcript fixture" }], timestamp: Date.now() }],
			deliverIrcMessage: async (message: unknown) => { delivered.push(message); return "injected"; }, dispose: async () => {} };
		registry.register({ id: "Child", kind: "sub", displayName: "Child", parentId: "Main", sessionFile: files.Child, status: "running",
			session: childSession as never });
		registry.register({ id: "Other", kind: "sub", displayName: "Other", parentId: "Main", sessionFile: files.Other, status: "running", session: null });
		const read = session.agent.state.tools.find(tool => tool.name === "read")!;
		const write = session.agent.state.tools.find(tool => tool.name === "write")!;
		const signal = new AbortController().signal;
		const index = await read.execute("index", { path: "history://" }, signal);
		expect(JSON.stringify(index.content)).toContain("history://Child");
		expect(JSON.stringify(index.content)).not.toContain("Other");
		const transcript = await read.execute("history", { path: "history://Child" }, signal);
		expect(JSON.stringify(transcript.content)).toContain("Child transcript fixture");
		await mkdir(join(dir, "other"));
		await writeFile(join(dir, "other/Child.md"), "foreign Child output");
		const progress = await read.execute("progress", { path: "agent://Child" }, signal);
		expect(JSON.stringify(progress.content)).not.toContain("foreign Child output");
		expect(JSON.stringify(progress.content)).toContain("Child");
		await writeFile(join(dir, "root/Child.md"), "owned Child output");
		await writeFile(join(dir, "root/Child.json"), JSON.stringify({ reports: [{ data: "owned JSON" }] }));
		expect(JSON.stringify((await read.execute("output", { path: "agent://Child" }, signal)).content)).toContain("owned Child output");
		expect(JSON.stringify((await read.execute("json", { path: "agent://Child/reports/0/data" }, signal)).content)).toContain("owned JSON");
		await rm(join(dir, "root/Child.json"));
		await symlink(join(dir, "other/Child.md"), join(dir, "root/Child.json"));
		await expect(read.execute("escape-json", { path: "agent://Child/reports" }, signal)).rejects.toThrow("outside the current session tree");
		await rm(join(dir, "root/Child.md"));
		await symlink(join(dir, "other/Child.md"), join(dir, "root/Child.md"));
		await expect(read.execute("escape-output", { path: "agent://Child" }, signal)).rejects.toThrow("outside the current session tree");
		const sent = await write.execute("message", { path: "agent://Child", content: "Please check tests" }, signal);
		expect(sent.isError).not.toBe(true);
		expect(delivered).toHaveLength(1);
		expect(delivered[0]).toMatchObject({ to: "Child", body: "Please check tests" });
		registry.register({ id: "Child", kind: "sub", displayName: "Child", parentId: "Main", sessionFile: files.Child, status: "parked", session: null });
		AgentLifecycleManager.global().setPersistedSubagentReviverFactory(async ref => ref.id === "Child" ? async () => childSession as never : undefined, () => 0);
		expect(JSON.stringify((await read.execute("parked-history", { path: "history://Child" }, signal)).content)).toContain("Child transcript fixture");
		const resumed = await write.execute("resume", { path: "agent://Child", content: "Continue checking tests" }, signal);
		expect(resumed.isError).not.toBe(true);
		expect(JSON.stringify(resumed.content)).toContain("revived");
		expect(delivered).toHaveLength(2);
		await expect(read.execute("foreign", { path: "history://Other" }, signal)).rejects.toThrow("outside the current session tree");
		await expect(write.execute("broadcast", { path: "agent://all", content: "hello" }, signal)).rejects.toThrow("broadcasts");
		await writeFile(join(dir, "ordinary.txt"), "ordinary read");
		expect(JSON.stringify((await read.execute("file", { path: join(dir, "ordinary.txt") }, signal)).content)).toContain("ordinary read");
	} finally {
		for (const ref of registry.list()) if (ref.sessionFile && Object.values(files).includes(ref.sessionFile)) registry.unregister(ref.id);
		await session.dispose();
	}
});
