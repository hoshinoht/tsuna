/**
 * Runtime policies shared by the OMP extension and its tests.
 *
 * This is an independent adaptation of the existing Tsuna policies for OMP's
 * public context hook.  It intentionally works on structural message values:
 * the extension receives a context copy, while the persisted session stays
 * unchanged.
 */

export const IMAGE_BUDGET_DEFAULTS = {
	maxImages: 12,
	maxImageBytes: 6 * 1024 * 1024,
	pruneTo: 0.5,
	providers: {} as Record<string, Partial<ImageBudget>>,
};

export interface ImageBudget {
	maxImages: number;
	maxImageBytes: number;
}

export interface ImageBudgetOptions extends ImageBudget {
	pruneTo: number;
	providers: Record<string, Partial<ImageBudget>>;
}

export interface CacheGuardOptions {
	enabled: boolean;
	riskAfterMinutes: number;
	minCacheReadTokens: number;
	providerIDs: string[];
	diagnosticsLimit: number;
}

export interface AgentPolicy {
	def: ReasoningEffort;
	min: ReasoningEffort;
	max: ReasoningEffort;
}

export type ReasoningEffort = "low" | "medium" | "high" | "xhigh" | "max";
export type ReasoningClass = "auto" | "fast" | "balanced" | "deep";

export interface ProviderRule {
	/** Retained for compatibility with the source options; OMP owns wire mapping. */
	option?: string;
	efforts: ReasoningEffort[];
}

export interface ReasoningOptions {
	diagnosticsLimit: number;
	agentPolicy: Record<string, AgentPolicy>;
	providerAgentPolicy: Record<string, Record<string, AgentPolicy>>;
	classBase: Record<Exclude<ReasoningClass, "auto">, ReasoningEffort>;
	supportedEfforts: ReasoningEffort[];
	providers: Record<string, ProviderRule>;
}

export interface RuntimeOptions {
	imageBudget: ImageBudgetOptions;
	cacheGuard: CacheGuardOptions;
	reasoningRouter: ReasoningOptions;
}

const KiB = 1024;
const MiB = 1024 * KiB;
const BYTES_RE = /^(\d+(?:\.\d+)?)\s*(b|kb|kib|mb|mib)?$/i;
const EFFORTS: ReasoningEffort[] = ["low", "medium", "high", "xhigh", "max"];
const REASONING_CLASSES: ReasoningClass[] = ["auto", "fast", "balanced", "deep"];
const IGNORED_AGENTS = new Set(["build", "orchestrator", "general", "compaction", "title", "summary"]);

const DEFAULT_AGENT_POLICY: Record<string, AgentPolicy> = {
	explore: { def: "medium", min: "low", max: "high" },
	tester: { def: "low", min: "low", max: "medium" },
	"code-writer": { def: "xhigh", min: "xhigh", max: "xhigh" },
	"code-engineer": { def: "medium", min: "medium", max: "high" },
	"frontend-engineer": { def: "medium", min: "medium", max: "high" },
	researcher: { def: "medium", min: "low", max: "medium" },
	"document-writer": { def: "medium", min: "low", max: "medium" },
	"document-proofreader": { def: "medium", min: "low", max: "medium" },
	plan: { def: "high", min: "medium", max: "high" },
	"plan-checker": { def: "high", min: "medium", max: "high" },
	"code-checker": { def: "medium", min: "medium", max: "high" },
	oracle: { def: "low", min: "low", max: "low" },
};

const DEFAULT_CACHE_GUARD: CacheGuardOptions = {
	enabled: true,
	riskAfterMinutes: 30,
	minCacheReadTokens: 10_000,
	providerIDs: ["openai"],
	diagnosticsLimit: 20,
};

const DEFAULT_REASONING: ReasoningOptions = {
	diagnosticsLimit: 100,
	agentPolicy: DEFAULT_AGENT_POLICY,
	providerAgentPolicy: {},
	classBase: { fast: "low", balanced: "medium", deep: "high" },
	supportedEfforts: [...EFFORTS],
	providers: { openai: { option: "reasoningEffort", efforts: [...EFFORTS] } },
};

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function positiveInteger(value: unknown, name: string, minimum: number): number {
	if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) {
		throw new Error(`[runtime] ${name} must be an integer >= ${minimum}`);
	}
	return value;
}

