import { describe, expect, test } from "bun:test";
import { resolve } from "node:path";
import { loadExtensions } from "@oh-my-pi/pi-coding-agent";
import {
	admitCacheRisk,
	cacheRisk,
	canonicalProvider,
	pruneContextImages,
	routeReasoning,
	validateRuntimeOptions,
	type CacheGuardState,
	type ImagePruneState,
	type ReasoningState,
} from "../lib/runtime.ts";

const image = (seed: string, bytes = 100) => ({ type: "image", data: (seed + "x".repeat(bytes)).slice(0, bytes), mimeType: "image/png" });
const context = (...parts: unknown[]) => parts.map((part, index) => ({ role: "user", timestamp: index + 1, content: [part] }));

describe("image budget", () => {
	test("keeps the newest image under a count cap and preserves decisions on the next request", () => {
		const state: ImagePruneState = { sessions: new Map() };
		const options = validateRuntimeOptions({ imageBudget: { maxImages: 2, maxImageBytes: 10_000, pruneTo: 0.5 } }).imageBudget;
		const first = pruneContextImages(state, "s", context(image("a"), image("b"), image("c")), options, "cliproxy-openai");
		expect(first.changed).toBe(true);
		expect(JSON.stringify(first.messages)).not.toContain('"data":"a');
		expect(JSON.stringify(first.messages)).toContain('"data":"c');
		const second = pruneContextImages(state, "s", [...context(image("a"), image("b"), image("c")), { role: "user", timestamp: 4, content: [image("d")] }], options, "openai");
		expect(second.changed).toBe(false);
		expect(JSON.stringify(second.messages)).toContain('"data":"d');
	});

	test("deduplicates an older copy when pruning becomes necessary", () => {
		const state: ImagePruneState = { sessions: new Map() };
		const options = validateRuntimeOptions({ imageBudget: { maxImages: 2, maxImageBytes: 10_000, pruneTo: 1 } }).imageBudget;
		const result = pruneContextImages(state, "s", context(image("same"), image("middle"), image("same")), options);
		expect(JSON.stringify(result.messages)).toContain("same image appears later");
	});
});

describe("cache guard", () => {
	test("warns at 30 minutes with 10,000 cache-read tokens, once per response", () => {
		const options = validateRuntimeOptions({}).cacheGuard;
		const message = { role: "assistant", provider: "cliproxy-openai", model: "gpt", completedAt: 0, usage: { cacheRead: 10_000 } };
		const risk = cacheRisk([message], options, { provider: "openai", id: "gpt" }, 1_800_000)!;
		expect(risk.idleMinutes).toBe(30);
		const state: CacheGuardState = { warned: new Set(), diagnostics: [] };
		expect(admitCacheRisk(state, "session", risk, 20)).toBeDefined();
		expect(admitCacheRisk(state, "session", risk, 20)).toBeUndefined();
	});

	test("does not use a cache hit after the active model changes", () => {
		const options = validateRuntimeOptions({}).cacheGuard;
		const messages = [{ role: "assistant", provider: "openai", model: "gpt-a", completedAt: 0, usage: { cacheRead: 10_000 } }];
		expect(cacheRisk(messages, options, { provider: "openai", id: "gpt-b" }, 1_800_000)).toBeUndefined();
	});
});

describe("reasoning router", () => {
	test("normalizes gateway providers and clamps a child marker to its configured range", () => {
		const options = validateRuntimeOptions({ reasoningRouter: { providers: { anthropic: { efforts: ["low", "medium", "high"] } }, providerAgentPolicy: { anthropic: { tester: { def: "low", min: "low", max: "medium" } } } } }).reasoningRouter;
		const state: ReasoningState = { sessions: new Map(), diagnostics: [] };
		const output = routeReasoning(state, "s", "tester", { provider: "cliproxy-anthropic", id: "claude", thinking: { efforts: ["low", "medium", "high"] } }, [{ role: "user", content: "[reasoning:deep] run tests" }], options);
		expect(output.decision?.effort).toBe("medium");
		expect(JSON.stringify(output.messages)).not.toContain("reasoning:deep");
		expect(canonicalProvider("cliproxy-openai")).toBe("openai");
	});

	test("uses HOSHI's primary identity only when supplied by the extension", () => {
		const options = validateRuntimeOptions({}).reasoningRouter;
		const state: ReasoningState = { sessions: new Map(), diagnostics: [] };
		const output = routeReasoning(state, "s", "explore", { provider: "openai", id: "gpt", thinking: { efforts: ["low", "medium", "high"] } }, [{ role: "user", content: "[reasoning:fast] find callers" }], options);
		expect(output.decision?.effort).toBe("low");
	});
});

test("loads the runtime extension with imported plugin options", async () => {
	const loaded = await loadExtensions([resolve(import.meta.dir, "../extensions/runtime.ts")], import.meta.dir);
	expect(loaded.errors).toEqual([]);
	expect([...loaded.extensions[0]!.tools.keys()].sort()).toEqual(["cache_guard_status", "reasoning_router_status"]);
});
