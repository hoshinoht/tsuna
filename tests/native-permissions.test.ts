import { expect, test } from "bun:test";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createAgentSession, discoverAuthStorage, ModelRegistry, SessionManager, Settings, type ExtensionAPI, type ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { getBundledModel } from "@oh-my-pi/pi-catalog";
import { initializeExtensions } from "@oh-my-pi/pi-coding-agent/modes/runtime-init";
import policies from "../config/permissions.json";
import permissions from "../extensions/permissions";
import { asToolCall, canSurfaceTool, decidePermission, type PermissionPolicies } from "../lib/permissions";
import { routeAutoPermission } from "../lib/auto-permissions";

const rules = policies as PermissionPolicies;
const modes = ["auto", "source"] as const;
const operations = [
	asToolCall("lsp", { action: "rename", file: "src/a.ts", new_name: "newSymbol" }),
	asToolCall("lsp", { action: "rename_file", file: "src/a.ts", new_name: "src/b.ts" }),
	asToolCall("lsp", { action: "code_actions", file: "src/a.ts", query: "Fix imports", apply: true }),
	asToolCall("lsp", { action: "request", file: "src/a.ts", query: "workspace/executeCommand" }),
	asToolCall("lsp", { action: "reload", file: "*" }),
	asToolCall("debug", { action: "launch", program: "./app", args: ["--flag"] }),
	asToolCall("debug", { action: "evaluate", expression: "changeState()" }),
	asToolCall("debug", { action: "write_memory", data: "AA==" }),
	asToolCall("debug", { action: "custom_request", command: "unknown" }),
	asToolCall("eval", { language: "js", code: "await Bun.write(\"result.txt\", \"changed\")" }),
	asToolCall("eval", { language: "py", code: "print(2 + 2)" }),
];

test("default native tools are discoverable while optional unmapped tools remain denied", () => {
	for (const tool of ["lsp", "debug", "eval"]) expect(canSurfaceTool(rules, "orchestrator", tool)).toBe(true);
	for (const tool of ["lsp", "debug"]) expect(canSurfaceTool(rules, "code-checker", tool)).toBe(true);
	expect(canSurfaceTool(rules, "code-checker", "eval")).toBe(false);
	for (const tool of ["ida", "github", "security_scan", "checkpoint", "rewind", "context_notes", "think", "memory_edit", "retain", "recall", "reflect", "learn"]) {
		expect(canSurfaceTool(rules, "orchestrator", tool)).toBe(false);
		expect(decidePermission(rules, "orchestrator", asToolCall(tool, {}), { approvalMode: "auto" }).effect).toBe("deny");
	}
});

test("LSP navigation and previews and debugger inspection follow read permissions", () => {
	for (const approvalMode of modes) {
		for (const action of ["diagnostics", "definition", "type_definition", "implementation", "references", "hover", "symbols", "status", "capabilities"]) {
			expect(decidePermission(rules, "code-checker", asToolCall("lsp", { action, file: "src/a.ts" }), { approvalMode }).effect).toBe("allow");
		}
		for (const input of [{ action: "rename", file: "src/a.ts", apply: false }, { action: "rename_file", file: "src/a.ts", new_name: "src/b.ts", apply: false }, { action: "code_actions", file: "src/a.ts" }]) {
			expect(decidePermission(rules, "code-checker", asToolCall("lsp", input), { approvalMode }).effect).toBe("allow");
		}
		for (const action of ["output", "threads", "stack_trace", "scopes", "variables", "disassemble", "read_memory", "loaded_sources", "modules", "sessions"]) {
			expect(decidePermission(rules, "code-checker", asToolCall("debug", { action }), { approvalMode }).effect).toBe("allow");
		}
	}
});

test("native operations preserve file restrictions and both rename paths", () => {
	const restricted: PermissionPolicies = { worker: [
		{ action: "*", resource: "*", effect: "deny" },
		{ action: "read", resource: "*", effect: "allow" },
		{ action: "edit", resource: "*", effect: "allow" },
		{ action: "external_directory", resource: "*", effect: "deny" },
		{ action: "read", resource: "*.env", effect: "deny" },
		{ action: "edit", resource: "*/blocked.ts", effect: "deny" },
	] };
	const options = { cwd: process.cwd(), approvalMode: "auto" as const };
	for (const call of [
		asToolCall("lsp", { action: "hover", file: ".env" }),
		asToolCall("lsp", { action: "definition", file: "/private/tmp/outside-tsuna-native/file.ts" }),
		asToolCall("debug", { action: "sessions", file: ".env" }),
		asToolCall("debug", { action: "variables", file: "src/a.ts", program: "/private/tmp/outside-tsuna-native/app" }),
		asToolCall("lsp", { action: "rename_file", file: "src/a.ts", new_name: "/private/tmp/outside-tsuna-native/file.ts" }),
		asToolCall("lsp", { action: "rename_file", file: "src/a.ts", new_name: "src/blocked.ts" }),
	]) expect(decidePermission(restricted, "worker", call, options).effect).toBe("deny");
	expect(decidePermission(rules, "orchestrator", asToolCall("lsp", { action: "unknown", file: "src/a.ts" }), options).effect).toBe("deny");
});