function parseBytes(value: unknown, name: string): number {
	if (typeof value === "number" && Number.isSafeInteger(value) && value > 0) return value;
	if (typeof value === "string") {
		const match = BYTES_RE.exec(value.trim());
		if (match) {
			const unit = (match[2] ?? "b").toLowerCase();
			const multiplier = unit.startsWith("m") ? MiB : unit.startsWith("k") ? KiB : 1;
			const result = Math.floor(Number(match[1]) * multiplier);
			if (result > 0) return result;
		}
	}
	throw new Error(`[runtime] ${name} must be a positive byte count`);
}

function parseImageBudget(raw: unknown): ImageBudgetOptions {
	if (raw === undefined || raw === null) return { ...IMAGE_BUDGET_DEFAULTS, providers: {} };
	if (!isRecord(raw)) throw new Error("[runtime] imageBudget must be an object");
	const permitted = new Set(["maxImages", "maxImageBytes", "pruneTo", "providers"]);
	for (const key of Object.keys(raw)) if (!permitted.has(key)) throw new Error(`[runtime] imageBudget has unknown option '${key}'`);
	const maxImages = raw.maxImages === undefined ? IMAGE_BUDGET_DEFAULTS.maxImages : positiveInteger(raw.maxImages, "imageBudget.maxImages", 1);
	const maxImageBytes = raw.maxImageBytes === undefined ? IMAGE_BUDGET_DEFAULTS.maxImageBytes : parseBytes(raw.maxImageBytes, "imageBudget.maxImageBytes");
	const pruneTo = raw.pruneTo === undefined ? IMAGE_BUDGET_DEFAULTS.pruneTo : raw.pruneTo;
	if (typeof pruneTo !== "number" || !(pruneTo > 0 && pruneTo <= 1)) throw new Error("[runtime] imageBudget.pruneTo must be in (0, 1]");
	const providers: Record<string, Partial<ImageBudget>> = {};
	if (raw.providers !== undefined) {
		if (!isRecord(raw.providers)) throw new Error("[runtime] imageBudget.providers must be an object");
		for (const [provider, override] of Object.entries(raw.providers)) {
			if (!isRecord(override)) throw new Error(`[runtime] imageBudget.providers.${provider} must be an object`);
			const invalid = Object.keys(override).filter(key => key !== "maxImages" && key !== "maxImageBytes");
			if (invalid.length) throw new Error(`[runtime] imageBudget.providers.${provider} has unknown option '${invalid[0]}'`);
			providers[provider] = {
				...(override.maxImages === undefined ? {} : { maxImages: positiveInteger(override.maxImages, `imageBudget.providers.${provider}.maxImages`, 1) }),
				...(override.maxImageBytes === undefined ? {} : { maxImageBytes: parseBytes(override.maxImageBytes, `imageBudget.providers.${provider}.maxImageBytes`) }),
			};
		}
	}
	return { maxImages, maxImageBytes, pruneTo, providers };
}

function parseCacheGuard(raw: unknown): CacheGuardOptions {
	if (raw === undefined || raw === null) return { ...DEFAULT_CACHE_GUARD, providerIDs: [...DEFAULT_CACHE_GUARD.providerIDs] };
	if (!isRecord(raw)) throw new Error("[runtime] cacheGuard must be an object");
	const permitted = new Set(["enabled", "riskAfterMinutes", "minCacheReadTokens", "providerIDs", "diagnosticsLimit", "mode"]);
	for (const key of Object.keys(raw)) if (!permitted.has(key)) throw new Error(`[runtime] cacheGuard has unknown option '${key}'`);
	const enabled = raw.enabled === undefined ? DEFAULT_CACHE_GUARD.enabled : raw.enabled;
	if (typeof enabled !== "boolean") throw new Error("[runtime] cacheGuard.enabled must be boolean");
	if (raw.mode !== undefined && raw.mode !== "advisory") throw new Error("[runtime] cacheGuard only supports advisory mode in OMP");
	const providerIDs = raw.providerIDs === undefined ? [...DEFAULT_CACHE_GUARD.providerIDs] : raw.providerIDs;
	if (!Array.isArray(providerIDs) || providerIDs.length === 0 || providerIDs.some(id => typeof id !== "string" || !id)) {
		throw new Error("[runtime] cacheGuard.providerIDs must be a non-empty string array");
	}
	return {
		enabled,
		riskAfterMinutes: raw.riskAfterMinutes === undefined ? DEFAULT_CACHE_GUARD.riskAfterMinutes : positiveInteger(raw.riskAfterMinutes, "cacheGuard.riskAfterMinutes", 0),
		minCacheReadTokens: raw.minCacheReadTokens === undefined ? DEFAULT_CACHE_GUARD.minCacheReadTokens : positiveInteger(raw.minCacheReadTokens, "cacheGuard.minCacheReadTokens", 0),
		providerIDs: [...new Set(providerIDs.map(id => canonicalProvider(id)))],
		diagnosticsLimit: raw.diagnosticsLimit === undefined ? DEFAULT_CACHE_GUARD.diagnosticsLimit : positiveInteger(raw.diagnosticsLimit, "cacheGuard.diagnosticsLimit", 1),
	};
}

