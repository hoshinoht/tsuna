/**
 * Pi SDK session backend (adapter boundary).
 *
 * The rest of the harness talks to `BackendSession`/`ModelHub` only. Pi is
 * configured so that it discovers nothing on its own: no extensions, skills,
 * prompt templates, themes or context files; an in-memory settings manager
 * and credential store; an agentDir and session directory inside Tsuna state.
 * Settlement follows Pi's `agent_settled` contract (`prompt()` resolves after
 * retries, compaction and queued continuations have finished).
 */
import { mkdirSync } from "node:fs";
import {
	createFauxCore,
	fauxAssistantMessage,
	fauxText,
	fauxThinking,
	fauxToolCall,
	type AssistantMessage,
	type Model,
} from "@earendil-works/pi-ai";
import {
	createAgentSession,
	DefaultResourceLoader,
	ModelRuntime,
	SessionManager,
	SettingsManager,
	type AgentSession,
	type AgentSessionEvent,
	type ToolDefinition,
} from "@earendil-works/pi-coding-agent";
import type { ReasoningLevel } from "../agents/definitions.ts";
import type { ModelEntry, ProvidersConfig } from "../config.ts";
import type { FixtureReply, FixtureScript, FixtureTurn } from "./fixture-types.ts";

export type { AgentSessionEvent, ToolDefinition };
export type AgentMessageLike = { role: string; content?: unknown; [key: string]: unknown };

export class BackendError extends Error {}

/** In-memory credential store: Tsuna never reads Pi's auth.json. */
function memoryCredentials() {
	const store = new Map<string, unknown>();
	return {
		read: async (id: string) => store.get(id) as never,
		list: async () => [] as never[],
		modify: async (id: string, fn: (current: never) => Promise<never>) => {
			const next = await fn(store.get(id) as never);
			if (next !== undefined) store.set(id, next);
			return next;
		},
		delete: async (id: string) => {
			store.delete(id);
		},
	};
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
	return new Promise(resolve => {
		if (signal?.aborted) return resolve();
		const timer = setTimeout(done, ms);
		function done() {
			clearTimeout(timer);
			signal?.removeEventListener("abort", done);
			resolve();
		}
		signal?.addEventListener("abort", done, { once: true });
	});
}

const IDENTITY = /^Tsuna-Agent-Id: (\S+)\nTsuna-Role: (\S+)/m;

function identityOf(messages: AgentMessageLike[]): { agentId: string; role: string } {
	const system = messages.find(m => m.role === "system") as { sections?: Record<string, string>; content?: unknown } | undefined;
	const text = system?.sections ? Object.values(system.sections).join("\n") : String(system?.content ?? "");
	const match = IDENTITY.exec(text);
	return { agentId: match?.[1] ?? "unknown", role: match?.[2] ?? "unknown" };
}

function textOf(content: unknown): string {
	if (typeof content === "string") return content;
	if (Array.isArray(content)) {
		return content
			.map(block => (block && typeof block === "object" && (block as { type?: string }).type === "text" ? String((block as { text?: string }).text ?? "") : ""))
			.join("");
	}
	return "";
}

export function fixtureTurn(messages: AgentMessageLike[], signal?: AbortSignal): FixtureTurn {
	const { agentId, role } = identityOf(messages);
	let lastUserText: string | undefined;
	for (let i = messages.length - 1; i >= 0; i--) {
		if (messages[i]!.role === "user") {
			lastUserText = textOf(messages[i]!.content);
			break;
		}
	}
	const lastToolResults: FixtureTurn["lastToolResults"] = [];
	for (let i = messages.length - 1; i >= 0 && messages[i]!.role === "toolResult"; i--) {
		const m = messages[i] as { toolName?: string; content?: unknown; isError?: boolean; details?: unknown };
		lastToolResults.unshift({ name: String(m.toolName ?? ""), text: textOf(m.content), isError: m.isError === true, details: m.details });
	}
	const assistantCount = messages.filter(m => m.role === "assistant").length;
	const userTexts = messages.filter(m => m.role === "user").map(m => textOf(m.content));
	return { agentId, role, messages, lastUserText, userTexts, lastToolResults, assistantCount, signal };
}

