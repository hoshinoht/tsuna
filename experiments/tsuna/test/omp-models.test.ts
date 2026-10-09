/**
 * OMP model layer behind Tsuna's `omp` provider type: real OMP wire clients
 * (Anthropic Messages, OpenAI Chat Completions) against a local fixture LLM.
 */
import { afterEach, expect, test } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { startLlmFixture, type FixtureLlmReply, type FixtureLlmRequest } from "../fixtures/models/llm-server.ts";
import { toOmpContext, toPiAssistant } from "../src/backend/omp-models.ts";
import type { TsunaConfig } from "../src/config.ts";
import { startRig, tempDir, testConfig, type TestRig } from "./helpers/harness.ts";
import { ORCH_RULES, ORCH_TOOLS, writePack } from "./helpers/pack.ts";

const rigs: TestRig[] = [];
const stops: (() => void)[] = [];
afterEach(async () => {
	for (const r of rigs.splice(0)) await r.harness.shutdown();
	for (const s of stops.splice(0)) s();
	delete process.env.TSUNA_TEST_ANTHROPIC_KEY;
	delete process.env.TSUNA_TEST_DEEPSEEK_KEY;
	delete process.env.ANTHROPIC_API_KEY;
});

function fixture(decide: (r: FixtureLlmRequest) => FixtureLlmReply) {
	const fx = startLlmFixture(decide);
	stops.push(fx.stop);
	return fx;
}

function pack(childReasoning: string) {
	return writePack(tempDir("tsuna-omp-pack-"), "omp", [
		{ name: "lead", model: "opus", primary: true, reasoning: { default: "medium" }, tools: [...ORCH_TOOLS, "read"], spawns: ["scout"], permissions: ORCH_RULES },
		{ name: "scout", model: "deep", reasoning: { default: childReasoning }, tools: ["read"], output: { schema: { type: "object", required: ["answer"], properties: { answer: { type: "string" } } } } },
	]);
}

function ompConfig(url: string, overrides: Partial<TsunaConfig["providers"]> = {}, childReasoning = "high"): TsunaConfig {
	return testConfig({
		agentPacks: [pack(childReasoning)],
		primaryRole: "lead",
		providers: {
			providers: {
				anthropic: { type: "omp", ompProvider: "anthropic", apiKeyEnv: "TSUNA_TEST_ANTHROPIC_KEY", baseUrl: url },
				deepseek: { type: "omp", ompProvider: "deepseek", apiKeyEnv: "TSUNA_TEST_DEEPSEEK_KEY", baseUrl: url },
			},
			models: {
				opus: { provider: "anthropic", id: "claude-opus-5-5", reasoning: true, contextWindow: 0, maxTokens: 0, input: ["text"] },
				deep: { provider: "deepseek", id: "deepseek-v4-pro", reasoning: true, contextWindow: 0, maxTokens: 0, input: ["text"] },
			},
			...overrides,
		},
	});
}

/** Lead delegates once to scout, then answers; scout yields a structured result. */
function scenario(r: FixtureLlmRequest): FixtureLlmReply {
	if (r.wire === "openai") {
		if (r.lastText.includes("Result recorded")) return { text: "done" };
		return { thinking: "looking", toolCall: { name: "yield", arguments: { data: { answer: "found it" } } } };
	}
	if (r.lastText.includes("tsuna-result")) return { text: "The scout says: found it." };
	return { thinking: "plan: delegate", toolCall: { name: "task", arguments: { agent: "scout", task: "find the thing" } } };
}