function isEffort(value: unknown): value is ReasoningEffort {
	return typeof value === "string" && EFFORTS.includes(value as ReasoningEffort);
}

function parseAgentPolicy(value: unknown, name: string): AgentPolicy {
	if (!isRecord(value) || !isEffort(value.def) || !isEffort(value.min) || !isEffort(value.max)) {
		throw new Error(`[runtime] ${name} must contain def, min, and max reasoning efforts`);
	}
	if (effortIndex(value.min) > effortIndex(value.max) || effortIndex(value.def) < effortIndex(value.min) || effortIndex(value.def) > effortIndex(value.max)) {
		throw new Error(`[runtime] ${name} has an invalid reasoning range`);
	}
	return { def: value.def, min: value.min, max: value.max };
}

function parseReasoning(raw: unknown): ReasoningOptions {
	if (raw === undefined || raw === null) return cloneReasoning(DEFAULT_REASONING);
	if (!isRecord(raw)) throw new Error("[runtime] reasoningRouter must be an object");
	const permitted = new Set(["diagnosticsLimit", "agentPolicy", "providerAgentPolicy", "classBase", "supportedEfforts", "providers", "notify"]);
	for (const key of Object.keys(raw)) if (!permitted.has(key)) throw new Error(`[runtime] reasoningRouter has unknown option '${key}'`);
	const agentPolicy = { ...DEFAULT_AGENT_POLICY };
	if (raw.agentPolicy !== undefined) {
		if (!isRecord(raw.agentPolicy)) throw new Error("[runtime] reasoningRouter.agentPolicy must be an object");
		for (const [agent, policy] of Object.entries(raw.agentPolicy)) agentPolicy[agent] = parseAgentPolicy(policy, `reasoningRouter.agentPolicy.${agent}`);
	}
	const classBase = { ...DEFAULT_REASONING.classBase };
	if (raw.classBase !== undefined) {
		if (!isRecord(raw.classBase)) throw new Error("[runtime] reasoningRouter.classBase must be an object");
		for (const [kind, effort] of Object.entries(raw.classBase)) {
			if (kind === "auto" || !["fast", "balanced", "deep"].includes(kind) || !isEffort(effort)) throw new Error(`[runtime] invalid reasoning class base '${kind}'`);
			classBase[kind as Exclude<ReasoningClass, "auto">] = effort;
		}
	}
	const supportedEfforts = raw.supportedEfforts === undefined ? [...EFFORTS] : raw.supportedEfforts;
	if (!Array.isArray(supportedEfforts) || supportedEfforts.some(effort => !isEffort(effort))) throw new Error("[runtime] reasoningRouter.supportedEfforts must contain reasoning efforts");
	const providers: Record<string, ProviderRule> = {};
	const configuredProviders = raw.providers === undefined ? { openai: {} } : raw.providers;
	if (!isRecord(configuredProviders)) throw new Error("[runtime] reasoningRouter.providers must be an object");
	for (const [id, value] of Object.entries(configuredProviders)) {
		if (!isRecord(value)) throw new Error(`[runtime] reasoningRouter.providers.${id} must be an object`);
		const efforts = value.efforts === undefined ? supportedEfforts : value.efforts;
		if (!Array.isArray(efforts) || efforts.length === 0 || efforts.some(effort => !isEffort(effort))) throw new Error(`[runtime] reasoningRouter.providers.${id}.efforts must contain reasoning efforts`);
		providers[canonicalProvider(id)] = { ...(typeof value.option === "string" ? { option: value.option } : {}), efforts: [...new Set(efforts)] as ReasoningEffort[] };
	}
	const providerAgentPolicy: Record<string, Record<string, AgentPolicy>> = {};
	if (raw.providerAgentPolicy !== undefined) {
		if (!isRecord(raw.providerAgentPolicy)) throw new Error("[runtime] reasoningRouter.providerAgentPolicy must be an object");
		for (const [provider, entries] of Object.entries(raw.providerAgentPolicy)) {
			const normalized = canonicalProvider(provider);
			if (!providers[normalized] || !isRecord(entries)) throw new Error(`[runtime] reasoningRouter.providerAgentPolicy.${provider} requires a configured provider`);
			providerAgentPolicy[normalized] = {};
			for (const [agent, policy] of Object.entries(entries)) providerAgentPolicy[normalized][agent] = parseAgentPolicy(policy, `reasoningRouter.providerAgentPolicy.${provider}.${agent}`);
		}
	}
	return {
		diagnosticsLimit: raw.diagnosticsLimit === undefined ? DEFAULT_REASONING.diagnosticsLimit : positiveInteger(raw.diagnosticsLimit, "reasoningRouter.diagnosticsLimit", 1),
		agentPolicy,
		providerAgentPolicy,
		classBase,
		supportedEfforts: [...new Set(supportedEfforts)] as ReasoningEffort[],
		providers,
	};
}

