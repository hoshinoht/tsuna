import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import type { FixtureReply, FixtureTurn } from "../../src/backend/fixture-types.ts";
import { defaultConfig, type McpConfig, type TsunaConfig } from "../../src/config.ts";
import { Harness, type HarnessOptions } from "../../src/harness.ts";
import type { RuntimeEvent } from "../../src/orchestration/types.ts";

export const EXPERIMENT = resolve(import.meta.dir, "../..");
export const SAMPLE_PACK = join(EXPERIMENT, "agent-packs/sample");

export type Script = (turn: FixtureTurn) => FixtureReply | Promise<FixtureReply>;

export function setScript(script: Script) {
	(globalThis as { __TSUNA_SCRIPT__?: Script }).__TSUNA_SCRIPT__ = script;
}

export function tempDir(prefix = "tsuna-test-"): string {
	return mkdtempSync(join(tmpdir(), prefix));
}

export function testConfig(overrides: Partial<TsunaConfig> & { mcp?: McpConfig } = {}): TsunaConfig {
	const config = defaultConfig();
	config.agentPacks = [SAMPLE_PACK];
	config.providers = {
		providers: { fixture: { type: "fixture", script: join(EXPERIMENT, "test/helpers/script.ts") } },
		models: {
			primary: { provider: "fixture", id: "fixture-primary", reasoning: true, contextWindow: 200_000, maxTokens: 8192, input: ["text", "image"] },
			fast: { provider: "fixture", id: "fixture-fast", reasoning: true, contextWindow: 200_000, maxTokens: 8192, input: ["text"] },
			strong: { provider: "fixture", id: "fixture-strong", reasoning: true, contextWindow: 200_000, maxTokens: 8192, input: ["text"] },
		},
	};
	return { ...config, ...overrides };
}

export interface TestRig {
	harness: Harness;
	events: RuntimeEvent[];
	state: string;
	workspace: string;
	config: TsunaConfig;
}

export async function startRig(opts: { config?: TsunaConfig; state?: string; workspace?: string; resume?: string; interactive?: boolean; approver?: HarnessOptions["approver"]; reviewer?: HarnessOptions["reviewer"]; createSession?: HarnessOptions["createSession"] } = {}): Promise<TestRig> {
	const state = opts.state ?? tempDir("tsuna-state-");
	const workspace = opts.workspace ?? tempDir("tsuna-ws-");
	mkdirSync(workspace, { recursive: true });
	const config = opts.config ?? testConfig();
	const events: RuntimeEvent[] = [];
	const harness = await Harness.start({
		config,
		state,
		cwd: workspace,
		interactive: opts.interactive ?? false,
		resume: opts.resume,
		approver: opts.approver,
		reviewer: opts.reviewer,
		onEvent: e => events.push(e),
		createSession: opts.createSession,
	});
	return { harness, events, state, workspace, config };
}

/** Helper for scripts: the latest tool result text by tool name. */
export function lastResult(turn: FixtureTurn, name?: string) {
	return [...turn.lastToolResults].reverse().find(r => !name || r.name === name);
}

export function call(name: string, args: Record<string, unknown>) {
	return { toolCalls: [{ name, arguments: args }] };
}
