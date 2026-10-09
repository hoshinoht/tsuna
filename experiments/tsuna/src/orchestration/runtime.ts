/**
 * Tsuna agent runtime: the authoritative owner of agent lifecycle.
 *
 * Behaviour adapted from Oh My Pi v18.8.6 orchestration (registry CAS
 * guards, park/revive coalescing, per-parent spawn permits, owner-scoped
 * waits, terminal-yield handling), re-implemented for the Pi SDK backend.
 * See docs/DESIGN.md §4 and docs/PROVENANCE.md.
 */
import { randomBytes } from "node:crypto";
import { existsSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { Value } from "typebox/value";
import type { AgentDefinition, DefinitionCatalog, ReasoningLevel } from "../agents/definitions.ts";
import { REASONING_LEVELS } from "../agents/definitions.ts";
import type { AgentMessageLike, AgentSessionEvent, BackendSession, BackendSessionSpec, ModelHub, ToolDefinition } from "../backend/pi.ts";
import type { TsunaConfig } from "../config.ts";
import { canSurfaceTool, type PolicyContext } from "../policy/engine.ts";
import type { GateSession, PermissionGate } from "../policy/gate.ts";
import { isInside, type TsunaPaths } from "../paths.ts";
import type { ToolImpl, ToolOutput } from "../tools/types.ts";
import { Semaphore } from "./semaphore.ts";
import { AgentStore, contractHash } from "./store.ts";
import type { Actor, AgentContract, AgentRecord, AgentStatus, IncrementalOutput, ResultRecord, RuntimeEvent, SendOutcome } from "./types.ts";

export class RuntimeError extends Error {}

const TERMINAL: ReadonlySet<AgentStatus> = new Set(["cancelled", "failed"]);
const YIELD_REMINDER =
	"[tsuna] Your assignment is not complete until you call `yield` alone in a message with your final result (or `status: \"failure\"` with an error). Call it now.";
const MAX_YIELD_REMINDERS = 2;
/** A child that keeps submitting invalid terminal yields fails instead of looping forever. */
const MAX_REJECTED_YIELDS = 5;
export const RESULT_MARKER = (resultId: string) => `[tsuna-result ${resultId}]`;
const RESULT_MARKER_RE = /\[tsuna-result ([A-Za-z0-9_\-/]+)\]/g;

/** Names of orchestration tools; only agents allowed to spawn receive them. */
export const ORCHESTRATION_TOOLS = ["task", "dispatch", "agent_list", "agent_read", "agent_send", "agent_interrupt", "agent_cancel", "agent_resume", "wait"];

export interface RuntimeDeps {
	paths: TsunaPaths;
	config: TsunaConfig;
	catalog: DefinitionCatalog;
	hub: ModelHub;
	gate: PermissionGate;
	/** All non-orchestration tools available in this process (builtins + MCP). */
	tools: () => Map<string, ToolImpl>;
	mcpServers: () => ReadonlySet<string>;
	createSession: (spec: BackendSessionSpec) => Promise<BackendSession>;
	toPiTool: (impl: ToolImpl, invoke: (name: string, toolCallId: string, input: Record<string, unknown>, signal?: AbortSignal, onUpdate?: (t: string) => void) => Promise<ToolOutput>) => ToolDefinition;
	contextTransform?: (record: AgentRecord) => ((messages: AgentMessageLike[]) => AgentMessageLike[]) | undefined;
	projectInstructions?: (cwd: string) => string;
	emit?: (event: RuntimeEvent) => void;
	/** Called once the runtime object exists, before any session opens. */
	onReady?: (runtime: AgentRuntime) => void;
	/** Orchestration tools bound to this runtime (built by tools.ts). */
	orchestrationTools?: (runtime: AgentRuntime) => Map<string, ToolImpl>;
}

interface YieldCandidate {
	status: "success" | "failure";
	data?: unknown;
	text?: string;
	error?: string;
}

interface RunState {
	runId: string;
	generation: number;
	controller: AbortController;
	terminal: YieldCandidate | null;
	/** Tool calls of the latest assistant message (for mixed-batch checks). */
	batch: { id: string; name: string }[];
	interrupted: boolean;
	/** Consecutive rejected terminal yields in this run. */
	rejectedYields: number;
	deliveryVia?: "task";
	done: Promise<ResultRecord | undefined>;
}

interface LiveAgent {
	record: AgentRecord;
	session: BackendSession | null;
	unsubscribe?: () => void;
	run: RunState | null;
	/** Follow-up assignments received while running (each becomes its own run). */
	pending: { text: string; deliveryVia?: "task" }[];
	/** Steering received while not running; prepended to the next run. */
	queuedSteers: string[];
	revival: Promise<BackendSession> | null;
	parking: Promise<void> | null;
	/** Permits for this agent's own children. */
	permits: Semaphore;
	waiters: Set<() => void>;
	/** Messages addressed to this agent that a `wait` may surface. */
	inbox: string[];
	tools: Map<string, ToolImpl>;
}

function newId(role: string, n: number): string {
	return `${role}-${n}-${randomBytes(2).toString("hex")}`;
}

function clampReasoning(def: AgentDefinition, requested?: ReasoningLevel): ReasoningLevel {
	const level = requested ?? def.reasoning.default;
	const idx = (l: ReasoningLevel) => REASONING_LEVELS.indexOf(l);
	let value = idx(level);
	if (def.reasoning.min) value = Math.max(value, idx(def.reasoning.min));
	if (def.reasoning.max) value = Math.min(value, idx(def.reasoning.max));
	return REASONING_LEVELS[value]!;
}

/** `[reasoning:fast|deep]` hints in a task text move within the definition's bounds only. */
export function reasoningHint(def: AgentDefinition, text: string): ReasoningLevel | undefined {
	const match = /^\s*\[reasoning:(fast|balanced|deep)\]/.exec(text);
	if (!match) return undefined;
	const base = REASONING_LEVELS.indexOf(def.reasoning.default);
	const delta = match[1] === "fast" ? -1 : match[1] === "deep" ? 1 : 0;
	return clampReasoning(def, REASONING_LEVELS[Math.max(0, Math.min(REASONING_LEVELS.length - 1, base + delta))]);
}

/** Returns a description of the first schema violations, or undefined when valid. */
export function validateInput(impl: ToolImpl, input: Record<string, unknown>): string | undefined {
	try {
		if (Value.Check(impl.parameters as never, input)) return undefined;
		return [...Value.Errors(impl.parameters as never, input)].slice(0, 3).map(e => `${(e as { instancePath?: string }).instancePath || "/"} ${(e as { message: string }).message}`).join("; ");
	} catch (error) {
		// Server-provided MCP schemas may use keywords the checker cannot compile;
		// their policy does not depend on argument values, so the server validates.
		if (impl.external) return undefined;
		return `schema check failed: ${(error as Error).message}`;
	}
}

function textOfContent(content: unknown): string {
	if (typeof content === "string") return content;
	if (Array.isArray(content)) {
		return content.map(b => (b && typeof b === "object" && (b as { type?: string }).type === "text" ? String((b as { text?: unknown }).text ?? "") : "")).join("");
	}
	return "";
}

export class AgentRuntime {
	readonly rootId: string;
	readonly store: AgentStore;
	private readonly live = new Map<string, LiveAgent>();
	private shuttingDown = false;
	private counter = 0;
	readonly warnings: string[] = [];

	private constructor(
		private readonly deps: RuntimeDeps,
		rootId: string,
	) {
		this.rootId = rootId;
		this.store = new AgentStore(deps.paths.agents, rootId);
	}

	// ---------------------------------------------------------------- setup

	/** Start a new root tree with a primary agent of `role`. */
	static async create(deps: RuntimeDeps, opts: { role: string; cwd: string; interactive: boolean; rootId?: string }): Promise<AgentRuntime> {
		const rootId = opts.rootId ?? `root-${Date.now().toString(36)}-${randomBytes(3).toString("hex")}`;
		const runtime = new AgentRuntime(deps, rootId);
		if (runtime.store.exists()) throw new RuntimeError(`root ${rootId} already exists; resume it instead`);
		deps.onReady?.(runtime);
		const def = deps.catalog.require(opts.role);
		if (!def.primary) throw new RuntimeError(`agent "${opts.role}" is not allowed as a primary agent`);
		runtime.store.lock();
		try {
			const record = runtime.newRecord(def, null, 0, opts.cwd, opts.interactive, undefined);
			runtime.adopt(record);
			runtime.store.save(record);
			await runtime.ensureLive(runtime.live.get(record.id)!);
			record.status = "idle";
			runtime.store.save(record);
			return runtime;
		} catch (error) {
			await runtime.shutdown().catch(() => undefined);
			throw error;
		}
	}

	/**
	 * Reopen a root after a Tsuna restart. Runs that were in flight are marked
	 * interrupted (outcome unknown) and never replayed. Records whose contract
	 * cannot be verified fail closed: transcript inspection only.
	 */
	static async resume(deps: RuntimeDeps, rootId: string, opts: { interactive: boolean }): Promise<AgentRuntime> {
		const runtime = new AgentRuntime(deps, rootId);
		if (!runtime.store.exists()) throw new RuntimeError(`unknown root ${rootId}`);
		deps.onReady?.(runtime);
		runtime.store.lock();
		try {
			await runtime.restore(opts);
			return runtime;
		} catch (error) {
			await runtime.shutdown().catch(() => undefined);
			throw error;
		}
	}

	private async restore(opts: { interactive: boolean }): Promise<void> {
		const runtime = this;
		const rootId = this.rootId;
		const { records, corrupt } = runtime.store.loadAll();
		for (const c of corrupt) runtime.warn(`corrupt agent record ${c.file}: ${c.error} (ignored; not revivable)`);
		const ids = new Set(records.map(r => r.id));
		for (const record of records.sort((a, b) => a.depth - b.depth)) {
			runtime.counter = Math.max(runtime.counter, Number(/-(\d+)-/.exec(record.id)?.[1] ?? 0));
			if (record.parentId && !ids.has(record.parentId)) {
				record.status = "failed";
				record.failure = "parent record missing or corrupt; child not revivable";
			}
			if (record.status === "running" || record.status === "queued") {
				record.interruptedRunId = record.currentRunId;
				record.status = "parked";
				if (record.currentRunId && !record.results.some(r => r.runId === record.currentRunId) && record.parentId) {
					record.results.push(runtime.makeResult(record, record.currentRunId, {
						status: "failure",
						error: "run interrupted by a Tsuna restart; its outcome is unknown and it was not replayed",
					}, true));
				}
			} else if (record.status === "idle") {
				record.status = "parked";
			}
			if (!TERMINAL.has(record.status)) {
				const problem = runtime.verifyContract(record);
				if (problem) {
					record.status = "failed";
					record.failure = `recovery failed closed: ${problem}`;
					runtime.warn(`${record.id}: ${record.failure}`);
				}
			}
			const parentRecord = record.parentId ? runtime.live.get(record.parentId)?.record : undefined;
			if (parentRecord && record.depth !== parentRecord.depth + 1 && !TERMINAL.has(record.status)) {
				record.status = "failed";
				record.failure = "depth does not match the parent record";
			}
			// A cancel cascade interrupted by a crash is completed here.
			if (parentRecord?.status === "cancelled" && record.status !== "cancelled") {
				record.status = "cancelled";
				record.failure = "parent was cancelled (cascade completed at recovery)";
			}
			if (record.parentId && runtime.live.get(record.parentId)?.record.status === "failed" && !TERMINAL.has(record.status)) {
				record.status = "failed";
				record.failure = "ancestor failed recovery; child not revivable";
			}
			runtime.adopt(record);
		}
		// Reconcile deliveries that were in flight when the process stopped.
		for (const live of runtime.live.values()) {
			for (const result of live.record.results) {
				if (result.delivery.state !== "delivering") continue;
				const parent = live.record.parentId ? runtime.live.get(live.record.parentId) : undefined;
				const seen = parent?.record.sessionFile && existsSync(parent.record.sessionFile)
					? readFileSync(parent.record.sessionFile, "utf8")
							.split("\n")
							.some(line => line.includes(RESULT_MARKER(result.resultId)) && !line.includes('"role":"assistant"'))
					: false;
				result.delivery = seen ? { ...result.delivery, state: "delivered" } : { state: "pending" };
			}
			runtime.store.save(live.record);
		}
		const primary = runtime.primary();
		if (!primary) throw new RuntimeError(`root ${rootId} has no primary record`);
		if (primary.record.status === "failed") throw new RuntimeError(`primary agent cannot be restored: ${primary.record.failure}`);
		primary.record.contract = { ...primary.record.contract, interactive: opts.interactive };
		// Interactivity is a property of this process, not of the stored contract.
		primary.record.contractHash = contractHash(primary.record.contract, primary.record);
		await runtime.ensureLive(primary);
		primary.record.status = "idle";
		runtime.store.save(primary.record);
	}

	private warn(message: string) {
		this.warnings.push(message);
		this.emit({ type: "warning", message });
	}

	private emit(event: RuntimeEvent) {
		this.deps.emit?.(event);
	}

	private newRecord(def: AgentDefinition, parent: AgentRecord | null, depth: number, cwd: string, interactive: boolean, reasoning?: ReasoningLevel): AgentRecord {
		const resolved = this.deps.hub.resolve(def.model);
		const level = this.deps.hub.checkReasoning(resolved, clampReasoning(def, reasoning));
		const id = parent ? newId(def.name, ++this.counter) : `${def.name}-0-${randomBytes(2).toString("hex")}`;
		const maxDepth = this.deps.config.scheduler.maxDepth;
		const canSpawn = def.spawns.length > 0 && depth < maxDepth;
		const policy: PolicyContext = {
			role: def.name,
			rules: def.permissions,
			cwd,
			approvalMode: this.deps.config.approvalMode,
			mcpServers: this.deps.mcpServers(),
			harnessDenies: this.deps.config.harnessDenies,
		};
		const available = this.deps.tools();
		const tools: string[] = [];
		const wanted = new Set<string>();
		for (const pattern of def.tools) {
			if (pattern.endsWith("*")) {
				const prefix = pattern.slice(0, -1);
				for (const name of [...available.keys(), ...ORCHESTRATION_TOOLS]) if (name.startsWith(prefix)) wanted.add(name);
			} else wanted.add(pattern);
		}
		for (const name of wanted) {
			const orchestration = ORCHESTRATION_TOOLS.includes(name);
			if (orchestration && !canSpawn) continue;
			if (!orchestration && !available.has(name) && name !== "batch" && name !== "compress") {
				this.warn(`${def.name}: requested tool "${name}" is not available`);
				continue;
			}
			if (!canSurfaceTool(policy, name)) continue;
			tools.push(name);
		}
		if (parent) tools.push("yield");
		tools.sort();
		const contract: AgentContract = {
			role: def.name,
			definition: def.provenance,
			systemPrompt: "",
			modelEntry: def.model,
			provider: resolved.provider,
			modelId: resolved.id,
			reasoning: level,
			tools,
			spawns: canSpawn ? [...def.spawns] : [],
			permissions: def.permissions,
			harnessDenies: this.deps.config.harnessDenies,
			approvalMode: this.deps.config.approvalMode,
			interactive,
			cwd,
			outputSchema: def.output?.schema,
			maxDepth,
		};
		contract.systemPrompt = this.composeSystemPrompt(id, def, contract, parent, depth);
		const now = Date.now();
		return {
			version: 1,
			id,
			rootId: this.rootId,
			parentId: parent?.id ?? null,
			depth,
			role: def.name,
			contract,
			contractHash: contractHash(contract, { id, rootId: this.rootId, parentId: parent?.id ?? null, depth }),
			status: "queued",
			generation: 0,
			runSeq: 0,
			results: [],
			outputs: [],
			jobs: [],
			compressions: {},
			createdAt: now,
			updatedAt: now,
		};
	}

	private composeSystemPrompt(id: string, def: AgentDefinition, contract: AgentContract, parent: AgentRecord | null, depth: number): string {
		const lines = [`Tsuna-Agent-Id: ${id}`, `Tsuna-Role: ${def.name}`, "", def.prompt, "", "## Tsuna runtime contract", ""];
		lines.push(`Workspace: ${contract.cwd}`);
		if (parent) {
			lines.push(`You are a delegated child agent (depth ${depth}) of ${parent.id}. You run headless: actions that need approval are denied, not prompted.`);
			lines.push("Finish every assignment by calling `yield` alone in its own message. Use `yield` with `final: false` for incremental output.");
		} else {
			lines.push("You are the primary agent for this session.");
		}
		if (contract.spawns.length > 0) {
			lines.push(`You may delegate to: ${contract.spawns.join(", ")}. Children results arrive through \`task\`, \`wait\`, or as [tsuna-result …] notices.`);
		}
		lines.push(`Tools: ${contract.tools.join(", ") || "(none)"}`);
		const instructions = this.deps.projectInstructions?.(contract.cwd);
		if (instructions) lines.push("", instructions);
		return lines.join("\n");
	}

	/** Returns a problem string when the stored contract can no longer be honoured. */
	private verifyContract(record: AgentRecord): string | undefined {
		if (contractHash(record.contract, record) !== record.contractHash) return "contract hash mismatch (record modified or corrupt)";
		if (record.role !== record.contract.role) return "record role differs from its contract";
		const sessionsRoot = join(this.deps.paths.sessions, this.rootId, record.id);
		if (record.sessionFile && !isInside(resolve(record.sessionFile), sessionsRoot)) return `transcript path ${record.sessionFile} is outside ${sessionsRoot}`;
		try {
			const resolved = this.deps.hub.resolve(record.contract.modelEntry);
			if (resolved.provider !== record.contract.provider || resolved.id !== record.contract.modelId) {
				return `model entry ${record.contract.modelEntry} now resolves to ${resolved.provider}/${resolved.id}, not ${record.contract.provider}/${record.contract.modelId}`;
			}
		} catch (error) {
			return `model unavailable: ${(error as Error).message}`;
		}
		try {
			if (!statSync(record.contract.cwd).isDirectory()) return `workspace ${record.contract.cwd} is not a directory`;
		} catch {
			return `workspace ${record.contract.cwd} no longer exists`;
		}
		// Pi writes the JSONL lazily (after the first message); only a transcript
		// that is known to have been written must still exist.
		if (record.transcriptStarted && (!record.sessionFile || !existsSync(record.sessionFile))) return `transcript ${record.sessionFile} is missing`;
		const def = this.deps.catalog.get(record.role);
		if (!def) this.warn(`${record.id}: definition "${record.role}" is no longer loaded; restoring from the recorded contract`);
		else if (def.provenance.sha256 !== record.contract.definition.sha256) {
			this.warn(`${record.id}: definition "${record.role}" changed since spawn; the recorded contract is used, not the new definition`);
		}
		return undefined;
	}

	private adopt(record: AgentRecord): LiveAgent {
		const live: LiveAgent = {
			record,
			session: null,
			run: null,
			pending: [],
			queuedSteers: [],
			revival: null,
			parking: null,
			permits: new Semaphore(this.deps.config.scheduler.maxConcurrency),
			waiters: new Set(),
			inbox: [],
			tools: new Map(),
		};
		this.live.set(record.id, live);
		return live;
	}

	primary(): LiveAgent | undefined {
		for (const live of this.live.values()) if (live.record.parentId === null) return live;
		return undefined;
	}

	get primaryId(): string {
		return this.primary()!.record.id;
	}

	record(id: string): AgentRecord | undefined {
		return this.live.get(id)?.record;
	}

	records(): AgentRecord[] {
		return [...this.live.values()].map(l => l.record);
	}

	gateSessionFor(record: AgentRecord): GateSession {
		return {
			agentId: record.id,
			interactive: record.parentId === null && record.contract.interactive,
			policy: {
				role: record.contract.role,
				rules: record.contract.permissions,
				cwd: record.contract.cwd,
				approvalMode: record.contract.approvalMode,
				mcpServers: this.deps.mcpServers(),
				// Recorded denies plus the current config's: a deny added later still applies.
				harnessDenies: [...record.contract.harnessDenies, ...this.deps.config.harnessDenies],
			},
		};
	}

	// ------------------------------------------------------------- sessions

	private toolsFor(live: LiveAgent): Map<string, ToolImpl> {
		const available = this.deps.tools();
		const orchestration = this.deps.orchestrationTools?.(this) ?? new Map<string, ToolImpl>();
		const map = new Map<string, ToolImpl>();
		for (const name of live.record.contract.tools) {
			const impl = orchestration.get(name) ?? available.get(name);
			if (impl) map.set(name, impl);
			else this.warn(`${live.record.id}: tool "${name}" from the contract is unavailable in this process (omitted; capability narrowed)`);
		}
		return map;
	}

	/** Every tool call — direct or nested — passes the catalog check and the gate. */
	async invokeTool(agentId: string, name: string, toolCallId: string, input: Record<string, unknown>, signal?: AbortSignal, onUpdate?: (t: string) => void): Promise<ToolOutput> {
		const live = this.live.get(agentId);
		if (!live) return { text: `unknown agent ${agentId}`, isError: true };
		if (TERMINAL.has(live.record.status)) return { text: `agent ${agentId} is ${live.record.status}`, isError: true };
		const impl = live.tools.get(name);
		if (!impl) return { text: `Tool "${name}" is not in this agent's catalog`, isError: true };
		// Validate every invocation (nested batch calls included) against the tool's
		// schema before policy sees it; Pi validates only top-level arguments.
		const invalid = validateInput(impl, input);
		if (invalid) return { text: `Invalid arguments for ${name}: ${invalid}`, isError: true };
		const gateSession = this.gateSessionFor(live.record);
		const verdict = await this.deps.gate.authorize(gateSession, { toolName: name, input }, signal);
		this.emit({ type: "permission", agentId, tool: name, allowed: verdict.allowed, reason: verdict.allowed ? undefined : verdict.reason, action: verdict.decision.action, resource: verdict.decision.resource });
		if (!verdict.allowed) return { text: `Permission denied: ${verdict.reason}`, isError: true, details: { denied: true, action: verdict.decision.action, resource: verdict.decision.resource } };
		let nested = 0;
		try {
			return await impl.execute(
				{
					agentId,
					rootId: this.rootId,
					role: live.record.contract.role,
					cwd: live.record.contract.cwd,
					toolCallId,
					signal,
					gate: gateSession,
					invoke: (n, i, s) => this.invokeTool(agentId, n, `${toolCallId}/${++nested}`, i, s ?? signal, onUpdate),
					onUpdate,
				},
				input,
			);
		} catch (error) {
			return { text: `Tool ${name} failed: ${(error as Error).message}`, isError: true };
		}
	}

	private async openSession(live: LiveAgent): Promise<BackendSession> {
		const record = live.record;
		live.tools = this.toolsFor(live);
		const resolved = this.deps.hub.resolve(record.contract.modelEntry);
		const piTools = [...live.tools.values()].map(impl =>
			this.deps.toPiTool(impl, (name, toolCallId, input, signal, onUpdate) => this.invokeTool(record.id, name, toolCallId, input, signal, onUpdate)),
		);
		const sessionFile = record.sessionFile && existsSync(record.sessionFile) ? record.sessionFile : undefined;
		return this.deps.createSession({
			hub: this.deps.hub,
			cwd: record.contract.cwd,
			sessionDir: join(this.deps.paths.sessions, this.rootId, record.id),
			piAgentDir: join(this.deps.paths.state, "pi-agent"),
			sessionFile,
			systemPrompt: record.contract.systemPrompt,
			model: resolved,
			thinking: record.contract.reasoning,
			tools: piTools,
			transformContext: this.deps.contextTransform?.(record),
		});
	}

	private attach(live: LiveAgent, session: BackendSession) {
		live.session = session;
		live.record.generation += 1;
		live.unsubscribe = session.subscribe(event => this.onSessionEvent(live, session, event));
		if (session.sessionFile) live.record.sessionFile = session.sessionFile;
		this.store.save(live.record);
	}

	/**
	 * Return the live session, reviving a parked agent. Concurrent callers
	 * share one revival; revival waits for an in-flight park to finish
	 * disposing; a cancelled agent can never be revived.
	 */
	async ensureLive(live: LiveAgent): Promise<BackendSession> {
		if (live.session) return live.session;
		if (TERMINAL.has(live.record.status)) throw new RuntimeError(`agent ${live.record.id} is ${live.record.status}`);
		if (live.revival) return live.revival;
		const revival = (async () => {
			if (live.parking) await live.parking;
			if (live.record.depth > 0 || live.record.generation > 0) {
				const problem = this.verifyContract(live.record);
				if (problem) {
					this.setStatus(live, "failed");
					live.record.failure = `revival failed closed: ${problem}`;
					this.store.save(live.record);
					throw new RuntimeError(live.record.failure);
				}
			}
			const session = await this.openSession(live);
			if (TERMINAL.has(live.record.status) || this.shuttingDown) {
				await session.dispose();
				throw new RuntimeError(`agent ${live.record.id} became ${live.record.status} during revival`);
			}
			this.attach(live, session);
			return session;
		})();
		live.revival = revival;
		try {
			return await revival;
		} finally {
			if (live.revival === revival) live.revival = null;
		}
	}

	private onSessionEvent(live: LiveAgent, session: BackendSession, event: AgentSessionEvent) {
		if (live.session !== session) return; // stale session generation
		if (session.sessionFile && live.record.sessionFile !== session.sessionFile) {
			live.record.sessionFile = session.sessionFile;
			this.store.save(live.record);
		}
		const e = event as { type: string; message?: AgentMessageLike; toolName?: string };
		if (e.type === "message_end" && e.message) {
			const message = e.message;
			if (!live.record.transcriptStarted) {
				live.record.transcriptStarted = true;
				this.store.save(live.record);
			}
			if (message.role === "assistant" && live.run) {
				const calls = Array.isArray(message.content)
					? (message.content as { type?: string; id?: string; name?: string }[]).filter(b => b.type === "toolCall").map(b => ({ id: String(b.id), name: String(b.name) }))
					: [];
				live.run.batch = calls;
				const text = textOfContent(message.content).trim();
				if (text) {
					live.record.activity = text.slice(0, 160);
					this.emit({ type: "assistant_text", agentId: live.record.id, text });
				}
			}
			// Delivery confirmation: a result marker reached this agent's transcript.
			const text = textOfContent(message.content);
			if (message.role !== "assistant") for (const match of text.matchAll(RESULT_MARKER_RE)) this.markDelivered(match[1]!, live.record.id);
		} else if (e.type === "tool_execution_start" && e.toolName) {
			live.record.activity = `running ${e.toolName}`;
			this.emit({ type: "agent_activity", agentId: live.record.id, activity: live.record.activity });
		}
	}

	private setStatus(live: LiveAgent, status: AgentStatus) {
		const prior = live.record.status;
		if (prior === "cancelled" && status !== "cancelled") return; // cancelled is sticky
		if (prior === status) return;
		live.record.status = status;
		this.store.save(live.record);
		this.emit({ type: "agent_status", agentId: live.record.id, status, prior });
		for (const waiter of this.live.get(live.record.parentId ?? "")?.waiters ?? []) waiter();
	}

	/** Park an idle child: detach synchronously, then dispose. */
	async park(id: string, actor?: Actor): Promise<{ ok: boolean; reason?: string }> {
		const live = this.live.get(id);
		if (!live) return { ok: false, reason: "unknown agent" };
		if (actor) {
			const denied = this.authorizeControl(actor, live);
			if (denied) return { ok: false, reason: denied };
		}
		if (live.record.parentId === null) return { ok: false, reason: "the primary agent is not parked" };
		if (live.record.status !== "idle" || !live.session) return { ok: false, reason: `agent is ${live.record.status}` };
		if (live.run || live.revival || live.pending.length > 0) return { ok: false, reason: "agent has work or a revival in flight" };
		const session = live.session;
		live.session = null;
		live.unsubscribe?.();
		live.unsubscribe = undefined;
		this.setStatus(live, "parked");
		const parking = session.dispose();
		live.parking = parking;
		try {
			await parking;
		} finally {
			if (live.parking === parking) live.parking = null;
		}
		return { ok: true };
	}

	// ----------------------------------------------------------------- runs

	private makeResult(record: AgentRecord, runId: string, candidate: YieldCandidate, interrupted = false): ResultRecord {
		return {
			resultId: runId,
			runId,
			agentId: record.id,
			status: candidate.status,
			data: candidate.data,
			text: candidate.text,
			error: candidate.error,
			interrupted: interrupted || undefined,
			acceptedAt: Date.now(),
			delivery: { state: "pending" },
		};
	}

	/**
	 * Queue an assignment as a new run. Resolves with the accepted result
	 * (children) or undefined (primary / stale run).
	 */
	private startRun(live: LiveAgent, text: string, deliveryVia?: "task"): Promise<ResultRecord | undefined> {
		if (TERMINAL.has(live.record.status)) return Promise.reject(new RuntimeError(`agent ${live.record.id} is ${live.record.status}`));
		const record = live.record;
		record.runSeq += 1;
		const runId = `${record.id}/r${record.runSeq}`;
		record.currentRunId = runId;
		record.interruptedRunId = undefined;
		record.task = text.slice(0, 400);
		const controller = new AbortController();
		const run: RunState = {
			runId,
			generation: record.generation,
			controller,
			terminal: null,
			batch: [],
			interrupted: false,
			rejectedYields: 0,
			deliveryVia,
			done: undefined as never,
		};
		live.run = run;
		this.setStatus(live, "queued");
		run.done = this.executeRun(live, run, text);
		return run.done;
	}

	private isCurrent(live: LiveAgent, run: RunState): boolean {
		return live.run === run && live.record.currentRunId === run.runId && live.record.status !== "cancelled";
	}

	private async executeRun(live: LiveAgent, run: RunState, text: string): Promise<ResultRecord | undefined> {
		const record = live.record;
		const parent = record.parentId ? this.live.get(record.parentId) : undefined;
		const permits = parent?.permits;
		let acquired = false;
		let opening = false;
		let promptDelivery: string | undefined;
		let candidate: YieldCandidate | undefined;
		try {
			if (permits) {
				await permits.acquire(run.controller.signal);
				acquired = true;
			}
			if (!this.isCurrent(live, run)) return undefined;
			this.setStatus(live, "running");
			opening = true;
			const session = await this.ensureLive(live);
			opening = false;
			run.generation = record.generation;
			if (!this.isCurrent(live, run)) return undefined;
			const steers = live.queuedSteers.splice(0);
			promptDelivery = this.newDeliveryId("prompt");
			const pendingResults = this.takeUndeliveredFor(live, "prompt", undefined, promptDelivery);
			const prompt = [
				...steers.map(s => `[steering] ${s}`),
				...pendingResults,
				text,
			].join("\n\n");
			await session.prompt(prompt);
			// Children must finish with a terminal yield; remind a bounded number of times.
			for (let i = 0; record.parentId && i < MAX_YIELD_REMINDERS && !run.terminal && !run.interrupted && !run.controller.signal.aborted && this.isCurrent(live, run); i++) {
				await session.prompt(YIELD_REMINDER);
			}
			if (record.parentId) {
				candidate = run.interrupted
					? { status: "failure", error: "interrupted", text: session.lastAssistantText() }
					: run.terminal ?? { status: "failure", error: "child settled without a terminal yield", text: session.lastAssistantText() };
			}
		} catch (error) {
			candidate = { status: "failure", error: run.interrupted ? "interrupted" : (error as Error).message };
			// Startup failure only when opening the session itself failed; an
			// interrupt or shutdown while queued leaves the agent usable.
			if (opening && !run.interrupted && !live.session && !TERMINAL.has(record.status)) {
				record.failure = (error as Error).message;
				this.setStatus(live, "failed");
			}
			// Results taken for this prompt never reached the transcript: re-offer them.
			if (promptDelivery) this.revertDelivering(live, promptDelivery);
		} finally {
			if (acquired) permits!.release();
		}
		if (!this.isCurrent(live, run)) return undefined; // stale: cancelled or replaced
		live.run = null;
		let result: ResultRecord | undefined;
		if (record.parentId && candidate) {
			result = this.makeResult(record, run.runId, candidate, run.interrupted);
			if (run.deliveryVia === "task") result.delivery = { state: "delivering", via: "task", deliveryId: run.runId };
			record.results.push(result);
			this.emit({ type: "agent_result", agentId: record.id, result });
		}
		if (record.status !== "failed") this.setStatus(live, "idle");
		this.store.save(record);
		if (result && result.delivery.state === "pending") this.offerToParent(live, result);
		this.wakeParent(live);
		const next = live.pending.shift();
		if (next && record.status === "idle") void this.startRun(live, next.text, next.deliveryVia).catch(() => undefined);
		return result;
	}

	private wakeParent(live: LiveAgent) {
		const parent = live.record.parentId ? this.live.get(live.record.parentId) : undefined;
		for (const waiter of parent?.waiters ?? []) waiter();
	}

	/** Auto-delivery: wake a waiting parent, else steer into a running parent. */
	private offerToParent(live: LiveAgent, result: ResultRecord) {
		const parent = live.record.parentId ? this.live.get(live.record.parentId) : undefined;
		if (!parent || TERMINAL.has(parent.record.status)) return;
		if (parent.waiters.size > 0) return; // the parent's wait will take it
		if (parent.session && parent.run && parent.session.isStreaming) {
			const deliveryId = this.newDeliveryId("steer");
			const notices = this.takeUndeliveredFor(parent, "steer", undefined, deliveryId);
			if (notices.length) {
				void parent.session.steer(`[tsuna] Results from your children (harness notice, not a new assignment):\n\n${notices.join("\n\n")}`).catch(() => this.revertDelivering(parent, deliveryId));
			}
		}
	}

	private formatResult(result: ResultRecord): string {
		const head = `${RESULT_MARKER(result.resultId)} ${result.agentId} ${result.status}${result.interrupted ? " (interrupted)" : ""}`;
		const body = result.status === "success"
			? JSON.stringify(result.data ?? result.text ?? null)
			: `error: ${result.error ?? "unknown"}${result.text ? `\nlast text: ${result.text}` : ""}`;
		return `${head}\n${body}`;
	}

	/** Move pending results of `parent`'s children to `delivering` and format them. */
	private deliverySeq = 0;

	private newDeliveryId(via: string): string {
		return `${via}-${Date.now().toString(36)}-${++this.deliverySeq}`;
	}

	private takeUndeliveredFor(parent: LiveAgent, via: "wait" | "steer" | "prompt", only?: Set<string>, deliveryId = this.newDeliveryId(via)): string[] {
		const out: string[] = [];
		for (const child of this.live.values()) {
			if (child.record.parentId !== parent.record.id) continue;
			if (only && !only.has(child.record.id)) continue;
			let changed = false;
			for (const result of child.record.results) {
				if (result.delivery.state !== "pending") continue;
				result.delivery = { state: "delivering", via, deliveryId, at: Date.now() };
				out.push(this.formatResult(result));
				changed = true;
			}
			if (changed) this.store.save(child.record);
		}
		return out;
	}

	/** Return results of one delivery attempt to `pending` (only that attempt). */
	private revertDelivering(parent: LiveAgent, deliveryId: string) {
		for (const child of this.live.values()) {
			if (child.record.parentId !== parent.record.id) continue;
			let changed = false;
			for (const result of child.record.results) {
				if (result.delivery.state === "delivering" && result.delivery.deliveryId === deliveryId) {
					result.delivery = { state: "pending" };
					changed = true;
				}
			}
			if (changed) this.store.save(child.record);
		}
		for (const waiter of parent.waiters) waiter();
	}

	/** A marker confirms delivery only in the result's parent transcript, for an in-flight delivery. */
	private markDelivered(resultId: string, seenBy: string) {
		const agentId = resultId.split("/")[0]!;
		const live = this.live.get(agentId);
		const result = live?.record.results.find(r => r.resultId === resultId);
		if (!live || !result || live.record.parentId !== seenBy || result.delivery.state !== "delivering") return;
		result.delivery = { ...result.delivery, state: "delivered", at: Date.now() };
		this.store.save(live.record);
	}

	// ---------------------------------------------------- authorization

	/** Read access: same root tree only (other roots are not loaded at all). */
	private authorizeRead(actor: Actor, target: LiveAgent): string | undefined {
		if (actor.kind === "human") return actor.rootId === this.rootId ? undefined : "agent belongs to another session tree";
		const caller = this.live.get(actor.agentId);
		if (!caller) return "unknown caller";
		return target.record.rootId === caller.record.rootId ? undefined : "agent belongs to another session tree";
	}

	/** Control (message, interrupt, cancel, resume, park): agents may address only direct children. */
	private authorizeControl(actor: Actor, target: LiveAgent): string | undefined {
		if (actor.kind === "human") return actor.rootId === this.rootId ? undefined : "agent belongs to another session tree";
		if (target.record.parentId !== actor.agentId) return `only the parent of ${target.record.id} may control it`;
		return undefined;
	}

	private lookup(actor: Actor, id: string, kind: "read" | "control"): LiveAgent | string {
		const live = this.live.get(id);
		if (!live) return `unknown agent ${id}`;
		const denied = kind === "read" ? this.authorizeRead(actor, live) : this.authorizeControl(actor, live);
		return denied ?? live;
	}

	// ------------------------------------------------------------ actions

	/**
	 * Spawn a child. Role, model and capabilities come from the child's
	 * definition only; the caller can choose among its own `spawns` targets
	 * and nothing else.
	 */
	spawn(callerId: string, args: { agent: string; task: string; foreground?: boolean }): { id: string; done: Promise<ResultRecord | undefined> } {
		if (this.shuttingDown) throw new RuntimeError("runtime is shutting down");
		const caller = this.live.get(callerId);
		if (!caller || TERMINAL.has(caller.record.status)) throw new RuntimeError("caller cannot spawn");
		if (!caller.record.contract.spawns.includes(args.agent)) {
			throw new RuntimeError(`${caller.record.role} may not spawn "${args.agent}" (allowed: ${caller.record.contract.spawns.join(", ") || "none"})`);
		}
		const depth = caller.record.depth + 1;
		if (depth > this.deps.config.scheduler.maxDepth) throw new RuntimeError(`maximum delegation depth ${this.deps.config.scheduler.maxDepth} reached`);
		if (typeof args.task !== "string" || args.task.trim() === "") throw new RuntimeError("task text is required");
		const def = this.deps.catalog.require(args.agent);
		const record = this.newRecord(def, caller.record, depth, caller.record.contract.cwd, false, reasoningHint(def, args.task));
		const live = this.adopt(record);
		this.store.save(record);
		this.emit({ type: "agent_spawned", agentId: record.id, parentId: callerId, role: def.name, task: args.task });
		const done = this.startRun(live, args.task, args.foreground ? "task" : undefined);
		done.catch(() => undefined);
		return { id: record.id, done };
	}

	async send(actor: Actor, id: string, message: string, mode: "steer" | "followup"): Promise<{ outcome: SendOutcome; reason?: string }> {
		const found = this.lookup(actor, id, "control");
		if (typeof found === "string") return { outcome: "rejected", reason: found };
		const live = found;
		if (TERMINAL.has(live.record.status)) return { outcome: "rejected", reason: `agent is ${live.record.status}` };
		if (!message.trim()) return { outcome: "rejected", reason: "empty message" };
		const from = actor.kind === "human" ? "human" : actor.agentId;
		this.emit({ type: "message", from, to: id, mode, message });
		if (mode === "steer") {
			if (live.run && live.session && live.record.status === "running") {
				await live.session.steer(`[steering from ${from}] ${message}`);
				return { outcome: "delivered" };
			}
			live.queuedSteers.push(`(from ${from}) ${message}`);
			return { outcome: "queued", reason: "agent is not running; the message is prepended to its next assignment" };
		}
		const text = `[follow-up from ${from}] ${message}`;
		if (live.record.parentId === null) {
			// The primary agent: a follow-up is an ordinary prompt.
			if (live.run) {
				live.pending.push({ text });
				return { outcome: "queued" };
			}
			void this.promptPrimary(text);
			return { outcome: "delivered" };
		}
		if (live.run) {
			live.pending.push({ text });
			return { outcome: "queued", reason: "agent is running; the follow-up starts when the current run settles" };
		}
		const wasParked = !live.session;
		this.startRun(live, text).catch(() => undefined);
		return { outcome: wasParked ? "revived" : "delivered" };
	}

	async interrupt(actor: Actor, id: string): Promise<{ ok: boolean; reason?: string }> {
		const found = this.lookup(actor, id, "control");
		if (typeof found === "string") return { ok: false, reason: found };
		const live = found;
		if (!live.run) return { ok: false, reason: `agent is ${live.record.status}, not running` };
		live.run.interrupted = true;
		live.pending.length = 0;
		live.run.controller.abort(new RuntimeError("interrupted"));
		if (live.session) await live.session.abort();
		await live.run?.done.catch(() => undefined);
		return { ok: true };
	}

	async cancel(actor: Actor, id: string): Promise<{ ok: boolean; cancelled: string[]; reason?: string }> {
		const found = this.lookup(actor, id, "control");
		if (typeof found === "string") return { ok: false, cancelled: [], reason: found };
		if (found.record.parentId === null) return { ok: false, cancelled: [], reason: "the primary agent cannot be cancelled; quit the session instead" };
		const cancelled: string[] = [];
		await this.cancelTree(found, cancelled);
		return { ok: true, cancelled };
	}

	private async cancelTree(live: LiveAgent, cancelled: string[]) {
		if (live.record.status === "cancelled") return;
		// Mark first so every late callback sees the terminal state.
		this.setStatus(live, "cancelled");
		cancelled.push(live.record.id);
		live.pending.length = 0;
		live.queuedSteers.length = 0;
		const run = live.run;
		if (run) {
			run.controller.abort(new RuntimeError("cancelled"));
			if (run.deliveryVia === "task" || live.record.parentId) {
				live.record.results.push(this.makeResult(live.record, run.runId, { status: "failure", error: "cancelled" }));
			}
		}
		live.run = null;
		const session = live.session;
		live.session = null;
		live.unsubscribe?.();
		if (session) await session.dispose().catch(() => undefined);
		this.store.save(live.record);
		this.wakeParent(live);
		for (const child of this.live.values()) {
			if (child.record.parentId === live.record.id) await this.cancelTree(child, cancelled);
		}
	}

	async resume(actor: Actor, id: string, message?: string): Promise<{ outcome: SendOutcome; reason?: string }> {
		const found = this.lookup(actor, id, "control");
		if (typeof found === "string") return { outcome: "rejected", reason: found };
		const live = found;
		if (live.record.status === "failed") return { outcome: "rejected", reason: live.record.failure ?? "agent failed" };
		if (live.record.status === "cancelled") return { outcome: "rejected", reason: "agent was cancelled (permanent)" };
		if (live.record.status !== "parked" && live.record.status !== "idle") return { outcome: "rejected", reason: `agent is ${live.record.status}` };
		if (message) return this.send(actor, id, message, "followup");
		const wasParked = !live.session;
		try {
			await this.ensureLive(live);
		} catch (error) {
			return { outcome: "rejected", reason: (error as Error).message };
		}
		if (!live.run && (live.record.status === "parked" || live.record.status === "idle")) this.setStatus(live, "idle");
		return { outcome: wasParked ? "revived" : "delivered" };
	}

	/** Foreground task: spawn and await the accepted result. */
	async runForeground(callerId: string, agent: string, task: string): Promise<ResultRecord | undefined> {
		return this.spawn(callerId, { agent, task, foreground: true }).done;
	}

	/**
	 * Wait for owned work (direct children only). Returns undelivered results
	 * immediately; otherwise blocks until a child result, a message, the abort
	 * signal, or the bound.
	 */
	async wait(callerId: string, opts: { ids?: string[]; timeoutMs: number; signal?: AbortSignal }): Promise<{ results: string[]; messages: string[]; status: { id: string; status: AgentStatus; activity?: string; outputs: number }[]; reason: string }> {
		const caller = this.live.get(callerId);
		if (!caller) throw new RuntimeError("unknown caller");
		const owned = [...this.live.values()].filter(l => l.record.parentId === callerId);
		const ownedIds = new Set(owned.map(l => l.record.id));
		for (const id of opts.ids ?? []) if (!ownedIds.has(id)) throw new RuntimeError(`${id} is not a child of ${callerId}`);
		const scope = opts.ids?.length ? new Set(opts.ids) : ownedIds;
		const snapshot = () => [...scope].map(id => {
			const r = this.live.get(id)!.record;
			return { id, status: r.status, activity: r.activity, outputs: r.outputs.length };
		});
		const take = () => ({ results: this.takeUndeliveredFor(caller, "wait", scope), messages: caller.inbox.splice(0) });
		let got = take();
		if (got.results.length || got.messages.length) return { ...got, status: snapshot(), reason: "ready" };
		const active = [...scope].some(id => {
			const s = this.live.get(id)!.record.status;
			return s === "queued" || s === "running";
		});
		if (!active) return { ...got, status: snapshot(), reason: "nothing to wait for: no owned child is queued or running" };
		const reason = await new Promise<string>(resolve => {
			let settled = false;
			const finish = (why: string) => {
				if (settled) return;
				settled = true;
				clearTimeout(timer);
				caller.waiters.delete(wake);
				opts.signal?.removeEventListener("abort", onAbort);
				resolve(why);
			};
			const wake = () => {
				const anyResult = [...scope].some(id => this.live.get(id)!.record.results.some(r => r.delivery.state === "pending"));
				const anyActive = [...scope].some(id => ["queued", "running"].includes(this.live.get(id)!.record.status));
				if (anyResult || caller.inbox.length) finish("ready");
				else if (!anyActive) finish("owned children stopped");
			};
			const onAbort = () => finish("cancelled");
			const timer = setTimeout(() => finish("timeout"), Math.max(1, opts.timeoutMs));
			caller.waiters.add(wake);
			opts.signal?.addEventListener("abort", onAbort, { once: true });
			if (opts.signal?.aborted) onAbort();
		});
		got = take();
		return { ...got, status: snapshot(), reason };
	}

	list(actor: Actor): AgentRecord[] {
		return [...this.live.values()].filter(l => !this.authorizeRead(actor, l)).map(l => l.record);
	}

	/** Transcript and accepted outputs, scoped to the session tree. */
	read(actor: Actor, id: string, opts: { messages?: number } = {}): { record: AgentRecord; transcript: { role: string; text: string }[] } | string {
		const found = this.lookup(actor, id, "read");
		if (typeof found === "string") return found;
		const transcript: { role: string; text: string }[] = [];
		const file = found.record.sessionFile;
		if (found.session) {
			for (const m of found.session.messages) transcript.push({ role: m.role, text: textOfContent(m.content) || (m.role === "assistant" ? this.describeCalls(m.content) : "") });
		} else if (file && existsSync(file)) {
			for (const line of readFileSync(file, "utf8").split("\n")) {
				if (!line.trim()) continue;
				try {
					const entry = JSON.parse(line) as { type?: string; message?: AgentMessageLike };
					if (entry.type === "message" && entry.message) {
						transcript.push({ role: entry.message.role, text: textOfContent(entry.message.content) || this.describeCalls(entry.message.content) });
					}
				} catch {}
			}
		}
		const limit = opts.messages ?? 40;
		return { record: found.record, transcript: transcript.slice(-limit) };
	}

	private describeCalls(content: unknown): string {
		if (!Array.isArray(content)) return "";
		return content
			.filter(b => b && typeof b === "object" && (b as { type?: string }).type === "toolCall")
			.map(b => `→ ${(b as { name?: string }).name}(${JSON.stringify((b as { arguments?: unknown }).arguments ?? {}).slice(0, 200)})`)
			.join("\n");
	}

	// --------------------------------------------------------------- yield

	/** Called by the `yield` tool. Validates; acceptance happens at settlement. */
	yieldResult(agentId: string, toolCallId: string, input: { final?: boolean; status?: "success" | "failure"; label?: string; data?: unknown; summary?: string; error?: string }): ToolOutput {
		const live = this.live.get(agentId);
		const run = live?.run;
		if (!live || !run || !this.isCurrent(live, run)) return { text: "No active assignment to yield for.", isError: true };
		const final = input.final !== false;
		if (!final) {
			const output: IncrementalOutput = { runId: run.runId, seq: live.record.outputs.length + 1, label: input.label ?? "progress", data: input.data ?? input.summary, at: Date.now() };
			live.record.outputs.push(output);
			this.store.save(live.record);
			this.emit({ type: "agent_output", agentId, output });
			return { text: `Incremental output ${output.seq} recorded.` };
		}
		const reject = (text: string): ToolOutput => {
			run.rejectedYields += 1;
			if (run.rejectedYields >= MAX_REJECTED_YIELDS) {
				run.terminal = { status: "failure", error: `gave up after ${run.rejectedYields} rejected yields: ${text}` };
				return { text: `${text}\nToo many rejected yields; the assignment is recorded as failed. Stop now.`, isError: true, terminate: true };
			}
			return { text, isError: true };
		};
		if (run.batch.length > 1 || (run.batch.length === 1 && run.batch[0]!.id !== toolCallId)) {
			return reject("Rejected: a terminal yield must be the only tool call in its message. Review the other tool results, then call yield alone.");
		}
		const status = input.status ?? (input.error ? "failure" : "success");
		if (status === "success" && live.record.contract.outputSchema) {
			const schema = live.record.contract.outputSchema;
			if (!Value.Check(schema as never, input.data)) {
				const errors = [...Value.Errors(schema as never, input.data)].slice(0, 5).map(e => `${(e as { instancePath?: string }).instancePath ?? ""} ${(e as { message: string }).message}`);
				return reject(`Rejected: data does not match the output schema: ${errors.join("; ")}`);
			}
		}
		run.terminal = { status, data: input.data, text: input.summary, error: status === "failure" ? input.error ?? "failure reported" : undefined };
		return { text: "Result recorded. Stop now.", terminate: true };
	}

	// --------------------------------------------------- primary agent

	/** Run one user turn on the primary agent. */
	async promptPrimary(text: string): Promise<void> {
		const primary = this.primary()!;
		if (primary.run) {
			primary.pending.push({ text });
			return;
		}
		await this.startRun(primary, text);
	}

	async steerPrimary(text: string): Promise<SendOutcome> {
		return (await this.send({ kind: "human", rootId: this.rootId }, this.primaryId, text, "steer")).outcome;
	}

	/** Wait until no run is queued or in flight anywhere in the tree. */
	async quiesce(timeoutMs = 60_000): Promise<boolean> {
		const deadline = Date.now() + timeoutMs;
		while (Date.now() < deadline) {
			const busy = [...this.live.values()].filter(l => l.run || l.revival || l.parking);
			if (busy.length === 0) return true;
			await Promise.race([
				...busy.map(l => l.run?.done ?? l.revival ?? l.parking ?? Promise.resolve()),
				new Promise(r => setTimeout(r, 50)),
			]).catch(() => undefined);
		}
		return false;
	}

	addCompression(agentId: string, toolCallIds: string[], summary: string): string | undefined {
		const live = this.live.get(agentId);
		if (!live) return "unknown agent";
		const known = new Set<string>();
		for (const m of live.session?.messages ?? []) {
			const id = (m as { toolCallId?: string }).toolCallId;
			if (m.role === "toolResult" && id) known.add(id);
		}
		for (const id of toolCallIds) if (!known.has(id)) return `unknown tool call id ${id}`;
		for (const id of toolCallIds) live.record.compressions[id] = summary;
		this.store.save(live.record);
		return undefined;
	}

	recordJob(agentId: string, job: AgentRecord["jobs"][number]) {
		const live = this.live.get(agentId);
		if (!live) return;
		if (!live.record.jobs.some(j => j.jobId === job.jobId)) live.record.jobs.push(job);
		this.store.save(live.record);
	}

	/**
	 * Dispose every owned session; never process-wide teardown. In-flight runs
	 * are interrupted (recorded as interrupted failures, not cancelled) and
	 * allowed to settle before their sessions are disposed.
	 */
	async shutdown(): Promise<void> {
		if (this.shuttingDown) return;
		this.shuttingDown = true;
		const runs: Promise<unknown>[] = [];
		for (const live of this.live.values()) {
			live.pending.length = 0;
			if (live.run) {
				live.run.interrupted = true;
				live.run.controller.abort(new RuntimeError("shutdown"));
				if (live.session) runs.push(live.session.abort().catch(() => undefined));
				runs.push(live.run.done.catch(() => undefined));
			}
		}
		await Promise.allSettled(runs);
		const disposals: Promise<void>[] = [];
		for (const live of this.live.values()) {
			if (live.revival) disposals.push(live.revival.then(s => s.dispose(), () => undefined));
			if (live.parking) disposals.push(live.parking.catch(() => undefined));
			const session = live.session;
			if (session) {
				live.session = null;
				live.unsubscribe?.();
				live.unsubscribe = undefined;
				disposals.push(session.dispose());
			}
		}
		await Promise.allSettled(disposals);
		for (const live of this.live.values()) {
			if (live.record.status === "running" || live.record.status === "queued") {
				live.record.interruptedRunId = live.record.currentRunId;
				live.record.status = "parked";
			} else if (live.record.status === "idle") {
				live.record.status = "parked";
			}
			this.store.save(live.record);
		}
		this.store.unlock();
	}

	formatForDelivery(result: ResultRecord): string {
		return this.formatResult(result);
	}

	/** Diagnostic counts for tests: live sessions and permits in flight. */
	diagnostics(): { liveSessions: number; permitsInFlight: number; waiting: number } {
		let liveSessions = 0;
		let permitsInFlight = 0;
		let waiting = 0;
		for (const live of this.live.values()) {
			if (live.session) liveSessions++;
			permitsInFlight += live.permits.inFlight;
			waiting += live.permits.waiting;
		}
		return { liveSessions, permitsInFlight, waiting };
	}
}