function cloneReasoning(options: ReasoningOptions): ReasoningOptions {
	return { ...options, agentPolicy: { ...options.agentPolicy }, providerAgentPolicy: {}, classBase: { ...options.classBase }, supportedEfforts: [...options.supportedEfforts], providers: Object.fromEntries(Object.entries(options.providers).map(([id, rule]) => [id, { ...rule, efforts: [...rule.efforts] }])) };
}

/** CLIProxyAPI provider ids carry the same protocol semantics as their upstream family. */
export function canonicalProvider(provider: string | undefined): string {
	if (provider === "cliproxy-openai") return "openai";
	if (provider === "cliproxy-anthropic") return "anthropic";
	return provider ?? "";
}

export function validateRuntimeOptions(raw: unknown): RuntimeOptions {
	if (raw === undefined || raw === null) raw = {};
	if (!isRecord(raw)) throw new Error("[runtime] options must be an object");
	const permitted = new Set(["imageBudget", "cacheGuard", "reasoningRouter"]);
	for (const key of Object.keys(raw)) if (!permitted.has(key)) throw new Error(`[runtime] unknown option '${key}'`);
	return { imageBudget: parseImageBudget(raw.imageBudget), cacheGuard: parseCacheGuard(raw.cacheGuard), reasoningRouter: parseReasoning(raw.reasoningRouter) };
}

interface ImageReference {
	key: string;
	hash: string;
	bytes: number;
	mime: string;
	pasted: boolean;
	message: number;
	part: number;
}

export type ImagePruneReason = "budget" | "duplicate";
export interface ImagePruneState { sessions: Map<string, Map<string, ImagePruneReason>>; }
export interface ImagePruneResult { messages: unknown[]; images: number; bytes: number; changed: boolean; }

function imageFingerprint(data: string): string {
	const sample = (at: number) => data.slice(Math.max(0, Math.min(data.length - 48, Math.floor(at))), Math.max(0, Math.min(data.length - 48, Math.floor(at))) + 48);
	return `${data.length}:${sample(data.length / 4)}:${sample(data.length / 2)}:${sample((3 * data.length) / 4)}`;
}

function imageReferences(messages: readonly unknown[]): ImageReference[] {
	const refs: ImageReference[] = [];
	for (const [messageIndex, message] of messages.entries()) {
		if (!isRecord(message) || !Array.isArray(message.content)) continue;
		for (const [partIndex, part] of message.content.entries()) {
			if (!isRecord(part) || part.type !== "image" || typeof part.data !== "string" || !part.data) continue;
			const hash = imageFingerprint(part.data);
			const id = typeof message.timestamp === "number" ? String(message.timestamp) : `#${messageIndex}`;
			refs.push({ key: `${message.role ?? "message"}:${id}:${partIndex}:${hash}`, hash, bytes: part.data.length, mime: typeof part.mimeType === "string" ? part.mimeType : "image", pasted: message.role === "user", message: messageIndex, part: partIndex });
		}
	}
	return refs;
}

