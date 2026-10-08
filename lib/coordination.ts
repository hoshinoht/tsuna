import { realpathSync } from "node:fs";
import { basename, dirname, isAbsolute, join } from "node:path";
import type { ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { AgentRegistry } from "@oh-my-pi/pi-coding-agent";
import { parseInternalUrl } from "@oh-my-pi/pi-coding-agent/internal-urls/parse";
import { InternalUrlRouter } from "@oh-my-pi/pi-coding-agent/internal-urls/router";
import { ensurePersistedRoster, sessionFileBelongsToRoot } from "@oh-my-pi/pi-coding-agent/registry/persisted-agents";
import { loadSessionMessagesReadOnly } from "@oh-my-pi/pi-coding-agent/session/session-loader";
import { splitInternalUrlSel } from "@oh-my-pi/pi-tui/tools/read";

export interface CoordinationTarget {
	scheme: "agent" | "history";
	id: string;
	pathname: string;
	invalid: boolean;
	selector?: string;
}

export interface CoordinationRef {
	id: string;
	kind: string;
	parentId?: string;
	status: string;
	sessionFile: string | null;
	history?: { outputPath?: string };
	session?: { messages: readonly unknown[] } | null;
}

export interface CoordinationScope {
	callerId: string;
	callerSessionFile?: string;
	rootSessionFile?: string;
	agents: readonly CoordinationRef[];
}

/** Parse the same inline selectors and single-slash aliases as the native read tool. */
export function coordinationTarget(path: string): CoordinationTarget | undefined {
	if (!/^(agent|history|hoshi-history|hoshi-agent):\//i.test(path)) return;
	const normalized = path.replace(/^(agent|history|hoshi-history|hoshi-agent):\/(?!\/)/i, "$1://");
	const split = splitInternalUrlSel(normalized, () => ({ selectors: "lines" }));
	try {
		const url = parseInternalUrl(split.path);
		const scheme = /^(?:hoshi-)?agent:$/.test(url.protocol.toLowerCase()) ? "agent" : "history";
		const id = url.rawHost || url.hostname;
		const pathname = url.rawPathname ?? url.pathname;
		return { scheme, id, pathname, selector: split.sel, invalid: !!url.search || !!url.hash ||
			(url.protocol.toLowerCase() === "hoshi-history:" && (id !== "" || (pathname !== "" && pathname !== "/"))) };
	} catch {
		return { scheme: "history", id: "", pathname: "", invalid: true };
	}
}

function canonicalFile(file: string | undefined | null): string | undefined {
	if (!file) return;
	try { return realpathSync.native(file); } catch { return; }
}

/** SessionManager can allocate a trusted transcript path before its first durable write. */
function canonicalSessionPath(file: string | undefined | null): string | undefined {
	if (!file || !isAbsolute(file)) return;
	const tail: string[] = [];
	for (let current = file; ; current = dirname(current)) {
		try { return join(realpathSync.native(current), ...tail); }
		catch {
			if (dirname(current) === current) return;
			tail.unshift(basename(current));
		}
	}
}

export function belongsToCoordinationScope(ref: CoordinationRef, scope: CoordinationScope): boolean {
	const file = canonicalSessionPath(ref.sessionFile);
	const root = canonicalSessionPath(scope.rootSessionFile);
	return ref.kind !== "advisor" && !!file && !!root && root.endsWith(".jsonl") && sessionFileBelongsToRoot(file, root);
}

export function coordinationReason(target: CoordinationTarget, scope: CoordinationScope | undefined, message: boolean): string | undefined {
	if (target.invalid) return "Invalid coordination URL";
	const caller = canonicalSessionPath(scope?.callerSessionFile);
	const root = canonicalSessionPath(scope?.rootSessionFile);
	if (!scope || !caller || !root || !root.endsWith(".jsonl") || !sessionFileBelongsToRoot(caller, root)) {
		return "Coordination requires a bound current session";
	}
	if (target.scheme === "history" && target.id === "" && (target.pathname === "" || target.pathname === "/") && !message) return;
	if (target.scheme === "history" && target.id.toLowerCase() === "current" && target.pathname === "/full" && !message) return;
	if (!target.id || target.id === "all") return "Use an explicit agent ID; broadcasts are not permitted";
	if (target.scheme === "history" && target.pathname !== "" && target.pathname !== "/") return "Invalid history target";
	const ref = scope.agents.find(agent => agent.id === target.id) ??
		(target.scheme === "history" ? scope.agents.find(agent => agent.id.toLowerCase() === target.id.toLowerCase()) : undefined);
	if (!ref || !belongsToCoordinationScope(ref, scope)) return "Agent is outside the current session tree or unavailable";
	if (message && (target.scheme !== "agent" || (target.pathname !== "" && target.pathname !== "/") || target.selector ||
		ref.kind !== "sub" || ref.parentId !== scope.callerId || ref.id === scope.callerId || ref.status === "aborted")) {
		return "Messages may target only a non-aborted direct child";
	}
}

export async function currentCoordinationScope(ctx: Pick<ExtensionContext, "agent" | "sessionManager">): Promise<CoordinationScope> {
	const registry = AgentRegistry.global();
	const callerSessionFile = ctx.sessionManager.getSessionFile();
	// Refresh resumed/parked children using the caller's own transcript, never the global Main ref.
	const rootSessionFile = callerSessionFile ? await ensurePersistedRoster(registry, callerSessionFile) : undefined;
	return { callerId: ctx.agent.id, callerSessionFile, rootSessionFile, agents: registry.list() };
}

/** Native indexes/output lookups scan global artifacts; pin reads to the caller's own tree. */
export function registerScopedCoordinationReads(): void {
	InternalUrlRouter.instance().register({
		scheme: "hoshi-history",
		spec: { backing: "virtual", selectors: "lines", immutable: true },
		async resolve(url, context) {
			const registry = context?.agentRegistry ?? AgentRegistry.global();
			const rootSessionFile = context?.sessionFile ? await ensurePersistedRoster(registry, context.sessionFile) : undefined;
			if (!rootSessionFile) throw new Error("History index requires a bound current session");
			const scope: CoordinationScope = { callerId: "", rootSessionFile, agents: registry.list() };
			const rows = scope.agents.filter(ref => ref.kind === "sub" && belongsToCoordinationScope(ref, scope));
			const content = rows.length ? rows.map(ref => `- ${ref.id}: ${ref.status}; parent=${ref.parentId ?? "root"}; history://${ref.id}`).join("\n") : "No subagents in this session.";
			return { url: url.href, content, contentType: "text/markdown", size: Buffer.byteLength(content) };
		},
	});
	InternalUrlRouter.instance().register({
		scheme: "hoshi-agent",
		spec: { backing: "virtual", selectors: "lines", immutable: true },
		async resolve(url, context) {
			const registry = context?.agentRegistry ?? AgentRegistry.global();
			const rootSessionFile = context?.sessionFile ? await ensurePersistedRoster(registry, context.sessionFile) : undefined;
			const scope: CoordinationScope = { callerId: "", rootSessionFile, agents: registry.list() };
			const ref = registry.get(url.rawHost || url.hostname);
			if (!ref || !belongsToCoordinationScope(ref, scope)) throw new Error("Agent is outside the current session tree or unavailable");
			const root = canonicalSessionPath(rootSessionFile)!;
			const transcript = canonicalSessionPath(ref.sessionFile)!;
			const artifact = ref.history?.outputPath ?? join(dirname(transcript), `${ref.id}.md`);
			const output = canonicalFile(artifact);
			if (output && !sessionFileBelongsToRoot(output, root)) throw new Error("Output artifact is outside the current session tree");
			const pathname = url.rawPathname ?? url.pathname;
			let content: string;
			if (pathname && pathname !== "/") {
				const sidecar = canonicalFile(artifact.replace(/\.md$/, ".json"));
				if (sidecar && !sessionFileBelongsToRoot(sidecar, root)) throw new Error("Output artifact is outside the current session tree");
				const jsonFile = sidecar ?? output;
				if (!jsonFile) throw new Error("Agent has no published JSON output yet; read its history instead");
				let value: unknown = await Bun.file(jsonFile).json();
				for (const key of pathname.split("/").filter(Boolean)) {
					if (!value || typeof value !== "object" || !Object.hasOwn(value, key)) throw new Error("Output JSON path was not found");
					value = (value as Record<string, unknown>)[key];
				}
				content = JSON.stringify(value, null, 2);
			} else if (output) {
				content = await Bun.file(output).text();
				if (ref.status === "running") content = `[Previous output; ${ref.id} is running again]\n\n${content}`;
			} else {
				const messages = (ref.session?.messages ?? await loadSessionMessagesReadOnly(transcript) as readonly unknown[])
					.filter((value): value is Record<string, unknown> => typeof value === "object" && value !== null);
				const last = messages.findLast(message => message.role === "assistant");
				const progress = messages.filter(message => message.role === "toolResult" && message.toolName === "yield" && !message.isError)
					.map(message => (message.details as { data?: unknown } | undefined)?.data).filter(data => data !== undefined);
				const latestAssistantMessage = typeof last?.content === "string" ? last.content : Array.isArray(last?.content)
					? last.content.filter(block => block?.type === "text").map(block => block.text).join("\n") : undefined;
				content = JSON.stringify({ id: ref.id, status: ref.status, progress, latestAssistantMessage }, null, 2);
			}
			return { url: url.href, content, contentType: "text/markdown", size: Buffer.byteLength(content) };
		},
	});
}