test("a session runs on OMP's Anthropic and OpenAI-compatible clients with catalog-validated models", async () => {
	process.env.TSUNA_TEST_ANTHROPIC_KEY = "sk-ant-test";
	process.env.TSUNA_TEST_DEEPSEEK_KEY = "sk-ds-test";
	const fx = fixture(scenario);
	const r = await startRig({ config: ompConfig(fx.url) });
	rigs.push(r);
	const rt = r.harness.runtime;
	// Limits come from OMP's catalog, not from config.
	expect(r.config.providers.models.opus).toMatchObject({ contextWindow: 1_000_000, maxTokens: 128_000, reasoning: true });
	expect(r.config.providers.models.deep!.reasoningLevels).toEqual(["low", "high", "max"]);
	await rt.promptPrimary("investigate");
	const scout = rt.records().find(x => x.role === "scout")!;
	expect(scout.results[0]).toMatchObject({ status: "success", data: { answer: "found it" } });
	expect(rt.read({ kind: "human", rootId: rt.rootId }, rt.primaryId)).toMatchObject({ transcript: expect.arrayContaining([expect.objectContaining({ role: "assistant", text: "The scout says: found it." })]) });
	const anthropic = fx.requests.filter(q => q.wire === "anthropic");
	const openai = fx.requests.filter(q => q.wire === "openai");
	expect(anthropic.length).toBe(2);
	// The scout's terminal yield ends its loop: exactly one request.
	expect(openai.length).toBe(1);
	// Keys only from the configured env vars; effort passed through unchanged after validation.
	expect(anthropic[0]!.headers["x-api-key"]).toBe("sk-ant-test");
	expect(openai[0]!.headers.authorization).toBe("Bearer sk-ds-test");
	expect(JSON.stringify(anthropic[0]!.body.output_config)).toContain("medium");
	expect(openai[0]!.body.reasoning_effort).toBe("high");
	// The second Anthropic request replays OMP-native state: the signed thinking block and the tool result.
	const replay = JSON.stringify(anthropic[1]!.body.messages);
	expect(replay).toContain("fixture-signature");
	expect(replay).toContain("tool_result");
	// The system prompt and Tsuna tools reached the wire.
	expect(JSON.stringify(anthropic[0]!.body.system)).toContain("Tsuna-Agent-Id:");
	expect(anthropic[0]!.tools).toContain("task");
	// Nothing written to OMP's or Pi's home locations.
	const home = process.env.HOME!;
	expect(existsSync(join(home, ".omp"))).toBe(false);
	expect(existsSync(join(home, ".pi"))).toBe(false);
	// Stored transcript keeps Tsuna's provider identity plus the upstream api.
	const stored = readFileSync(rt.record(rt.primaryId)!.sessionFile!, "utf8");
	expect(stored).toContain('"provider":"anthropic"');
	expect(stored).toContain('"upstreamApi":"anthropic-messages"');
});

test("restart: resumed sessions replay OMP-native history through the adapter", async () => {
	process.env.TSUNA_TEST_ANTHROPIC_KEY = "sk-ant-test";
	process.env.TSUNA_TEST_DEEPSEEK_KEY = "sk-ds-test";
	const fx = fixture(scenario);
	const config = ompConfig(fx.url);
	const state = tempDir("tsuna-state-");
	const workspace = tempDir("tsuna-ws-");
	const first = await startRig({ config, state, workspace });
	await first.harness.runtime.promptPrimary("investigate");
	const rootId = first.harness.runtime.rootId;
	await first.harness.shutdown();
	const second = await startRig({ config: ompConfig(fx.url), state, workspace, resume: rootId });
	rigs.push(second);
	const before = fx.requests.length;
	await second.harness.runtime.promptPrimary("anything else?");
	const resumed = fx.requests.slice(before).filter(q => q.wire === "anthropic")[0]!;
	expect(JSON.stringify(resumed.body.messages)).toContain("fixture-signature");
});