test("execution and mutations require approval in both modes and retain role denials", () => {
	for (const approvalMode of modes) {
		for (const call of operations) {
			const decision = decidePermission(rules, "orchestrator", call, { approvalMode });
			expect(decision.effect).toBe("ask");
			expect(decision.approvalReason).toBeTruthy();
			expect(decidePermission(rules, "code-checker", call, { approvalMode }).effect).toBe("deny");
		}
	}
	const denied: PermissionPolicies = { orchestrator: [...rules.orchestrator, { action: "eval", resource: "*", effect: "deny" }] };
	expect(decidePermission(denied, "orchestrator", operations.at(-1)!, { approvalMode: "auto" }).effect).toBe("deny");
});

test("native operations reach contextual review while restricted roles stay denied", () => {
	for (const call of operations) {
		const decision = decidePermission(rules, "orchestrator", call, { approvalMode: "source" });
		expect(routeAutoPermission(call, decision, process.cwd())).toBe("review");
		const restricted = decidePermission(rules, "code-checker", call, { approvalMode: "source" });
		expect(routeAutoPermission(call, restricted, process.cwd())).toBe("deny");
	}
});

test("source mode shows complete operations and enforces human and headless decisions", async () => {
	const originalRole = process.env.TSUNA_AGENT;
	const originalMode = process.env.TSUNA_PERMISSION_MODE;
	process.env.TSUNA_AGENT = "orchestrator";
	process.env.TSUNA_PERMISSION_MODE = "source";
	const handlers = new Map<string, (event: never, ctx: ExtensionContext) => Promise<unknown>>();
	permissions({ on: (name: string, handler: unknown) => handlers.set(name, handler as never), registerCommand: () => {}, appendEntry: () => {} } as unknown as ExtensionAPI);
	let accepted = false;
	let prompt = "";
	let prompts = 0;
	const ctx = { cwd: process.cwd(), agent: { id: "Main", name: "main", kind: "main" }, hasUI: true,
		sessionManager: { getSessionId: () => "native-test" }, modelRegistry: { find: () => undefined },
		ui: { notify: () => {}, confirm: async (_title: string, body: string) => { prompts++; prompt = body; return accepted; } },
	} as unknown as ExtensionContext;
	try {
		const handler = handlers.get("tool_call")!;
		for (const [index, call] of operations.entries()) {
			const event = { ...call, toolCallId: `native-${index}` } as never;
			const before = prompts;
			expect(await handler(event, { ...ctx, hasUI: false })).toMatchObject({ block: true });
			expect(prompts).toBe(before);
			accepted = false;
			expect(await handler(event, ctx)).toMatchObject({ block: true, reason: "Permission was not approved" });
			expect(prompt).toContain(JSON.stringify(call.input, null, 2));
			accepted = true;
			expect(await handler(event, ctx)).toBeUndefined();
		}
	} finally {
		if (originalRole === undefined) delete process.env.TSUNA_AGENT; else process.env.TSUNA_AGENT = originalRole;
		if (originalMode === undefined) delete process.env.TSUNA_PERMISSION_MODE; else process.env.TSUNA_PERMISSION_MODE = originalMode;
	}
});

test("native OMP wrappers permit LSP/debug inspection and block headless Eval before execution", async () => {
	const originalMode = process.env.TSUNA_PERMISSION_MODE;
	const dir = await mkdtemp(join(tmpdir(), "tsuna-native-permissions-"));
	let session: Awaited<ReturnType<typeof createAgentSession>>["session"] | undefined;
	try {
		process.env.TSUNA_PERMISSION_MODE = "auto";
		const agentDir = join(dir, "profile");
		await mkdir(agentDir);
		await writeFile(join(agentDir, "config.yml"), `tools:\n  approvalMode: yolo\nmemory:\n  backend: off\nextensions:\n  - ${resolve(import.meta.dir, "../extensions/permissions.ts")}\n`);
		const settings = await Settings.loadIsolated({ cwd: dir, agentDir });
		const authStorage = await discoverAuthStorage(agentDir);
		const modelRegistry = new ModelRegistry(authStorage, join(agentDir, "models.yml"), { settings });
		({ session } = await createAgentSession({ cwd: dir, agentDir, settings, authStorage, modelRegistry,
			model: getBundledModel("anthropic", "claude-opus-5-5"), sessionManager: SessionManager.inMemory(dir), enableMCP: false, cacheWarming: false, bindProcessState: false }));
		await initializeExtensions(session, { reportSendError: (_action, error) => { throw error; }, reportRuntimeError: error => { throw new Error(JSON.stringify(error)); } });
		await session.setActiveToolsByName(["lsp", "debug", "eval"]);
		const tools = session.agent.state.tools;
		const signal = new AbortController().signal;
		for (const [name, action] of [["lsp", "status"], ["debug", "sessions"]]) {
			const result = await tools.find(tool => tool.name === name)!.execute(`native-${name}`, { action }, signal);
			expect(result.isError).not.toBe(true);
		}
		await expect(tools.find(tool => tool.name === "eval")!.execute("native-eval", { language: "js", code: "throw new Error(\"must not execute\")" }, signal))
			.rejects.toThrow("This permission requires an interactive primary session");
	} finally {
		if (originalMode === undefined) delete process.env.TSUNA_PERMISSION_MODE; else process.env.TSUNA_PERMISSION_MODE = originalMode;
		await session?.dispose();
		await rm(dir, { recursive: true, force: true });
	}
});
