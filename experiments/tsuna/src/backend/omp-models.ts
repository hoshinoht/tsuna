/**
 * OMP model layer adapter.
 *
 * Streams requests through Oh My Pi's provider implementation
 * (`@oh-my-pi/pi-ai@18.8.6`, MIT; Mario Zechner, Can Bölük, Stencil Labs)
 * and validates model entries against OMP's bundled catalog
 * (`@oh-my-pi/pi-catalog@18.8.6`). Pi sessions stay the backend: the adapter
 * is registered on Pi's ModelRuntime as a provider whose `streamSimple`
 * converts Pi's transcript context to OMP's `Context`, and OMP's assistant
 * events back to Pi's.
 *
 * Isolation:
 *  - credentials come only from the environment variable named in Tsuna's
 *    provider config and are passed explicitly per request; OMP's auth
 *    storage (~/.omp agent.db), OAuth flows and env-key fallbacks are never
 *    used (a request without the configured key fails before dispatch);
 *  - OMP's logger defaults to a rotating file under ~/.omp/logs: Tsuna turns
 *    the file transport off and forwards warnings/errors as runtime events;
 *  - provider in-flight leases (files) are only used when limits are
 *    configured; Tsuna never configures them.
 */
import {
	createAssistantMessageEventStream,
	getCurrentSystemPrompt,
	getCurrentTools,
	type AssistantMessageEventStream,
	type Model as PiModel,
} from "@earendil-works/pi-ai";

type OmpAi = typeof import("@oh-my-pi/pi-ai");
type OmpCatalog = typeof import("@oh-my-pi/pi-catalog");
type OmpModel = ReturnType<OmpCatalog["getBundledModel"]>;
type AnyMessage = { role: string; [key: string]: unknown };

export interface OmpProviderConfig {
	type: "omp";
	/** OMP catalog provider id, e.g. `anthropic`, `openai`, `deepseek`. */
	ompProvider: string;
	/** Environment variable holding the API key (never the key itself). */
	apiKeyEnv?: string;
	/** Endpoint override (e.g. a local gateway such as CLIProxyAPI). */
	baseUrl?: string;
	headers?: Record<string, string>;
}

export const PI_LEVELS = ["minimal", "low", "medium", "high", "xhigh", "max"] as const;

let modules: Promise<{ ai: OmpAi; catalog: OmpCatalog }> | undefined;
const logListeners = new Set<(level: string, message: string) => void>();

/** Load OMP's model layer once per process with file logging disabled. */
export function loadOmpModelLayer(): Promise<{ ai: OmpAi; catalog: OmpCatalog }> {
	modules ??= (async () => {
		const utils = await import("@oh-my-pi/pi-utils");
		// No rotating log file under ~/.omp/logs; Tsuna forwards warnings instead.
		utils.logger.setTransports({ console: false, file: false });
		utils.logger.registerLogSink(event => {
			if (event.level === "warn" || event.level === "error") for (const l of logListeners) l(event.level, event.message);
		});
		const [ai, catalog] = await Promise.all([import("@oh-my-pi/pi-ai"), import("@oh-my-pi/pi-catalog")]);
		return { ai, catalog };
	})();
	return modules;
}

export function onOmpLog(listener: (level: string, message: string) => void): () => void {
	logListeners.add(listener);
	return () => logListeners.delete(listener);
}

export class OmpModelError extends Error {}

export interface ResolvedOmpModel {
	model: NonNullable<OmpModel>;
	contextWindow: number;
	maxTokens: number;
	reasoning: boolean;
	efforts: string[];
	input: ("text" | "image")[];
}

/**
 * Resolve a catalog model. Context window / output limits may be narrowed by
 * config but never raised above the catalog; reasoning levels must be a
 * subset of the catalog's supported efforts.
 */
export function resolveOmpModel(
	catalog: OmpCatalog,
	provider: OmpProviderConfig,
	id: string,
	entry: { contextWindow?: number; maxTokens?: number; reasoningLevels?: string[] },
): ResolvedOmpModel {
	if (!catalog.isGeneratedProvider(provider.ompProvider)) throw new OmpModelError(`OMP catalog has no provider "${provider.ompProvider}"`);
	const base = catalog.getBundledModel(provider.ompProvider as never, id);
	if (!base) throw new OmpModelError(`OMP catalog has no model ${provider.ompProvider}/${id}`);
	const catalogWindow = base.contextWindow ?? 0;
	const catalogMax = base.maxTokens ?? 0;
	if (entry.contextWindow !== undefined && catalogWindow && entry.contextWindow > catalogWindow) {
		throw new OmpModelError(`${provider.ompProvider}/${id}: contextWindow ${entry.contextWindow} exceeds the catalog's ${catalogWindow}`);
	}
	if (entry.maxTokens !== undefined && catalogMax && entry.maxTokens > catalogMax) {
		throw new OmpModelError(`${provider.ompProvider}/${id}: maxTokens ${entry.maxTokens} exceeds the catalog's ${catalogMax}`);
	}
	const efforts = [...catalog.getSupportedEfforts(base)] as string[];
	for (const level of entry.reasoningLevels ?? []) {
		if (!efforts.includes(level)) {
			throw new OmpModelError(`${provider.ompProvider}/${id}: reasoning level ${level} is not supported (catalog: ${efforts.join(", ") || "none"})`);
		}
	}
	const model = {
		...base,
		...(provider.baseUrl ? { baseUrl: provider.baseUrl } : {}),
		...(provider.headers ? { headers: { ...(base as { headers?: Record<string, string> }).headers, ...provider.headers } } : {}),
	} as NonNullable<OmpModel>;
	return {
		model,
		contextWindow: entry.contextWindow ?? catalogWindow,
		maxTokens: entry.maxTokens ?? catalogMax,
		reasoning: base.reasoning === true && efforts.length > 0,
		efforts: entry.reasoningLevels ?? efforts,
		input: ((base.input ?? ["text"]) as string[]).filter((i): i is "text" | "image" => i === "text" || i === "image"),
	};
}

