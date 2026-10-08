import { expect, test } from "bun:test";
import type { AssistantMessage, completeSimple } from "@oh-my-pi/pi-ai";
import { Effort } from "@oh-my-pi/pi-catalog";
import type { ExtensionAPI, ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import permissions from "../extensions/permissions";
import { reviewPermission, redactReviewText, formatPermissionReview, shouldAutoApprovePermission } from "../lib/permission-review";
import { asToolCall, decidePermission, type PermissionPolicies } from "../lib/permissions";
import policies from "../config/permissions.json";

const config = { enabled: true, model: "cliproxy-openai/gpt-6-luna", timeoutMs: 1000, maxTokens: 1024 };
function context() {
	return { cwd: "/tmp/project", agent: { id: "Main", name: "main", kind: "main" }, hasUI: true,
		sessionManager: { getSessionId: () => "review-session", getBranch: () => [{ type: "message", message: { role: "user", content: "Check the git status" } }] },
		modelRegistry: { find: (provider: string, id: string) => ({ provider, id, api: "openai-responses" }), getApiKey: async () => "test-key" },
	} as unknown as ExtensionContext;
}
function response(text: string, stopReason = "stop"): AssistantMessage {
	return { content: [{ type: "text", text }], stopReason, usage: { input: 1, output: 1, totalTokens: 2 } } as AssistantMessage;
}
const answer = JSON.stringify({ risk: "low", scope: "within-request", reason: "Reads repository status without modifying files." });

test("Luna receives bounded redacted evidence and no execution tools", async () => {
	let request: unknown;
	const complete = (async (model, input, options) => {
		expect(model.provider).toBe("cliproxy-openai");
		expect(model.id).toBe("gpt-6-luna");
		expect(input.tools).toEqual([]);
		expect(options?.reasoning).toBe(Effort.Low);
		expect(options?.maxTokens).toBe(1024);
		request = input;
		return response(answer);
	}) as typeof completeSimple;
	const result = await reviewPermission({ toolName: "bash", input: { command: "API_KEY='secret-value' git status", authorization: "credential-value" } }, "orchestrator", "Needs review", context(), config, complete);
	expect(result?.status).toBe("reviewed");
	expect(JSON.stringify(request)).toContain("Check the git status");
	expect(JSON.stringify(request)).not.toContain("secret-value");
	expect(JSON.stringify(request)).not.toContain("credential-value");
	expect(formatPermissionReview(result)).toContain("Human approval is still required");
});

test("malformed, truncated and failed reviews cannot grant approval or expose provider errors", async () => {
	for (const complete of [async () => response("not JSON"), async () => response(answer, "length"), async () => { throw new Error("sk-secret-provider-error-value"); }]) {
		const result = await reviewPermission({ toolName: "bash", input: { command: "rm -rf /tmp/example" } }, "orchestrator", undefined, context(), config, complete as typeof completeSimple);
		expect(result?.status).toBe("unavailable");
		expect(formatPermissionReview(result)).toContain("manual approval");
		expect(JSON.stringify(result)).not.toContain("sk-secret");
	}
});

test("missing models, disabled reviews and oversized evidence do not make a model request", async () => {
	let calls = 0;
	const complete = (async () => { calls++; return response(answer); }) as typeof completeSimple;
	const ctx = context();
	ctx.modelRegistry.find = () => undefined;
	expect((await reviewPermission({ toolName: "bash", input: {} }, "build", undefined, ctx, config, complete))?.status).toBe("unavailable");
	expect(await reviewPermission({ toolName: "bash", input: {} }, "build", undefined, context(), { ...config, enabled: false }, complete)).toBeUndefined();
	expect((await reviewPermission({ toolName: "bash", input: { command: "x".repeat(13000) } }, "build", undefined, context(), config, complete))?.status).toBe("unavailable");
	expect(calls).toBe(0);
});

test("timeouts abort the reviewer and leave a manual approval path", async () => {
	let aborted = false;
	const complete = (async (_model, _input, options) => new Promise((_resolve, reject) => {
		options?.signal?.addEventListener("abort", () => { aborted = true; reject(new Error("aborted")); }, { once: true });
	})) as typeof completeSimple;
	const result = await reviewPermission({ toolName: "bash", input: {} }, "build", undefined, context(), { ...config, timeoutMs: 10 }, complete);
	expect(aborted).toBe(true);
	expect(result?.status).toBe("unavailable");
});

test("prompt reviews preserve hard denials, headless restrictions and the human decision", async () => {
	const handlers = new Map<string, (event: never, ctx: ExtensionContext) => Promise<unknown>>();
	const entries: unknown[] = [];
	permissions({ on: (name: string, handler: unknown) => handlers.set(name, handler as never), registerCommand: () => {}, appendEntry: (_name: string, value: unknown) => entries.push(value) } as unknown as ExtensionAPI);
	const ctx = context();
	let finds = 0;
	ctx.modelRegistry.find = () => { finds++; return undefined; };
	let prompt = "";
	ctx.ui = { confirm: async (_title: string, body: string) => { prompt = body; return false; }, notify: () => {} } as never;
	const call = handlers.get("tool_call")!;
	expect(await call({ toolName: "eval", input: { code: "1" }, toolCallId: "deny" } as never, ctx)).toMatchObject({ block: true });
	expect(finds).toBe(0);
	const event = { toolName: "bash", input: { command: "echo checked; rm -rf /tmp/example" }, toolCallId: "ask" } as never;
	expect(await call(event, { ...ctx, hasUI: false })).toMatchObject({ block: true });
	expect(finds).toBe(0);
	expect(await call(event, ctx)).toMatchObject({ block: true, reason: "Permission was not approved" });
	expect(prompt).toContain("GPT-6-Luna");
	expect(prompt).toContain("manual approval");
	expect(entries).toHaveLength(2);
	expect(JSON.stringify(entries)).not.toContain("rm -rf");
});

test("only aligned low-risk inspection can waive a generic permission ask", async () => {
	const ctx = context();
	const review = await reviewPermission({ toolName: "bash", input: {} }, "build", undefined, ctx, config, (async () => response(answer)) as typeof completeSimple);
	const auto = { ...config, mode: "auto" };
	const call = asToolCall("bash", { command: "git status --short | head -20" });
	const decision = decidePermission(policies as PermissionPolicies, "orchestrator", call, { approvalMode: "source" });
	expect(decision.effect).toBe("ask");
	expect(shouldAutoApprovePermission(call, decision, review, auto)).toBe(true);
	for (const command of ["rm -rf /tmp/example", "git push origin main", "git reset --hard HEAD", "git update-ref refs/heads/main HEAD", "git worktree remove --force feature", "python3 -c print(1)", "rg --pre script TODO src", "cat .env | head", "git status; echo safe"]) {
		const proposed = asToolCall("bash", { command });
		expect(shouldAutoApprovePermission(proposed, { ...decision, effect: "ask" }, review, auto)).toBe(false);
	}
	expect(shouldAutoApprovePermission(call, { ...decision, effect: "deny" }, review, auto)).toBe(false);
	expect(shouldAutoApprovePermission(call, decision, review, { ...auto, mode: "advisory" })).toBe(false);
	if (review?.status === "reviewed") {
		expect(shouldAutoApprovePermission(call, decision, { ...review, risk: "high" }, auto)).toBe(false);
		expect(shouldAutoApprovePermission(call, decision, { ...review, scope: "unknown" }, auto)).toBe(false);
	}
});

test("common credential formats are removed without changing ordinary commands", () => {
	expect(redactReviewText("git status --short")).toBe("git status --short");
	for (const input of ["Bearer secret-bearer-value", "--password secret-password", "TOKEN=secret-token", "sk-12345678901234567890"]) {
		expect(redactReviewText(input)).toContain("[REDACTED]");
	}
});
