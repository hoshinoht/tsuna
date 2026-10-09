/**
 * Composition root: wires Tsuna-owned config, state, agent packs, policy,
 * Pi backend, tools, MCP and the agent runtime, and owns their shutdown.
 */
import { realpathSync } from "node:fs";
import { loadDefinitions, type DefinitionCatalog } from "./agents/definitions.ts";
import { createBackendSession, ModelHub, type BackendSession, type BackendSessionSpec } from "./backend/pi.ts";
import { loadConfig, type TsunaConfig } from "./config.ts";
import { cacheRisk, compressToolResults, pruneImages, type ImagePruneReason } from "./context/transforms.ts";
import { DEFAULT_INSTRUCTION_FOLDERS, loadProjectContext, renderProjectContext } from "./context/project.ts";
import { scrubbedEnv, SupervisorClient } from "./jobs/supervisor.ts";
import { McpManager } from "./mcp/manager.ts";
import { orchestrationTools, runtimeTools } from "./orchestration/tools.ts";
import { AgentRuntime } from "./orchestration/runtime.ts";
import type { AgentRecord, RuntimeEvent } from "./orchestration/types.ts";
import { ensureDirs, resolvePaths, type TsunaPaths } from "./paths.ts";
import { PermissionGate, type Approver, type Reviewer } from "./policy/gate.ts";
import { builtinTools } from "./tools/builtins.ts";
import { toPiTool } from "./tools/pi-adapter.ts";
import type { ToolImpl } from "./tools/types.ts";

export interface HarnessOptions {
	config?: string | TsunaConfig;
	state?: string;
	cwd: string;
	interactive: boolean;
	role?: string;
	/** Resume an existing root session tree. */
	resume?: string;
	approver?: Approver;
	reviewer?: Reviewer;
	onEvent?: (event: RuntimeEvent) => void;
	/** Test hook: wrap or replace backend session creation. */
	createSession?: (spec: BackendSessionSpec) => Promise<BackendSession>;
	env?: NodeJS.ProcessEnv;
}

/** Per-agent outgoing-context transform (compression, image budget, cache advisory). */
class ContextTransforms {
	private readonly imageDecisions = new Map<string, Map<string, ImagePruneReason>>();
	private readonly advised = new Set<string>();

	constructor(
		private readonly config: TsunaConfig,
		private readonly emit: (e: RuntimeEvent) => void,
		private readonly save: (record: AgentRecord) => void,
	) {}

	for(record: AgentRecord) {
		return (messages: { role: string; content?: unknown; [k: string]: unknown }[]) => {
			let out = compressToolResults(messages, record.compressions);
			let decisions = this.imageDecisions.get(record.id);
			if (!decisions) {
				decisions = new Map(Object.entries(record.imagePrune ?? {}) as [string, ImagePruneReason][]);
				this.imageDecisions.set(record.id, decisions);
			}
			const pruned = pruneImages(out, decisions, this.config.context.imageBudget);
			out = pruned.messages;
			if (pruned.changed) {
				record.imagePrune = Object.fromEntries(decisions);
				this.save(record);
			}
			const risk = cacheRisk(out, this.config.context.cacheAdvisory, { provider: record.contract.provider, id: record.contract.modelId });
			if (risk && !this.advised.has(`${record.id}|${risk.fingerprint}`)) {
				this.advised.add(`${record.id}|${risk.fingerprint}`);
				this.emit({ type: "warning", agentId: record.id, message: `cache advisory: ${risk.idleMinutes.toFixed(0)} min idle on ${risk.provider}/${risk.model} with ${risk.cacheReadTokens} cached tokens; the next request will likely miss the prompt cache` });
			}
			return out;
		};
	}
}

export class Harness {
	private closed = false;

	private constructor(
		readonly config: TsunaConfig,
		readonly paths: TsunaPaths,
		readonly catalog: DefinitionCatalog,
		readonly hub: ModelHub,
		readonly gate: PermissionGate,
		readonly mcp: McpManager,
		readonly supervisor: SupervisorClient,
		public runtime: AgentRuntime,
	) {}

