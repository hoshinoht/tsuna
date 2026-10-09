import { expect, test } from "bun:test";
import { resolve } from "node:path";
import { loadExtensions } from "@oh-my-pi/pi-coding-agent";
import { backgroundJobViolation } from "../lib/background-jobs.ts";

test("rejects the stale sentinel waiter even with a tool timeout", () => {
	for (const command of [
		"while [ ! -f /private/tmp/mr2-e2e.exit ]; do sleep 20; done",
		"until test -e \"$exit_file\"; do sleep 1; done",
		"while ! test -f run.exit; do sleep 20; done",
		"while [[ ! -e run.exit ]]; do\n sleep 20\ndone",
	]) {
		expect(backgroundJobViolation({ command, timeout: 300 })).toContain("File-polling");
	}
});

test("ordinary foreground and async jobs cannot disable their deadline", () => {
	for (const async of [false, true]) {
		expect(backgroundJobViolation({ command: "bun run e2e", timeout: 0, async })).toContain("finite positive timeout");
	}
	expect(backgroundJobViolation({ command: "bun run e2e", timeout: 0, name: "  " })).toBeDefined();
});

test("allows managed runs, single recovery probes and named services", () => {
	for (const input of [
		{ command: "bun run e2e", async: true, timeout: 900 },
		{ command: "bun test" },
		{ command: "test -f run.exit", timeout: 10 },
		{ command: "kill -0 1234", timeout: 10 },
		{ command: "tail -n 20 run.log", timeout: 10 },
		{ command: "bun run dev", name: "dev", ready: { port: 3000 } },
	]) expect(backgroundJobViolation(input)).toBeUndefined();
});

test("the real runtime extension blocks the reported waiter and unlimited jobs", async () => {
	const loaded = await loadExtensions([resolve(import.meta.dir, "../extensions/runtime.ts")], import.meta.dir);
	expect(loaded.errors).toEqual([]);
	const handler = loaded.extensions[0]!.handlers.get("tool_call")![0]!;
	const invoke = (toolName: string, input: Record<string, unknown>) => handler(
		{ type: "tool_call", toolCallId: "recovered-run", toolName, input } as Parameters<typeof handler>[0],
		{} as Parameters<typeof handler>[1],
	);
	expect(await invoke("bash", { command: "while [ ! -f run.exit ]; do sleep 20; done", timeout: 300 })).toMatchObject({ block: true });
	expect(await invoke("bash", { command: "bun run e2e", async: true, timeout: 0 })).toMatchObject({ block: true });
	expect(await invoke("bash", { command: "bun run e2e", async: true, timeout: 900 })).toBeUndefined();
	expect(await invoke("read", { path: "run.log" })).toBeUndefined();
});
