import { expect, test } from "bun:test";
import type { ExtensionAPI, ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import permissions from "../extensions/permissions";
import type { AutoReviewEvidence, AutoReviewResult } from "../lib/auto-review";

const allow: AutoReviewResult = { status: "reviewed", model: "test", verdict: "allow", reason: "Routine task-local work", authorizationIds: [], usage: {} as never };
const deny: AutoReviewResult = { ...allow, verdict: "deny", reason: "Unknown deletion target" };
const unavailable: AutoReviewResult = { status: "unavailable", model: "test", reason: "Timed out" };

function harness(result: AutoReviewResult = allow) {
	const handlers = new Map<string, (event: any, ctx: ExtensionContext) => Promise<any>>();
	const commands = new Map<string, { handler: (args: string, ctx: any) => Promise<void> }>();
	const reviews: AutoReviewEvidence[] = [];
	const entries: Array<{ type: string; data: any }> = [];
	const prompts: string[] = [];
	const messages: unknown[] = [];
	let aborted = 0;
	let humanAllows = false;
	let currentResult = result;
	let duringReview: (() => void) | undefined;
	const evidence: AutoReviewEvidence = { action: { toolName: "bash", input: {} }, cwd: "/private/tmp/tsuna-auto-tests", role: "orchestrator", humanMessages: [{ id: "human-1", text: "Fix and test the project" }], toolCalls: [], delegatedMessages: [], contextComplete: true };
	const ctx = { cwd: evidence.cwd, agent: { id: "Main", kind: "main", name: "main" }, hasUI: true,
		sessionManager: { getSessionId: () => "auto-tests" },
		ui: { notify: () => {}, select: async (_title: string, options: string[]) => options[0], confirm: async (_title: string, body: string) => { prompts.push(body); return humanAllows; } },
		abort: () => { aborted++; },
	} as unknown as ExtensionContext;
	permissions({ on: (name: string, fn: any) => handlers.set(name, fn), registerCommand: (name: string, command: any) => commands.set(name, command),
		appendEntry: (type: string, data: any) => entries.push({ type, data }), sendMessage: (message: unknown) => messages.push(message),
	} as unknown as ExtensionAPI, {
		initialMode: "auto",
		evidence: async call => structuredClone({ ...evidence, action: call }),
		review: async value => { reviews.push(structuredClone(value)); duringReview?.(); return currentResult; },
	});
	const call = (toolName: string, input: Record<string, unknown>, context = ctx) => handlers.get("tool_call")!({ toolCallId: `call-${reviews.length}`, toolName, input }, context);
	return { ctx, call, reviews, entries, prompts, evidence, messages, commands, handlers,
		get aborted() { return aborted; }, set humanAllows(value: boolean) { humanAllows = value; }, set result(value: AutoReviewResult) { currentResult = value; },
		set duringReview(value: () => void) { duringReview = value; },
	};
}

test("auto mode quietly reviews executable work and bypasses ordinary file reads", async () => {
	const h = harness();
	expect(await h.call("read", { path: "src/main.ts" })).toBeUndefined();
	expect(h.reviews).toHaveLength(0);
	expect(await h.call("bash", { command: "bun test" })).toBeUndefined();
	expect(h.reviews).toHaveLength(1);
	expect(h.prompts).toHaveLength(0);
	expect(h.entries.at(-1)).toMatchObject({ type: "tsuna-auto-review-decision", data: { verdict: "allow", model: "test" } });
	expect(JSON.stringify(h.entries)).not.toContain("bun test");
});

test("hard denials and explicit asks never reach the model", async () => {
	const h = harness();
	expect(await h.call("checkpoint", {})).toMatchObject({ block: true });
	expect(await h.call("bash", { command: "git push origin main" })).toMatchObject({ block: true, reason: "Permission was not approved" });
	expect(h.prompts).toHaveLength(1);
	expect(h.reviews).toHaveLength(0);
});

test("denial returns a recovery instruction without interrupting the user, then circuit breaks", async () => {
	const h = harness(deny);
	for (let i = 0; i < 2; i++) expect((await h.call("bash", { command: `rm /tmp/unknown-${i}` })).reason).toContain("materially safer");
	expect(h.prompts).toHaveLength(0);
	expect((await h.call("bash", { command: "rm /tmp/unknown-3" })).reason).toContain("circuit breaker");
	expect(h.aborted).toBe(1);
	expect(await h.call("read", { path: "src/main.ts" })).toMatchObject({ block: true });
	expect(h.reviews).toHaveLength(3);
	await h.handlers.get("agent_start")!({}, h.ctx);
	h.result = allow;
	expect(await h.call("bash", { command: "bun test" })).toBeUndefined();
});

test("review outage is separate from denial and requires human permission", async () => {
	const h = harness(unavailable);
	expect(await h.call("bash", { command: "bun test" })).toMatchObject({ block: true });
	expect(h.prompts[0]).toContain("not an unsafe verdict");
	h.humanAllows = true;
	expect(await h.call("bash", { command: "bun test" })).toBeUndefined();
	const child = { ...h.ctx, hasUI: false, agent: { id: "child", kind: "sub", name: "code-writer" } } as ExtensionContext;
	expect((await h.call("bash", { command: "bun test" }, child)).reason).toContain("Return the exact action");
});

test("headless children receive review without granting themselves human consent", async () => {
	const h = harness();
	const child = { ...h.ctx, hasUI: false, agent: { id: "child", kind: "sub", name: "code-writer" } } as ExtensionContext;
	expect(await h.call("bash", { command: "bun test" }, child)).toBeUndefined();
	expect(h.reviews).toHaveLength(1);
	expect(h.prompts).toHaveLength(0);
});

test("changed authorization or arguments invalidates an in-flight allow", async () => {
	const h = harness();
	h.duringReview = () => { h.evidence.humanMessages.push({ id: "human-2", text: "Stop making changes" }); };
	expect((await h.call("bash", { command: "bun test" })).reason).toContain("context changed");
	const changed = harness();
	const input = { command: "bun test" };
	changed.duringReview = () => { input.command = "git push origin main"; };
	expect((await changed.call("bash", input)).reason).toContain("context changed");
});

test("a human override is exact, single-use, and still reviewed", async () => {
	const h = harness(deny);
	await h.call("bash", { command: "rm /tmp/unknown" });
	h.humanAllows = true;
	await h.commands.get("tsuna-approve")!.handler("", h.ctx);
	expect(h.messages).toHaveLength(1);
	h.result = allow;
	await h.call("bash", { command: "rm /tmp/different" });
	expect(h.reviews.at(-1)?.override).toBeUndefined();
	await h.call("bash", { command: "rm /tmp/unknown" });
	expect(h.reviews.at(-1)?.override?.actionHash).toBeTruthy();
	await h.call("bash", { command: "rm /tmp/unknown" });
	expect(h.reviews.at(-1)?.override).toBeUndefined();
});

test("source mode preserves manual review and changes are session-scoped", async () => {
	const h = harness();
	await h.commands.get("tsuna-permissions")!.handler("source", h.ctx);
	expect(await h.call("bash", { command: "python -c 'print(1)'" })).toMatchObject({ block: true });
	expect(h.reviews).toHaveLength(0);
	const other = { ...h.ctx, sessionManager: { getSessionId: () => "other" } } as ExtensionContext;
	expect(await h.call("bash", { command: "python -c 'print(1)'" }, other)).toBeUndefined();
	expect(h.reviews).toHaveLength(1);
});

test("concurrent reviews share the denial circuit and cancelled reviews cannot execute", async () => {
	const h = harness(deny);
	const results = await Promise.all(Array.from({ length: 6 }, (_, i) => h.call("bash", { command: `rm /tmp/unknown-${i}` })));
	expect(results.every(result => result?.block)).toBe(true);
	expect(h.reviews).toHaveLength(3);
	expect(h.aborted).toBe(1);
	const cancelled = harness();
	cancelled.duringReview = () => { void cancelled.handlers.get("agent_end")!({}, cancelled.ctx); };
	expect(await cancelled.call("bash", { command: "bun test" })).toMatchObject({ block: true, reason: "Automatic review cancelled" });
	expect(cancelled.entries).toHaveLength(0);
});

test("new human input invalidates a granted retry", async () => {
	const h = harness(deny);
	await h.call("bash", { command: "rm /tmp/unknown" });
	h.humanAllows = true;
	await h.commands.get("tsuna-approve")!.handler("", h.ctx);
	h.evidence.humanMessages.push({ id: "human-2", text: "Do not delete it" });
	await h.call("bash", { command: "rm /tmp/unknown" });
	expect(h.reviews.at(-1)?.override).toBeUndefined();
});

test("off mode auto-approves requests after an aborted run, without review or prompts", async () => {
	const h = harness(deny);
	for (let i = 0; i < 3; i++) await h.call("bash", { command: `rm /tmp/unknown-${i}` });
	await h.commands.get("tsuna-permissions")!.handler("off", h.ctx);
	const before = h.reviews.length;
	for (const command of [
		"cargo test --all-features --test extract redirect > /private/tmp/t-redirect.log 2>&1; echo exit=$?; grep -E '^test result|FAILED|panicked' /private/tmp/t-redirect.log | head",
		"git push origin main", "rm -rf dist",
	]) expect(await h.call("bash", { command })).toBeUndefined();
	expect(await h.call("edit", { path: "src/extract/AGENTS.md", input: "project documentation" })).toBeUndefined();
	expect(await h.call("eval", { language: "js", code: "1 + 1" })).toBeUndefined();
	expect(h.reviews.length).toBe(before);
	expect(h.prompts).toHaveLength(0);
	expect(await h.call("checkpoint", {})).toMatchObject({ block: true });
	await h.commands.get("tsuna-permissions")!.handler("auto", h.ctx);
	h.result = allow;
	await h.call("bash", { command: "bun test" });
	expect(h.reviews.length).toBe(before + 1);
});

test("repeated reviewer failures do not abort an otherwise manually approved run", async () => {
	const h = harness(unavailable);
	h.humanAllows = true;
	for (let i = 0; i < 5; i++) expect(await h.call("bash", { command: "bun test" })).toBeUndefined();
	expect(h.aborted).toBe(0);
	expect(h.prompts).toHaveLength(5);
});