	static async start(opts: HarnessOptions): Promise<Harness> {
		const env = opts.env ?? process.env;
		const config = typeof opts.config === "object" ? opts.config : loadConfig(opts.config, env);
		const paths = resolvePaths(opts.state, env);
		ensureDirs(paths);
		const cwd = realpathSync(opts.cwd);
		const catalog = loadDefinitions(config.agentPacks);
		const hub = await ModelHub.create(config.providers, env);
		const events: ((e: RuntimeEvent) => void)[] = [];
		const emit = (e: RuntimeEvent) => {
			for (const listener of events) listener(e);
		};
		if (opts.onEvent) events.push(opts.onEvent);
		const gate = new PermissionGate({ approver: opts.approver, reviewer: opts.reviewer });
		const mcp = new McpManager(config.mcp, cwd, env);
		await mcp.connectAll();
		for (const state of mcp.states.values()) {
			if (state.status === "failed") emit({ type: "warning", message: `MCP server ${state.name} failed to connect: ${state.error}` });
		}
		const supervisor = new SupervisorClient(config.supervisor, paths.jobs);
		let runtimeRef: AgentRuntime | undefined;
		const builtins = builtinTools({
			nativesDir: paths.natives,
			jobEnv: scrubbedEnv({}, env),
			supervisor,
			jobs: {
				record: (agentId, job) => runtimeRef?.recordJob(agentId, job),
				owned: (agentId, jobId) => runtimeRef?.record(agentId)?.jobs.find(j => j.jobId === jobId)?.runDirectory,
			},
		});
		const allTools = (): Map<string, ToolImpl> => new Map([...builtins, ...mcp.toolImpls()]);
		const instructionsCache = new Map<string, string>();
		const projectInstructions = (dir: string) => {
			if (!config.context.projectInstructions) return "";
			if (!instructionsCache.has(dir)) instructionsCache.set(dir, renderProjectContext(loadProjectContext(dir, DEFAULT_INSTRUCTION_FOLDERS)));
			return instructionsCache.get(dir)!;
		};
		// Context transforms must exist before the primary session opens (inside
		// create/resume), so they cannot depend on the Harness instance.
		const transforms = new ContextTransforms(config, emit, record => runtimeRef?.store.save(record));
		const deps = {
			paths,
			config,
			catalog,
			hub,
			gate,
			tools: allTools,
			mcpServers: () => mcp.enabledServers(),
			createSession: opts.createSession ?? createBackendSession,
			toPiTool,
			contextTransform: (record: AgentRecord) => transforms.for(record),
			projectInstructions,
			emit,
			orchestrationTools: (rt: AgentRuntime) => new Map([...orchestrationTools(rt), ...runtimeTools(rt)]),
		};
		const role = opts.role ?? config.primaryRole;
		let runtime: AgentRuntime;
		try {
			runtime = opts.resume
				? await AgentRuntime.resume({ ...deps, onReady: rt => (runtimeRef = rt) }, opts.resume, { interactive: opts.interactive })
				: await AgentRuntime.create({ ...deps, onReady: rt => (runtimeRef = rt) }, { role, cwd, interactive: opts.interactive });
		} catch (error) {
			// Release what this start already owns (MCP server processes).
			await mcp.shutdown();
			throw error;
		}
		runtimeRef = runtime;
		const harness = new Harness(config, paths, catalog, hub, gate, mcp, supervisor, runtime);
		harness.listeners = events;
		return harness;
	}

	listeners: ((e: RuntimeEvent) => void)[] = [];

	onEvent(listener: (e: RuntimeEvent) => void): () => void {
		this.listeners.push(listener);
		return () => {
			const i = this.listeners.indexOf(listener);
			if (i >= 0) this.listeners.splice(i, 1);
		};
	}

	/** Release every owned resource: agent sessions, MCP connections, root lock. */
	async shutdown(): Promise<void> {
		if (this.closed) return;
		this.closed = true;
		await this.runtime.shutdown();
		await this.mcp.shutdown();
	}
}