function replyToMessage(reply: FixtureReply, turn: FixtureTurn): AssistantMessage {
	if (reply.error) return fauxAssistantMessage(reply.error, { stopReason: "error", errorMessage: reply.error });
	const blocks = [];
	if (reply.thinking) blocks.push(fauxThinking(reply.thinking));
	if (reply.text) blocks.push(fauxText(reply.text));
	(reply.toolCalls ?? []).forEach((call, index) => {
		blocks.push(fauxToolCall(call.name, call.arguments as never, { id: call.id ?? `call_${turn.agentId}_${turn.assistantCount}_${index}` }));
	});
	if (blocks.length === 0) blocks.push(fauxText(""));
	return fauxAssistantMessage(blocks, { stopReason: reply.toolCalls?.length ? "toolUse" : "stop" });
}

export interface ResolvedModel {
	entry: string;
	provider: string;
	id: string;
	model: Model<string>;
	config: ModelEntry;
}

/**
 * Registers configured providers on one Pi `ModelRuntime` (no auth.json,
 * no models.json, no network model refresh).
 */
export class ModelHub {
	private constructor(
		readonly runtime: ModelRuntime,
		private readonly config: ProvidersConfig,
	) {}

	static async create(config: ProvidersConfig, env: NodeJS.ProcessEnv = process.env): Promise<ModelHub> {
		const runtime = await ModelRuntime.create({ credentials: memoryCredentials(), modelsPath: null, allowModelNetwork: false } as never);
		const hub = new ModelHub(runtime, config);
		for (const [name, provider] of Object.entries(config.providers)) {
			const models = Object.values(config.models)
				.filter(m => m.provider === name)
				.map(m => ({
					id: m.id,
					name: m.id,
					reasoning: m.reasoning,
					input: m.input,
					cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
					contextWindow: m.contextWindow,
					maxTokens: m.maxTokens,
				}));
			if (provider.type === "fixture") {
				const mod = (await import(provider.script)) as { default?: FixtureScript };
				const script = mod.default;
				if (typeof script !== "function") throw new BackendError(`fixture script ${provider.script} has no default export`);
				const api = `tsuna-fixture-${name}`;
				const core = createFauxCore({ provider: name, api, models: models.map(m => ({ ...m, input: [...m.input] })), tokenSize: { min: 64, max: 64 } });
				const dispatch = async (context: { messages: AgentMessageLike[] }, options?: { signal?: AbortSignal }) => {
					const turn = fixtureTurn(context.messages, options?.signal);
					const reply = await script(turn);
					if (reply.delayMs) await sleep(reply.delayMs, options?.signal);
					return replyToMessage(reply, turn);
				};
				runtime.registerProvider(name, {
					api,
					baseUrl: "fixture://local",
					apiKey: "fixture",
					models,
					// Every queued step is the same dispatcher, so concurrent agents
					// cannot receive each other's scripted turns.
					streamSimple: (model: Model<string>, context: never, options: never) => {
						core.appendResponses([dispatch as never]);
						return core.streamSimple(model, context, options);
					},
				} as never);
			} else {
				const apiKey = provider.apiKeyEnv ? env[provider.apiKeyEnv] : undefined;
				runtime.registerProvider(name, {
					api: provider.api,
					baseUrl: provider.baseUrl,
					apiKey: apiKey ?? "unset",
					headers: provider.headers,
					models,
				} as never);
			}
		}
		return hub;
	}

	/** Resolve a logical model entry; throws for unknown entries or unregistered models. */
	resolve(entryName: string): ResolvedModel {
		const entry = this.config.models[entryName];
		if (!entry) throw new BackendError(`unknown model entry "${entryName}"`);
		const model = this.runtime.getModel(entry.provider, entry.id);
		if (!model) throw new BackendError(`model ${entry.provider}/${entry.id} is not registered`);
		return { entry: entryName, provider: entry.provider, id: entry.id, model: model as Model<string>, config: entry };
	}