function budgetFor(options: ImageBudgetOptions, provider: string | undefined): ImageBudget {
	const override = options.providers[canonicalProvider(provider)];
	return { maxImages: override?.maxImages ?? options.maxImages, maxImageBytes: override?.maxImageBytes ?? options.maxImageBytes };
}

function planPrune(refs: readonly ImageReference[], previous: ReadonlyMap<string, ImagePruneReason>, budget: ImageBudget, pruneTo: number): Map<string, ImagePruneReason> | undefined {
	const live = refs.filter(ref => !previous.has(ref.key));
	if (live.length <= budget.maxImages && live.reduce((sum, ref) => sum + ref.bytes, 0) <= budget.maxImageBytes) return undefined;
	const next = new Map(previous);
	const targetImages = Math.max(1, Math.floor(budget.maxImages * pruneTo));
	const targetBytes = budget.maxImageBytes * pruneTo;
	const hashes = new Set<string>();
	let bytes = 0;
	let cut = false;
	for (let index = live.length - 1; index >= 0; index -= 1) {
		const ref = live[index]!;
		if (hashes.has(ref.hash)) { next.set(ref.key, "duplicate"); continue; }
		const fits = hashes.size === 0 ? ref.bytes <= budget.maxImageBytes : hashes.size < targetImages && bytes + ref.bytes <= targetBytes;
		if (!cut && fits) { hashes.add(ref.hash); bytes += ref.bytes; } else { cut = true; next.set(ref.key, "budget"); }
	}
	return next;
}

function imageNote(ref: ImageReference, reason: ImagePruneReason): string {
	if (reason === "duplicate") return "[Image omitted: the same image appears later in this conversation.]";
	const size = ref.bytes >= MiB ? `${(ref.bytes / MiB).toFixed(1)} MB` : `${Math.max(1, Math.round(ref.bytes / KiB))} KB`;
	return ref.pasted
		? `[Image attached by the user (${size} ${ref.mime}) removed to keep the request under the provider's size limit. Ask the user to attach it again if needed.]`
		: `[Image (${size} ${ref.mime}) removed to keep the request under the provider's size limit. Run the tool again if needed.]`;
}

/**
 * Prune an OMP context copy. Decisions are deliberately retained by stable
 * message positions, which keeps the provider's old prompt prefix identical
 * after the first overflow turn.
 */
export function pruneContextImages(state: ImagePruneState, sessionID: string, messages: readonly unknown[], options: ImageBudgetOptions, provider?: string): ImagePruneResult {
	const refs = imageReferences(messages);
	if (refs.length === 0) return { messages: [...messages], images: 0, bytes: 0, changed: false };
	const previous = state.sessions.get(sessionID) ?? new Map<string, ImagePruneReason>();
	const next = planPrune(refs, previous, budgetFor(options, provider), options.pruneTo);
	if (next) state.sessions.set(sessionID, next);
	const active = next ?? previous;
	let transformed: unknown[] | undefined;
	let images = 0;
	let bytes = 0;
	for (const ref of refs) {
		const reason = active.get(ref.key);
		if (!reason) continue;
		transformed ??= [...messages];
		const original = transformed[ref.message];
		if (!isRecord(original) || !Array.isArray(original.content)) continue;
		const content = [...original.content];
		content[ref.part] = { type: "text", text: imageNote(ref, reason) };
		transformed[ref.message] = { ...original, content };
		images += 1;
		bytes += ref.bytes;
	}
	return { messages: transformed ?? [...messages], images, bytes, changed: next !== undefined };
}

export interface CacheRisk { fingerprint: string; provider: string; model: string; idleMinutes: number; cacheReadTokens: number; }
export interface CacheDiagnostic extends CacheRisk { sessionID: string; at: string; }
export interface CacheGuardState { warned: Set<string>; diagnostics: CacheDiagnostic[]; }

