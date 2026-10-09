/**
 * Outgoing-context transforms. They operate on the copy Pi passes to the
 * `context` hook before every model call; the stored transcript is never
 * modified. Transforms are deterministic functions of (messages, stored
 * decisions), so resume, fork and branch replay produce the same request.
 *
 * Image budgeting and cache advisories are adapted from Tsuna `lib/runtime.ts`
 * at 92c4728 (GPL-3.0-or-later): Pi messages carry `timestamp` rather than
 * OMP's `completedAt`, and provider budgets are not yet configurable per
 * provider. Manual compression follows Tsuna `lib/tsuna.ts`.
 */
import type { CacheAdvisoryConfig, ImageBudgetConfig } from "../config.ts";

type Message = { role: string; content?: unknown; [key: string]: unknown };

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Replace selected tool results' content; ids and pairing are untouched. */
export function compressToolResults<T extends Message>(messages: readonly T[], compressions: Readonly<Record<string, string>>): T[] {
	if (Object.keys(compressions).length === 0) return [...messages];
	return messages.map(message => {
		const id = message.toolCallId;
		if (message.role !== "toolResult" || typeof id !== "string" || !(id in compressions)) return message;
		return { ...message, content: [{ type: "text", text: `[Compressed tool result]\n${compressions[id]}` }] };
	});
}

// ---------------------------------------------------------------- images

const KiB = 1024;
const MiB = 1024 * KiB;

interface ImageRef {
	key: string;
	hash: string;
	bytes: number;
	mime: string;
	pasted: boolean;
	message: number;
	part: number;
}

export type ImagePruneReason = "budget" | "duplicate";

function fingerprint(data: string): string {
	const sample = (at: number) => {
		const start = Math.max(0, Math.min(data.length - 48, Math.floor(at)));
		return data.slice(start, start + 48);
	};
	return `${data.length}:${sample(data.length / 4)}:${sample(data.length / 2)}:${sample((3 * data.length) / 4)}`;
}

function imageRefs(messages: readonly Message[]): ImageRef[] {
	const refs: ImageRef[] = [];
	for (const [messageIndex, message] of messages.entries()) {
		if (!Array.isArray(message.content)) continue;
		for (const [partIndex, part] of message.content.entries()) {
			if (!isRecord(part) || part.type !== "image" || typeof part.data !== "string" || !part.data) continue;
			const hash = fingerprint(part.data);
			const id = typeof message.timestamp === "number" ? String(message.timestamp) : `#${messageIndex}`;
			refs.push({ key: `${message.role}:${id}:${partIndex}:${hash}`, hash, bytes: part.data.length, mime: typeof part.mimeType === "string" ? part.mimeType : "image", pasted: message.role === "user", message: messageIndex, part: partIndex });
		}
	}
	return refs;
}

function planPrune(refs: readonly ImageRef[], previous: ReadonlyMap<string, ImagePruneReason>, budget: ImageBudgetConfig): Map<string, ImagePruneReason> | undefined {
	const live = refs.filter(ref => !previous.has(ref.key));
	if (live.length <= budget.maxImages && live.reduce((sum, ref) => sum + ref.bytes, 0) <= budget.maxImageBytes) return undefined;
	const next = new Map(previous);
	const targetImages = Math.max(1, Math.floor(budget.maxImages * budget.pruneTo));
	const targetBytes = budget.maxImageBytes * budget.pruneTo;
	const hashes = new Set<string>();
	let bytes = 0;
	let cut = false;
	for (let index = live.length - 1; index >= 0; index -= 1) {
		const ref = live[index]!;
		if (hashes.has(ref.hash)) {
			next.set(ref.key, "duplicate");
			continue;
		}
		const fits = hashes.size === 0 ? ref.bytes <= budget.maxImageBytes : hashes.size < targetImages && bytes + ref.bytes <= targetBytes;
		if (!cut && fits) {
			hashes.add(ref.hash);
			bytes += ref.bytes;
		} else {
			cut = true;
			next.set(ref.key, "budget");
		}
	}
	return next;
}