	/** Validate a reasoning level against the configured backend capability. */
	checkReasoning(resolved: ResolvedModel, level: ReasoningLevel): ReasoningLevel {
		if (!resolved.config.reasoning) return "off";
		const supported = resolved.config.reasoningLevels;
		if (supported && !supported.includes(level)) {
			throw new BackendError(`model entry ${resolved.entry} does not support reasoning level ${level}`);
		}
		return level;
	}
}

export interface BackendSessionSpec {
	hub: ModelHub;
	cwd: string;
	/** Directory for Pi's JSONL files (inside Tsuna state). */
	sessionDir: string;
	/** Pi agentDir placeholder inside Tsuna state; nothing should be read from it. */
	piAgentDir: string;
	/** Resume this transcript instead of starting a new one. */
	sessionFile?: string;
	systemPrompt: string;
	model: ResolvedModel;
	thinking: ReasoningLevel;
	tools: ToolDefinition[];
	transformContext?: (messages: AgentMessageLike[]) => AgentMessageLike[];
}

export interface BackendSession {
	readonly sessionFile: string | undefined;
	readonly isStreaming: boolean;
	readonly messages: readonly AgentMessageLike[];
	prompt(text: string): Promise<void>;
	steer(text: string): Promise<"handled" | "queued">;
	abort(): Promise<void>;
	subscribe(listener: (event: AgentSessionEvent) => void): () => void;
	lastAssistantText(): string | undefined;
	dispose(): Promise<void>;
}

class PiSession implements BackendSession {
	private disposed = false;
	constructor(private readonly session: AgentSession) {}
	get sessionFile() {
		return this.session.sessionFile;
	}
	get isStreaming() {
		return this.session.isStreaming;
	}
	get messages() {
		return this.session.messages as unknown as AgentMessageLike[];
	}
	prompt(text: string) {
		return this.session.prompt(text);
	}
	steer(text: string) {
		return this.session.steer(text);
	}
	abort() {
		return this.session.abort();
	}
	subscribe(listener: (event: AgentSessionEvent) => void) {
		return this.session.subscribe(listener);
	}
	lastAssistantText() {
		return this.session.getLastAssistantText();
	}
	async dispose() {
		if (this.disposed) return;
		this.disposed = true;
		// Pi's dispose aborts in-flight work but does not wait for idleness.
		if (!this.session.isIdle) {
			try {
				await this.session.abort();
			} catch {}
		}
		this.session.dispose();
	}
}

export async function createBackendSession(spec: BackendSessionSpec): Promise<BackendSession> {
	mkdirSync(spec.sessionDir, { recursive: true, mode: 0o700 });
	mkdirSync(spec.piAgentDir, { recursive: true, mode: 0o700 });
	const transform = spec.transformContext;
	const loader = new DefaultResourceLoader({
		cwd: spec.cwd,
		agentDir: spec.piAgentDir,
		noExtensions: true,
		noSkills: true,
		noPromptTemplates: true,
		noThemes: true,
		noContextFiles: true,
		systemPromptOverride: () => spec.systemPrompt,
		appendSystemPromptOverride: () => [],
		extensionFactories: transform
			? [
					{
						name: "tsuna-context",
						factory: (pi: { on: (event: string, handler: (e: { messages: AgentMessageLike[] }) => unknown) => void }) => {
							pi.on("context", event => ({ messages: transform(event.messages) }));
						},
					} as never,
				]
			: [],
	});
	await loader.reload();
	const settingsManager = SettingsManager.inMemory({
		compaction: { enabled: false },
		retry: { enabled: false },
		cacheWarming: "off",
	} as never);
	const sessionManager = spec.sessionFile
		? SessionManager.open(spec.sessionFile, spec.sessionDir)
		: SessionManager.create(spec.cwd, spec.sessionDir);
	const { session } = await createAgentSession({
		cwd: spec.cwd,
		agentDir: spec.piAgentDir,
		modelRuntime: spec.hub.runtime,
		model: spec.model.model,
		thinkingLevel: spec.thinking,
		tools: spec.tools.map(t => t.name),
		customTools: spec.tools,
		resourceLoader: loader,
		settingsManager,
		sessionManager,
	} as never);
	return new PiSession(session);
}
