import { stat } from "node:fs/promises";
import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { AgentRegistry, type ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { ensurePersistedRoster, sessionFileBelongsToRoot } from "@oh-my-pi/pi-coding-agent/registry/persisted-agents";
import { loadSessionFile } from "@oh-my-pi/pi-coding-agent/session/session-loader";
import type { AutoReviewEvidence } from "./auto-review";
import type { ToolCall } from "./permissions";

const MAX_PERSISTED_TRANSCRIPT_BYTES = 2_000_000;

type RecordValue = Record<string, unknown>;

function record(value: unknown): RecordValue | undefined {
	return typeof value === "object" && value !== null && !Array.isArray(value) ? value as RecordValue : undefined;
}

function messageText(content: unknown): string | undefined {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return;
	const text = content.flatMap(block => {
		const value = record(block);
		return value?.type === "text" && typeof value.text === "string" ? [value.text] : [];
	}).join("\n");
	return text || undefined;
}

function sessionId(ctx: ExtensionContext): string {
	return ctx.sessionManager.getSessionId?.() || ctx.sessionManager.getHeader?.()?.id || "active-session";
}

function evidenceId(session: string, entry: RecordValue, index: number): string {
	return `${session}:${typeof entry.id === "string" ? entry.id : index}`;
}

function canonicalFile(file: string | null | undefined): string | undefined {
	if (!file) return;
	try { return realpathSync.native(file); } catch { return resolve(file); }
}

function executableInput(toolName: string, value: unknown): unknown {
	const source = record(value);
	if (!source) return value;
	// Only bash has these explanatory approval fields in this runtime. Other
	// tools may execute a `description` value (for example an API payload).
	if (toolName !== "bash") return { ...source };
	const { description: _description, justification: _justification, ...arguments_ } = source;
	return arguments_;
}

function branchEvidence(entries: readonly unknown[], id: string): {
	humanMessages: AutoReviewEvidence["humanMessages"];
	delegatedMessages: AutoReviewEvidence["delegatedMessages"];
	toolCalls: AutoReviewEvidence["toolCalls"];
} {
	const humanMessages: AutoReviewEvidence["humanMessages"] = [];
	const delegatedMessages: AutoReviewEvidence["delegatedMessages"] = [];
	const toolCalls: AutoReviewEvidence["toolCalls"] = [];
	for (const [index, raw] of entries.entries()) {
		const rawEntry = record(raw);
		const entry = rawEntry;
		const message = rawEntry?.type === "message" ? record(rawEntry.message) : rawEntry?.role ? rawEntry : undefined;
		if (!entry || !message) continue;
		const text = messageText(message.content);
		if (message.role === "user" && text && message.synthetic !== true) {
			// Older persisted user turns predate attribution. The documented values
			// are `user` and `agent`; reject any other value rather than guessing.
			if (message.attribution === undefined || message.attribution === "user") humanMessages.push({ id: evidenceId(id, entry, index), text });
			delegatedMessages.push({ id: evidenceId(id, entry, index), text });
		}
		if (message.role !== "assistant" || !Array.isArray(message.content)) continue;
		for (const block of message.content) {
			const tool = record(block);
			if (tool?.type !== "toolCall" || typeof tool.id !== "string" || typeof tool.name !== "string") continue;
			toolCalls.push({ id: tool.id, toolName: tool.name, input: executableInput(tool.name, tool.arguments ?? {}) as Record<string, unknown> });
		}
	}
	return { humanMessages, delegatedMessages, toolCalls };
}

function branchOrUndefined(ctx: ExtensionContext): readonly unknown[] | undefined {
	try {
		const branch = ctx.sessionManager.getBranch();
		return Array.isArray(branch) ? branch : undefined;
	} catch {
		return;
	}
}

function activeEntries(entries: readonly unknown[]): readonly unknown[] | undefined {
	const byId = new Map<string, RecordValue>();
	let leaf: string | undefined;
	for (const raw of entries) {
		const entry = record(raw);
		if (!entry || entry.type === "session" || typeof entry.id !== "string") continue;
		byId.set(entry.id, entry);
		leaf = entry.id;
	}
	if (!leaf) return [];
	const branch: RecordValue[] = [];
	const seen = new Set<string>();
	while (leaf && !seen.has(leaf)) {
		seen.add(leaf);
		const entry = byId.get(leaf);
		if (!entry) return;
		branch.push(entry);
		leaf = typeof entry.parentId === "string" ? entry.parentId : undefined;
	}
	if (leaf) return;
	return branch.reverse();
}

async function boundedMessages(file: string): Promise<{ id: string; messages: readonly unknown[] } | undefined> {
	try {
		if ((await stat(file)).size > MAX_PERSISTED_TRANSCRIPT_BYTES) return;
		const loaded = await loadSessionFile(file, undefined, { throwIfMissing: true });
		const header = record(loaded.entries[0]);
		if (loaded.invalidHeader || loaded.malformedRecords > 0 || !header || header.type !== "session" || typeof header.id !== "string") return;
		const messages = activeEntries(loaded.entries);
		return messages ? { id: header.id, messages } : undefined;
	} catch {
		return;
	}
}

function rootFromLiveParent(ctx: ExtensionContext): { id: string; branch: readonly unknown[] } | undefined {
	const registry = AgentRegistry.global();
	let current = registry.get(ctx.agent.id);
	let currentFile = canonicalFile(ctx.sessionManager.getSessionFile?.());
	const currentId = ctx.sessionManager.getSessionId?.();
	const initialHeader = ctx.sessionManager.getHeader?.();
	if (!current || !current.session || !currentFile || !initialHeader?.parentSession ||
		(currentId && current.session.sessionManager.getSessionId() !== currentId) ||
		(current.sessionFile && canonicalFile(current.sessionFile) !== currentFile)) return;
	let currentHeader: { parentSession?: string } = initialHeader;
	const seen = new Set<string>();
	while (current?.parentId && !seen.has(current.id)) {
		seen.add(current.id);
		const parent = registry.get(current.parentId);
		if (!parent?.session) return;
		if (parent.kind !== "main" && parent.kind !== "sub") return;
		const parentFile = canonicalFile(parent.session.sessionManager.getSessionFile());
		if (!parentFile || canonicalFile(currentHeader.parentSession) !== parentFile) return;
		current = parent;
		currentFile = parentFile;
		const parentHeader = parent.session.sessionManager.getHeader();
		if (!parentHeader) return;
		currentHeader = parentHeader;
	}
	if (!current || current.kind !== "main" || seen.has(current.id) || !current.session || !currentFile) return;
	try {
		return { id: current.session.sessionManager.getSessionId(), branch: current.session.sessionManager.getBranch() };
	} catch {
		return;
	}
}

async function rootFromPersistedParent(ctx: ExtensionContext): Promise<{ id: string; messages: readonly unknown[] } | undefined> {
	const sessionFile = ctx.sessionManager.getSessionFile?.();
	const header = ctx.sessionManager.getHeader?.();
	if (!sessionFile || !header?.parentSession) return;
	const registry = AgentRegistry.global();
	const root = await ensurePersistedRoster(registry, sessionFile);
	if (!root || !sessionFileBelongsToRoot(sessionFile, root) || !sessionFileBelongsToRoot(header.parentSession, root)) return;
	const current = registry.get(ctx.agent.id);
	if (!current || current.kind !== "sub" || !current.sessionFile || canonicalFile(current.sessionFile) !== canonicalFile(sessionFile)) return;
	let ref = current;
	const seen = new Set<string>();
	while (ref.parentId && !seen.has(ref.id)) {
		seen.add(ref.id);
		const parent = registry.get(ref.parentId);
		if (!parent || !parent.sessionFile || !sessionFileBelongsToRoot(parent.sessionFile, root)) return;
		ref = parent;
	}
	if (ref.kind !== "main" || seen.has(ref.id) || canonicalFile(ref.sessionFile) !== canonicalFile(root)) return;
	return boundedMessages(root);
}

/** Build classifier evidence from active execution history, never agent prose or tool output. */
export async function buildAutoReviewEvidence(call: ToolCall, role: string, ctx: ExtensionContext): Promise<AutoReviewEvidence> {
	const branch = branchOrUndefined(ctx);
	const base: AutoReviewEvidence = {
		action: { toolName: call.toolName, input: executableInput(call.toolName, call.input) as Record<string, unknown> },
		cwd: ctx.cwd,
		role,
		humanMessages: [],
		toolCalls: [],
		delegatedMessages: [],
		contextComplete: false,
	};
	if (!branch) return base;
	const current = branchEvidence(branch, sessionId(ctx));
	base.toolCalls = current.toolCalls;
	if (ctx.agent.kind === "main") {
		base.humanMessages = current.humanMessages;
		base.contextComplete = true;
		return base;
	}
	base.delegatedMessages = current.delegatedMessages;
	const liveRoot = rootFromLiveParent(ctx);
	if (liveRoot) {
		const root = branchEvidence(liveRoot.branch, liveRoot.id);
		base.humanMessages = root.humanMessages;
		base.toolCalls = [...root.toolCalls, ...base.toolCalls];
		base.contextComplete = true;
		return base;
	}
	const persistedRoot = await rootFromPersistedParent(ctx);
	if (!persistedRoot) return base;
	const root = branchEvidence(persistedRoot.messages, persistedRoot.id);
	base.humanMessages = root.humanMessages;
	base.toolCalls = [...root.toolCalls, ...base.toolCalls];
	base.contextComplete = true;
	return base;
}