function imageNote(ref: ImageRef, reason: ImagePruneReason): string {
	if (reason === "duplicate") return "[Image omitted: the same image appears later in this conversation.]";
	const size = ref.bytes >= MiB ? `${(ref.bytes / MiB).toFixed(1)} MB` : `${Math.max(1, Math.round(ref.bytes / KiB))} KB`;
	return ref.pasted
		? `[Image attached by the user (${size} ${ref.mime}) removed to keep the request under the provider's size limit. Ask the user to attach it again if needed.]`
		: `[Image (${size} ${ref.mime}) removed to keep the request under the provider's size limit. Run the tool again if needed.]`;
}

/**
 * Decisions persist by stable key (in the agent record), so the earlier prompt
 * prefix stays byte-identical after the first prune and across restarts.
 */
export function pruneImages<T extends Message>(messages: readonly T[], decisions: Map<string, ImagePruneReason>, budget: ImageBudgetConfig): { messages: T[]; changed: boolean; removed: number } {
	const refs = imageRefs(messages);
	if (refs.length === 0) return { messages: [...messages], changed: false, removed: 0 };
	const next = planPrune(refs, decisions, budget);
	if (next) for (const [k, v] of next) decisions.set(k, v);
	let out: T[] | undefined;
	let removed = 0;
	for (const ref of refs) {
		const reason = decisions.get(ref.key);
		if (!reason) continue;
		out ??= [...messages];
		const original = out[ref.message]!;
		if (!Array.isArray(original.content)) continue;
		const content = [...original.content];
		content[ref.part] = { type: "text", text: imageNote(ref, reason) };
		out[ref.message] = { ...original, content };
		removed += 1;
	}
	return { messages: out ?? [...messages], changed: next !== undefined, removed };
}

// ----------------------------------------------------------- cache advisory

export interface CacheRisk {
	provider: string;
	model: string;
	idleMinutes: number;
	cacheReadTokens: number;
	fingerprint: string;
}

/** Advisory only: the latest assistant response decides; nothing is blocked. */
export function cacheRisk(messages: readonly Message[], options: CacheAdvisoryConfig, current: { provider: string; id: string }, now = Date.now()): CacheRisk | undefined {
	if (!options.enabled) return undefined;
	let latest: Message | undefined;
	for (const m of messages) if (m.role === "assistant" && typeof m.timestamp === "number" && (!latest || (m.timestamp as number) >= (latest.timestamp as number))) latest = m;
	if (!latest || latest.provider !== current.provider || latest.model !== current.id) return undefined;
	const usage = isRecord(latest.usage) ? latest.usage : undefined;
	const cacheRead = usage?.cacheRead;
	if (typeof cacheRead !== "number" || cacheRead < options.minCacheReadTokens) return undefined;
	const idleMinutes = (now - (latest.timestamp as number)) / 60_000;
	if (idleMinutes < options.riskAfterMinutes) return undefined;
	return { provider: current.provider, model: current.id, idleMinutes, cacheReadTokens: cacheRead, fingerprint: `${current.provider}|${current.id}|${latest.timestamp}|${cacheRead}` };
}

/** Every tool result must answer a preceding assistant tool call (and vice versa). */
export function checkToolPairing(messages: readonly Message[]): string[] {
	const problems: string[] = [];
	const open = new Set<string>();
	for (const [index, m] of messages.entries()) {
		if (m.role === "assistant" && Array.isArray(m.content)) {
			for (const b of m.content) if (isRecord(b) && b.type === "toolCall") open.add(String(b.id));
		} else if (m.role === "toolResult") {
			const id = String(m.toolCallId);
			if (!open.delete(id)) problems.push(`message ${index}: tool result ${id} has no matching call`);
		}
	}
	return problems;
}