function stringField(value: unknown, field: string): string | undefined {
	return isRecord(value) && typeof value[field] === "string" ? value[field] : undefined;
}

/** Evaluate the most recent response only: an earlier hit cannot justify a later miss. */
export function cacheRisk(messages: readonly unknown[], options: CacheGuardOptions, currentModel: { provider?: string; id?: string } | undefined, now = Date.now()): CacheRisk | undefined {
	if (!options.enabled || !currentModel) return undefined;
	let latest: Record<string, unknown> | undefined;
	for (const message of messages) {
		if (!isRecord(message) || message.role !== "assistant" || typeof message.completedAt !== "number") continue;
		if (!latest || message.completedAt >= (latest.completedAt as number)) latest = message;
	}
	if (!latest) return undefined;
	const provider = canonicalProvider(stringField(latest, "provider"));
	const currentProvider = canonicalProvider(currentModel.provider);
	if (!options.providerIDs.includes(provider) || provider !== currentProvider || latest.model !== currentModel.id) return undefined;
	const usage = isRecord(latest.usage) ? latest.usage : undefined;
	const cacheReadTokens = usage?.cacheRead;
	if (typeof cacheReadTokens !== "number" || !Number.isSafeInteger(cacheReadTokens) || cacheReadTokens < options.minCacheReadTokens) return undefined;
	const idleMinutes = (now - (latest.completedAt as number)) / 60_000;
	if (!Number.isFinite(idleMinutes) || idleMinutes < options.riskAfterMinutes) return undefined;
	return { provider, model: String(latest.model), idleMinutes, cacheReadTokens, fingerprint: [provider, latest.model, latest.completedAt, cacheReadTokens].join("|") };
}

export function admitCacheRisk(state: CacheGuardState, sessionID: string, risk: CacheRisk, limit: number): CacheDiagnostic | undefined {
	const fingerprint = `${sessionID}|${risk.fingerprint}`;
	if (state.warned.has(fingerprint)) return undefined;
	state.warned.add(fingerprint);
	const diagnostic: CacheDiagnostic = { ...risk, sessionID, at: new Date().toISOString() };
	state.diagnostics.push(diagnostic);
	while (state.diagnostics.length > limit) state.diagnostics.shift();
	return diagnostic;
}

export function formatCacheWarning(risk: CacheRisk): string {
	const idle = `${risk.idleMinutes.toFixed(risk.idleMinutes < 10 ? 1 : 0)} min`;
	return `Cache reuse is at risk after ${idle} idle; the prior ${risk.provider}/${risk.model} response reported ${risk.cacheReadTokens.toLocaleString("en-US")} cache-read tokens. Reuse is not guaranteed: matching prefix and routing can still miss at any age.`;
}

export interface ReasoningDecision { agent: string; provider: string; requested: ReasoningClass; effort?: ReasoningEffort; fallback?: string; rule: string; }
export interface ReasoningState { sessions: Map<string, { identity: string; provider: string; model: string; decision: ReasoningDecision }>; diagnostics: ReasoningDecision[]; }

function effortIndex(effort: ReasoningEffort): number { return EFFORTS.indexOf(effort); }

function messagesText(messages: unknown): string {
	const values: string[] = [];
	const visit = (value: unknown): void => {
		if (typeof value === "string") { values.push(value); return; }
		if (Array.isArray(value)) { for (const entry of value) visit(entry); return; }
		if (isRecord(value)) for (const entry of Object.values(value)) visit(entry);
	};
	visit(messages);
	return values.join("\n");
}

const MARKER_RE = /\[reasoning\s*:\s*(auto|fast|balanced|deep)(\s*(?::|\+)\s*escalate)?\s*\]/i;
const MARKER_ALL_RE = new RegExp(MARKER_RE.source, "gi");

function parseMarker(text: string): { requested: ReasoningClass; escalate: boolean } | undefined {
	const match = MARKER_RE.exec(text);
	return match ? { requested: match[1]!.toLowerCase() as ReasoningClass, escalate: Boolean(match[2]) } : undefined;
}

/** Return a structurally equivalent context with routing markers removed. */
export function stripReasoningMarkers(value: unknown): unknown {
	if (typeof value === "string") return value.replace(MARKER_ALL_RE, "");
	if (Array.isArray(value)) return value.map(stripReasoningMarkers);
	if (!isRecord(value)) return value;
	return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, stripReasoningMarkers(entry)]));
}

