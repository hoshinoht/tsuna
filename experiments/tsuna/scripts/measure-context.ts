#!/usr/bin/env bun
/**
 * Measure the fixed per-request context of each sample-pack agent: the
 * system prompt and tool definitions exactly as sent on the wire (OMP's
 * Anthropic Messages client against the local fixture LLM), counted with
 * pi-natives' Claude v5 tokenizer and o200k_base.
 *
 *   bun scripts/measure-context.ts [--mcp]   (--mcp adds the demo MCP fixture server)
 */
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { startLlmFixture, type FixtureLlmRequest } from "../fixtures/models/llm-server.ts";
import { defaultConfig, parseMcp } from "../src/config.ts";
import { Harness } from "../src/harness.ts";
import { loadNatives } from "../src/tools/natives.ts";

const here = resolve(import.meta.dir, "..");
const withMcp = process.argv.includes("--mcp");
const first = new Map<string, FixtureLlmRequest>();
const fx = startLlmFixture(req => {
	const role = /Tsuna-Role: ([a-z0-9-]+)/.exec(JSON.stringify(req.body.system))?.[1] ?? "?";
	if (!first.has(role)) first.set(role, req);
	return { text: "ok" };
});
process.env.TSUNA_MEASURE_KEY = "measure";
const state = mkdtempSync(join(tmpdir(), "tsuna-measure-"));
const workspace = mkdtempSync(join(tmpdir(), "tsuna-measure-ws-"));
writeFileSync(join(workspace, "README.md"), "x\n");
const config = defaultConfig();
config.agentPacks = [join(here, "agent-packs/sample")];
const entry = { provider: "anthropic", id: "claude-opus-5-5", reasoning: true, contextWindow: 0, maxTokens: 0, input: ["text" as const] };
config.providers = {
	providers: { anthropic: { type: "omp", ompProvider: "anthropic", apiKeyEnv: "TSUNA_MEASURE_KEY", baseUrl: fx.url } },
	models: { primary: { ...entry }, fast: { ...entry }, strong: { ...entry } },
};
config.context.projectInstructions = false;
if (withMcp) config.mcp = parseMcp(JSON.parse(await Bun.file(join(here, "fixtures/demo/mcp.json")).text()), join(here, "fixtures/demo"), "demo mcp");
const harness = await Harness.start({ config, state, cwd: workspace, interactive: false });
const rt = harness.runtime;
await rt.promptPrimary("measure");
for (const role of ["explore", "code-engineer", "code-checker", "tester"]) {
	await rt.spawn(rt.primaryId, { agent: role, task: "measure" }).done.catch(() => undefined);
}
await harness.shutdown();
fx.stop();

const natives = await loadNatives(join(state, "natives"));
const count = (s: string) => ({ claude: natives.countTokens(s, natives.Encoding.ClaudeV5), o200k: natives.countTokens(s, natives.Encoding.O200kBase) });
const rows: string[][] = [["agent", "tools", "system (Claude v5)", "tools (Claude v5)", "total (Claude v5)", "total (o200k)", "system chars", "tools chars"]];
for (const [role, req] of first) {
	const system = (req.body.system as { text?: string }[]).map(b => b.text ?? "").join("\n");
	const tools = JSON.stringify(req.body.tools ?? []);
	const s = count(system);
	const t = count(tools);
	rows.push([role, String((req.body.tools as unknown[]).length), String(s.claude), String(t.claude), String(s.claude + t.claude), String(s.o200k + t.o200k), String(system.length), String(tools.length)]);
}
const widths = rows[0]!.map((_, i) => Math.max(...rows.map(r => r[i]!.length)));
for (const r of rows) console.log(r.map((c, i) => c.padEnd(widths[i]!)).join("  "));
const tools = (first.get("orchestrator")!.body.tools as { name: string; description?: string; input_schema?: unknown }[]).map(t => ({ name: t.name, tokens: count(JSON.stringify(t)).claude })).sort((a, b) => b.tokens - a.tokens);
console.log(`\norchestrator tool cost (Claude v5 tokens): ${tools.map(t => `${t.name} ${t.tokens}`).join(", ")}`);