test("catalog validation rejects unknown models, inflated limits and unsupported reasoning levels", async () => {
	const fx = fixture(scenario);
	const bad = async (models: Record<string, unknown>, childReasoning = "high") => {
		const config = ompConfig(fx.url, {}, childReasoning);
		Object.assign(config.providers.models, models);
		return startRig({ config });
	};
	await expect(bad({ deep: { provider: "deepseek", id: "no-such-model", reasoning: true, contextWindow: 0, maxTokens: 0, input: ["text"] } })).rejects.toThrow(/no model deepseek\/no-such-model/);
	await expect(bad({ opus: { provider: "anthropic", id: "claude-opus-5-5", reasoning: true, contextWindow: 2_000_000, maxTokens: 0, input: ["text"] } })).rejects.toThrow(/exceeds the catalog/);
	await expect(bad({ deep: { provider: "deepseek", id: "deepseek-v4-pro", reasoning: true, reasoningLevels: ["medium"], contextWindow: 0, maxTokens: 0, input: ["text"] } })).rejects.toThrow(/reasoning level medium is not supported/);
	// A definition whose reasoning preference the model cannot honour is rejected at spawn, not translated.
	process.env.TSUNA_TEST_ANTHROPIC_KEY = "k";
	process.env.TSUNA_TEST_DEEPSEEK_KEY = "k";
	const r = await bad({}, "medium");
	rigs.push(r);
	expect(() => r.harness.runtime.spawn(r.harness.runtime.primaryId, { agent: "scout", task: "x" })).toThrow(/does not support reasoning level medium/);
});

test("a missing key fails before any request; OMP's own env and storage fallbacks are never used", async () => {
	process.env.ANTHROPIC_API_KEY = "sk-from-omp-default-env";
	const fx = fixture(scenario);
	const r = await startRig({ config: ompConfig(fx.url) });
	rigs.push(r);
	await r.harness.runtime.promptPrimary("investigate");
	expect(fx.requests).toHaveLength(0);
	const view = r.harness.runtime.read({ kind: "human", rootId: r.harness.runtime.rootId }, r.harness.runtime.primaryId);
	if (typeof view === "string") throw new Error(view);
	expect(readFileSync(r.harness.runtime.record(r.harness.runtime.primaryId)!.sessionFile!, "utf8")).toContain("missing API key: set TSUNA_TEST_ANTHROPIC_KEY");
});

test("aborting a turn cancels the in-flight OMP request", async () => {
	process.env.TSUNA_TEST_ANTHROPIC_KEY = "k";
	process.env.TSUNA_TEST_DEEPSEEK_KEY = "k";
	const fx = fixture(() => ({ delayMs: 5000, text: "too late" }));
	const r = await startRig({ config: ompConfig(fx.url) });
	rigs.push(r);
	const rt = r.harness.runtime;
	const run = rt.promptPrimary("slow please");
	await Bun.sleep(300);
	const t0 = Date.now();
	await rt.interrupt({ kind: "human", rootId: rt.rootId }, rt.primaryId);
	await run;
	expect(Date.now() - t0).toBeLessThan(2000);
	expect(readFileSync(rt.record(rt.primaryId)!.sessionFile!, "utf8")).not.toContain("too late");
});

test("context conversion maps Tsuna-owned turns back to OMP identity and leaves others alone", () => {
	const system = { role: "system", content: "base", sections: { a: "<a>x</a>" }, tools: [{ name: "read", description: "r", parameters: { type: "object" } }] };
	const mine = { role: "assistant", provider: "anthropic", api: "tsuna-omp-anthropic", model: "claude-opus-5-5", content: [], timestamp: 1 };
	const other = { role: "assistant", provider: "fixture", api: "tsuna-fixture", model: "x", content: [], timestamp: 2 };
	const ctx = toOmpContext([system, { role: "user", content: "hi", timestamp: 0 }, mine, other], { api: "anthropic-messages", provider: "anthropic" }, "anthropic");
	expect(ctx.messages.map(m => m.role)).toEqual(["user", "assistant", "assistant"]);
	expect(ctx.messages[1]).toMatchObject({ api: "anthropic-messages", provider: "anthropic" });
	expect(ctx.messages[2]).toMatchObject({ api: "tsuna-fixture", provider: "fixture" });
	expect(toPiAssistant({ role: "assistant", api: "anthropic-messages", provider: "anthropic" }, { api: "tsuna-omp-anthropic", provider: "anthropic", id: "claude-opus-5-5" })).toMatchObject({ api: "tsuna-omp-anthropic", upstreamApi: "anthropic-messages" });
});