/** Pi transcript context -> OMP Context. Pure; exported for tests. */
export function toOmpContext(messages: readonly AnyMessage[], ompModel: { api: string; provider: string }, piProvider: string) {
	const systemPrompt = getCurrentSystemPrompt(messages as never);
	const tools = getCurrentTools(messages as never).map(t => ({ name: t.name, description: t.description, parameters: t.parameters }));
	const out: AnyMessage[] = [];
	for (const m of messages) {
		if (m.role === "system") continue;
		if (m.role === "assistant" && m.provider === piProvider) {
			// Our own earlier turns: restore OMP's api/provider identity so OMP can
			// replay provider-native state (thinking signatures, response ids).
			out.push({ ...m, api: ompModel.api, provider: ompModel.provider });
		} else out.push(m);
	}
	return { systemPrompt: systemPrompt ? [systemPrompt] : [], messages: out, tools };
}

/** OMP assistant message -> Pi assistant message under the Tsuna provider identity. */
export function toPiAssistant(message: AnyMessage, piModel: Pick<PiModel<string>, "api" | "provider" | "id">): AnyMessage {
	return { ...message, api: piModel.api, provider: piModel.provider, model: piModel.id, upstreamApi: message.api, upstreamProvider: message.provider };
}

function errorStream(piModel: PiModel<string>, text: string): AssistantMessageEventStream {
	const stream = createAssistantMessageEventStream();
	const message = {
		role: "assistant",
		content: [],
		api: piModel.api,
		provider: piModel.provider,
		model: piModel.id,
		usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
		stopReason: "error",
		errorMessage: text,
		timestamp: Date.now(),
	};
	queueMicrotask(() => {
		stream.push({ type: "error", reason: "error", error: message } as never);
		stream.end(message as never);
	});
	return stream;
}

/**
 * Build the Pi `streamSimple` for one Tsuna provider backed by OMP.
 * `models` maps Tsuna model ids (catalog ids) to resolved OMP models.
 */
export function ompStreamSimple(
	ai: OmpAi,
	piProvider: string,
	config: OmpProviderConfig,
	models: ReadonlyMap<string, ResolvedOmpModel>,
	env: NodeJS.ProcessEnv,
) {
	return (piModel: PiModel<string>, context: { messages: AnyMessage[] }, options?: { signal?: AbortSignal; reasoning?: string; maxTokens?: number; temperature?: number }): AssistantMessageEventStream => {
		const resolved = models.get(piModel.id);
		if (!resolved) return errorStream(piModel, `model ${piModel.id} is not configured for provider ${piProvider}`);
		const apiKey = config.apiKeyEnv ? env[config.apiKeyEnv] : undefined;
		if (!apiKey) {
			// Never fall back to OMP's auth storage or its own env-variable map.
			return errorStream(piModel, `missing API key: set ${config.apiKeyEnv ?? "apiKeyEnv in the provider config"}`);
		}
		let reasoning: string | undefined;
		let disableReasoning = false;
		if (!resolved.reasoning || options?.reasoning === undefined) disableReasoning = resolved.reasoning;
		else if (!resolved.efforts.includes(options.reasoning)) {
			return errorStream(piModel, `reasoning level ${options.reasoning} is not supported by ${config.ompProvider}/${piModel.id} (${resolved.efforts.join(", ")})`);
		} else reasoning = options.reasoning;
		const ompContext = toOmpContext(context.messages, resolved.model as { api: string; provider: string }, piProvider);
		const out = createAssistantMessageEventStream();
		void (async () => {
			try {
				const upstream = ai.streamSimple(resolved.model as never, ompContext as never, {
					apiKey,
					signal: options?.signal,
					maxTokens: options?.maxTokens,
					temperature: options?.temperature,
					reasoning: reasoning as never,
					disableReasoning,
				} as never);
				for await (const event of upstream as AsyncIterable<AnyMessage & { type: string }>) {
					const e: Record<string, unknown> = { ...event };
					if (e.partial) e.partial = toPiAssistant(e.partial as AnyMessage, piModel);
					if (e.message) e.message = toPiAssistant(e.message as AnyMessage, piModel);
					if (e.error) e.error = toPiAssistant(e.error as AnyMessage, piModel);
					out.push(e as never);
					if (e.type === "done") out.end(e.message as never);
					else if (e.type === "error") out.end(e.error as never);
				}
			} catch (error) {
				const failed = errorStream(piModel, `OMP provider error: ${(error as Error).message}`);
				for await (const e of failed) out.push(e);
				out.end(await failed.result());
			}
		})();
		return out;
	};
}

/** Pi `thinkingLevelMap` for a resolved OMP model: unsupported levels map to null. */
export function thinkingLevelMap(resolved: ResolvedOmpModel): Record<string, string | null> {
	const map: Record<string, string | null> = {};
	for (const level of PI_LEVELS) map[level] = resolved.reasoning && resolved.efforts.includes(level) ? level : null;
	return map;
}