function configuredPolicy(options: ReasoningOptions, provider: string): Record<string, AgentPolicy> {
	return { ...options.agentPolicy, ...(options.providerAgentPolicy[provider] ?? {}) };
}

/** Resolve a semantic marker within the configured agent/provider bounds. */
export function resolveReasoning(options: ReasoningOptions, agent: string, providerInput: string | undefined, requestedInput: unknown, escalate: boolean, supportedByModel?: readonly unknown[]): ReasoningDecision {
	const provider = canonicalProvider(providerInput);
	const rule = options.providers[provider];
	if (!rule) return { agent, provider, requested: "auto", rule: `provider=${provider || "?"} passthrough`, fallback: "provider is not routed" };
	if (IGNORED_AGENTS.has(agent)) return { agent, provider, requested: "auto", rule: `agent=${agent} passthrough`, fallback: "auxiliary agent is not routed" };
	const policy = configuredPolicy(options, provider)[agent];
	if (!policy) return { agent, provider, requested: "auto", rule: `agent=${agent} passthrough`, fallback: "unknown agent" };
	const requested = REASONING_CLASSES.includes(requestedInput as ReasoningClass) ? requestedInput as ReasoningClass : "auto";
	const notes: string[] = [];
	let effort = requested === "auto" ? policy.def : options.classBase[requested];
	if (effortIndex(effort) < effortIndex(policy.min)) { effort = policy.min; notes.push(`clamped to agent minimum (${policy.min})`); }
	if (effortIndex(effort) > effortIndex(policy.max)) { effort = policy.max; notes.push(`clamped to agent maximum (${policy.max})`); }
	if (escalate) {
		const raised = EFFORTS[Math.min(effortIndex(effort) + 1, EFFORTS.length - 1)]!;
		if (effortIndex(raised) <= effortIndex(policy.max)) { effort = raised; notes.push("escalated one level"); }
		else notes.push(`escalation capped at agent maximum (${policy.max})`);
	}
	const candidates = rule.efforts.filter(candidate => effortIndex(candidate) >= effortIndex(policy.min) && effortIndex(candidate) <= effortIndex(policy.max) && (supportedByModel === undefined || supportedByModel.includes(candidate)));
	if (candidates.length === 0) return { agent, provider, requested, rule: `agent=${agent} class=${requested} no-supported-effort-in-range`, fallback: [...notes, "no model-supported effort within the agent range"].filter(Boolean).join("; ") };
	if (!candidates.includes(effort)) {
		const replacement = [...candidates].filter(candidate => effortIndex(candidate) <= effortIndex(effort)).sort((a, b) => effortIndex(b) - effortIndex(a))[0] ?? [...candidates].sort((a, b) => effortIndex(a) - effortIndex(b))[0]!;
		effort = replacement;
		notes.push(`model lacks requested effort; using '${replacement}'`);
	}
	return { agent, provider, requested, effort, rule: `agent=${agent} class=${requested}${escalate ? "+escalate" : ""} range=${policy.min}-${policy.max} -> ${effort}`, ...(notes.length ? { fallback: notes.join("; ") } : {}) };
}

export function routeReasoning(state: ReasoningState, sessionID: string, identity: string, model: { provider?: string; id?: string; thinking?: { efforts?: readonly unknown[] } } | undefined, messages: unknown, options: ReasoningOptions): { decision?: ReasoningDecision; messages: unknown } {
	const provider = canonicalProvider(model?.provider);
	const cleaned = stripReasoningMarkers(messages);
	if (!model?.id) return { messages: cleaned };
	const existing = state.sessions.get(sessionID);
	if (existing && existing.identity === identity && existing.provider === provider && existing.model === model.id) return { decision: existing.decision, messages: cleaned };
	const marker = parseMarker(messagesText(messages));
	const decision = resolveReasoning(options, identity, provider, marker?.requested ?? "auto", marker?.escalate ?? false, model.thinking?.efforts);
	state.sessions.set(sessionID, { identity, provider, model: model.id, decision });
	state.diagnostics.push(decision);
	while (state.diagnostics.length > options.diagnosticsLimit) state.diagnostics.shift();
	return { decision, messages: cleaned };
}
