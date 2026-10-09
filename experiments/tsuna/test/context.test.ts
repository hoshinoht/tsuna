/**
 * Stage 7: outgoing-context transforms and project context discovery.
 */
import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, readFileSync, realpathSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { FixtureTurn } from "../src/backend/fixture-types.ts";
import { DEFAULT_INSTRUCTION_FOLDERS, loadProjectContext, renderProjectContext } from "../src/context/project.ts";
import { cacheRisk, checkToolPairing, compressToolResults, pruneImages, type ImagePruneReason } from "../src/context/transforms.ts";
import { ORCH_RULES, ORCH_TOOLS, writePack } from "./helpers/pack.ts";
import { setScript, startRig, tempDir, testConfig, type TestRig } from "./helpers/harness.ts";

type Msg = { role: string; content?: unknown; [key: string]: unknown };

const rigs: TestRig[] = [];
afterEach(async () => {
	for (const rig of rigs.splice(0)) await rig.harness.shutdown();
});

async function rig(opts: Parameters<typeof startRig>[0] = {}) {
	const r = await startRig(opts);
	rigs.push(r);
	return r;
}

function textOf(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content.map(p => (typeof p === "object" && p && "text" in p ? String((p as { text: unknown }).text) : "")).join("");
}

describe("compressToolResults", () => {
	const messages: Msg[] = [
		{ role: "user", content: [{ type: "text", text: "go" }] },
		{ role: "assistant", content: [{ type: "toolCall", id: "t1", name: "read", arguments: {} }, { type: "toolCall", id: "t2", name: "grep", arguments: {} }] },
		{ role: "toolResult", toolCallId: "t1", toolName: "read", content: [{ type: "text", text: "a very long file" }], isError: false },
		{ role: "toolResult", toolCallId: "t2", toolName: "grep", content: [{ type: "text", text: "matches" }], isError: false },
		{ role: "assistant", content: [{ type: "text", text: "ok" }] },
	];

	test("replaces only the selected results and keeps ids, roles and order", () => {
		const before = structuredClone(messages);
		const out = compressToolResults(messages, { t1: "file summary" });
		expect(messages).toEqual(before);
		expect(out).toHaveLength(messages.length);
		expect(out.map(m => m.role)).toEqual(messages.map(m => m.role));
		expect(out[2]).toMatchObject({ role: "toolResult", toolCallId: "t1", toolName: "read", isError: false });
		expect(out[2]!.content).toEqual([{ type: "text", text: "[Compressed tool result]\nfile summary" }]);
		expect(out[3]).toBe(messages[3]!);
		expect(out[0]).toBe(messages[0]!);
		expect(checkToolPairing(out)).toEqual([]);
		expect(checkToolPairing(messages)).toEqual([]);
	});

	test("no compressions returns an equal copy; unknown ids are ignored", () => {
		const out = compressToolResults(messages, {});
		expect(out).not.toBe(messages);
		expect(out).toEqual(messages);
		expect(compressToolResults(messages, { nope: "x" })).toEqual(messages);
	});

	test("checkToolPairing reports orphans", () => {
		expect(checkToolPairing([{ role: "toolResult", toolCallId: "zz", content: [] }])).toHaveLength(1);
	});
});

describe("pruneImages", () => {
	const img = (seed: string) => seed.repeat(400);
	function imageMessage(i: number, data: string, role = i % 2 === 0 ? "user" : "toolResult"): Msg {
		return { role, timestamp: 1000 + i, content: [{ type: "text", text: `m${i}` }, { type: "image", data, mimeType: "image/png" }] };
	}
	const budget = (maxImages: number, pruneTo = 0.5) => ({ maxImages, maxImageBytes: 10 * 1024 * 1024, pruneTo });
	const imagesIn = (messages: Msg[]) => messages.map(m => (Array.isArray(m.content) ? m.content.some(p => (p as { type: string }).type === "image") : false));

	test("under budget: unchanged", () => {
		const messages = [imageMessage(0, img("a")), imageMessage(1, img("b"))];
		const before = structuredClone(messages);
		const decisions = new Map<string, ImagePruneReason>();
		const out = pruneImages(messages, decisions, budget(3));
		expect(out.changed).toBe(false);
		expect(out.removed).toBe(0);
		expect(out.messages).toEqual(messages);
		expect(decisions.size).toBe(0);
		expect(messages).toEqual(before);
	});

	test("over budget prunes the oldest, keeps the newest, and leaves input untouched", () => {
		const messages = [0, 1, 2, 3, 4].map(i => imageMessage(i, img(String.fromCharCode(97 + i))));
		const before = structuredClone(messages);
		const decisions = new Map<string, ImagePruneReason>();
		const out = pruneImages(messages, decisions, budget(4));
		expect(messages).toEqual(before);
		expect(out.changed).toBe(true);
		expect(out.removed).toBe(3);
		expect(imagesIn(out.messages)).toEqual([false, false, false, true, true]);
		expect([...decisions.values()]).toEqual(["budget", "budget", "budget"]);
		// Notes depend on who attached the image.
		expect(textOf(out.messages[0]!.content)).toContain("Image attached by the user");
		expect(textOf(out.messages[1]!.content)).toContain("Run the tool again");
		// Non-image parts survive.
		expect(textOf(out.messages[0]!.content)).toContain("m0");
	});

	test("older duplicates of a kept image are replaced with a duplicate note", () => {
		const a = img("a");
		const messages = [imageMessage(0, a), imageMessage(1, img("b")), imageMessage(2, img("c")), imageMessage(3, a)];
		const decisions = new Map<string, ImagePruneReason>();
		const out = pruneImages(messages, decisions, budget(3, 1));
		expect(out.removed).toBe(1);
		expect(imagesIn(out.messages)).toEqual([false, true, true, true]);
		expect(textOf(out.messages[0]!.content)).toContain("the same image appears later");
		expect([...decisions.values()]).toEqual(["duplicate"]);
	});

	test("decisions are stable: a longer conversation keeps earlier decisions and the prefix", () => {
		const messages = [0, 1, 2, 3, 4].map(i => imageMessage(i, img(String.fromCharCode(97 + i))));
		const decisions = new Map<string, ImagePruneReason>();
		const first = pruneImages(messages, decisions, budget(4));
		const snapshot = new Map(decisions);

		// Two more images: live images (4) are back within budget, so nothing new is planned.
		const longer = [...messages, imageMessage(5, img("f")), imageMessage(6, img("g"))];
		const second = pruneImages(longer, decisions, budget(4));
		expect(second.changed).toBe(false);
		expect(second.messages.slice(0, messages.length)).toEqual(first.messages);
		expect(decisions).toEqual(snapshot);

		// Over budget again: earlier decisions persist; only newer images are added.
		const longest = [...longer, imageMessage(7, img("h"))];
		const before = structuredClone(longest);
		const third = pruneImages(longest, decisions, budget(4));
		expect(third.changed).toBe(true);
		for (const [k, v] of snapshot) expect(decisions.get(k)).toBe(v);
		expect(third.messages.slice(0, 3)).toEqual(first.messages.slice(0, 3));
		expect(imagesIn(third.messages)).toEqual([false, false, false, false, false, false, true, true]);
		expect(longest).toEqual(before);

		// Replaying from the persisted decisions alone (fresh map) reproduces the request exactly.
		const replay = pruneImages(longest, new Map(decisions), budget(4));
		expect(replay.messages).toEqual(third.messages);
		expect(replay.changed).toBe(false);
	});
});

describe("cacheRisk", () => {
	const options = { enabled: true, riskAfterMinutes: 30, minCacheReadTokens: 10_000 };
	const now = 10_000_000_000;
	const assistant = (extra: Partial<Msg> = {}): Msg => ({ role: "assistant", provider: "p", model: "m", timestamp: now - 40 * 60_000, usage: { cacheRead: 20_000 }, content: [], ...extra });

	test("returns a risk only for the same model, enough cache and long idle", () => {
		const risk = cacheRisk([assistant()], options, { provider: "p", id: "m" }, now);
		expect(risk).toBeDefined();
		expect(risk!.provider).toBe("p");
		expect(risk!.model).toBe("m");
		expect(risk!.cacheReadTokens).toBe(20_000);
		expect(Math.round(risk!.idleMinutes)).toBe(40);
		expect(risk!.fingerprint).toContain("p|m|");
	});

	test("returns undefined otherwise", () => {
		expect(cacheRisk([assistant()], options, { provider: "q", id: "m" }, now)).toBeUndefined();
		expect(cacheRisk([assistant()], options, { provider: "p", id: "other" }, now)).toBeUndefined();
		expect(cacheRisk([assistant({ usage: { cacheRead: 9_999 } })], options, { provider: "p", id: "m" }, now)).toBeUndefined();
		expect(cacheRisk([assistant({ usage: {} })], options, { provider: "p", id: "m" }, now)).toBeUndefined();
		expect(cacheRisk([assistant({ timestamp: now - 10 * 60_000 })], options, { provider: "p", id: "m" }, now)).toBeUndefined();
		expect(cacheRisk([assistant()], { ...options, enabled: false }, { provider: "p", id: "m" }, now)).toBeUndefined();
		expect(cacheRisk([], options, { provider: "p", id: "m" }, now)).toBeUndefined();
		// The latest assistant decides: a newer reply from another model clears the risk.
		const switched = [assistant(), assistant({ model: "other", timestamp: now - 35 * 60_000 })];
		expect(cacheRisk(switched, options, { provider: "p", id: "m" }, now)).toBeUndefined();
	});
});

describe("compression through the runtime", () => {
	const ORIGINAL = "ORIGINAL-FILE-CONTENT-7f3a";
	const SUMMARY = "[Compressed tool result]\nnotes: one line";

	function toolResult(messages: Msg[], id: string): Msg | undefined {
		return messages.find(m => m.role === "toolResult" && m.toolCallId === id);
	}

	/** root (primary, sample-like orchestration) -> worker with read + compress. */
	function compressPackConfig() {
		const dir = tempDir("tsuna-pack-");
		const pack = writePack(dir, "cmp", [
			{ name: "root", model: "primary", primary: true, tools: [...ORCH_TOOLS, "read", "compress"], spawns: ["worker"], permissions: [...ORCH_RULES, ["allow", "compress", "*"]] },
			{ name: "worker", tools: ["read", "compress"], permissions: [["deny", "*", "*"], ["allow", "read", "*"], ["allow", "compress", "*"]] },
		]);
		return testConfig({ agentPacks: [pack], primaryRole: "root" });
	}

	/**
	 * Drives `role`: read notes.txt (id read-1), compress it, then finish.
	 * After `phase.resumed` is set, every request is captured and answered.
	 */
	function compressScript(role: string, requests: FixtureTurn["messages"][], phase: { resumed: boolean }) {
		const finish = role === "orchestrator" ? { text: "done" } : { toolCalls: [{ name: "yield", arguments: { data: { ok: true } } }] };
		return (turn: FixtureTurn) => {
			if (turn.role !== role) return role === "orchestrator" ? { text: "?" } : { text: "parent idle" };
			requests.push(structuredClone(turn.messages));
			if (phase.resumed) return finish;
			const last = turn.lastToolResults.at(-1);
			if (!last) return { toolCalls: [{ id: "read-1", name: "read", arguments: { path: "notes.txt" } }] };
			if (last.name === "read") return { toolCalls: [{ id: "cmp-1", name: "compress", arguments: { tool_call_ids: ["read-1"], summary: "notes: one line" } }] };
			return finish;
		};
	}

	async function scenario(kind: "primary" | "child") {
		const requests: FixtureTurn["messages"][] = [];
		const phase = { resumed: false };
		const role = kind === "primary" ? "orchestrator" : "worker";
		setScript(compressScript(role, requests, phase));
		const workspace = tempDir("tsuna-ws-");
		writeFileSync(join(workspace, "notes.txt"), `${ORIGINAL}\n`);
		const state = tempDir("tsuna-state-");
		const config = kind === "primary" ? undefined : compressPackConfig();
		const first = await startRig({ workspace, state, config });
		const rt = first.harness.runtime;
		let agentId = rt.primaryId;
		if (kind === "primary") await rt.promptPrimary("summarise notes");
		else {
			const child = rt.spawn(rt.primaryId, { agent: "worker", task: "summarise notes" });
			agentId = child.id;
			await child.done;
		}
		expect(requests).toHaveLength(3);
		// The request right after the read still saw the original content.
		expect(textOf(toolResult(requests[1]!, "read-1")!.content)).toContain(ORIGINAL);
		const record = rt.record(agentId)!;
		expect(record.compressions).toEqual({ "read-1": "notes: one line" });
		const transcriptFile = record.sessionFile!;
		const transcript = readFileSync(transcriptFile, "utf8");
		// The stored transcript keeps the original result.
		expect(transcript).toContain(ORIGINAL);
		expect(transcript).not.toContain("[Compressed tool result]");
		const rootId = rt.rootId;
		const firstRequests = [...requests];
		await first.harness.shutdown();

		// Resume the same root and run the agent again.
		requests.length = 0;
		phase.resumed = true;
		const second = await rig({ workspace, state, config: first.config, resume: rootId });
		const rt2 = second.harness.runtime;
		expect(rt2.record(agentId)!.compressions).toEqual({ "read-1": "notes: one line" });
		if (kind === "primary") await rt2.promptPrimary("continue");
		else {
			expect((await rt2.resume({ kind: "human", rootId }, agentId, "continue")).outcome).toBe("revived");
			await rt2.quiesce();
		}
		const after = readFileSync(transcriptFile, "utf8");
		expect(after.startsWith(transcript)).toBe(true);
		expect(after).toContain(ORIGINAL);
		expect(after).not.toContain("[Compressed tool result]");
		return { firstRequests, resumedRequests: [...requests] };
	}

	function expectCompressed(request: Msg[]) {
		const compressed = toolResult(request, "read-1")!;
		expect(textOf(compressed.content)).toBe(SUMMARY);
		expect(compressed.toolName).toBe("read");
		expect(checkToolPairing(request)).toEqual([]);
		expect(JSON.stringify(request)).not.toContain(ORIGINAL);
	}

	test("child: compress changes only the outgoing request and is replayed after resume", async () => {
		const { firstRequests, resumedRequests } = await scenario("child");
		expectCompressed(firstRequests[2]!);
		expect(resumedRequests).toHaveLength(1);
		expectCompressed(resumedRequests[0]!);
	});

	// BUG (src/harness.ts:101 + :111): the primary session is opened inside
	// AgentRuntime.create/resume (runtime.ts:150 / :226 ensureLive) before
	// `harness` is assigned, so `contextTransform` returns undefined and the
	// primary never gets compression, image pruning or cache advisories.
	// Remove `.failing` once fixed.
	test.failing("primary (sample orchestrator): compress changes the outgoing request and is replayed after resume", async () => {
		const { firstRequests, resumedRequests } = await scenario("primary");
		expectCompressed(firstRequests[2]!);
		expect(resumedRequests).toHaveLength(1);
		expectCompressed(resumedRequests[0]!);
	});

	test("compress rejects ids that are not tool results of this agent", async () => {
		setScript(() => ({ text: "hi" }));
		const r = await rig();
		const rt = r.harness.runtime;
		await rt.promptPrimary("hello");
		const out = await rt.invokeTool(rt.primaryId, "compress", "c-x", { tool_call_ids: ["nope"], summary: "s" });
		expect(out.isError).toBe(true);
		expect(out.text).toContain("unknown tool call id nope");
		expect(rt.record(rt.primaryId)!.compressions).toEqual({});
	});
});

describe("loadProjectContext", () => {
	function write(path: string, text: string) {
		mkdirSync(join(path, ".."), { recursive: true });
		writeFileSync(path, text);
	}

	function fixture() {
		const repo = realpathSync(tempDir("tsuna-proj-"));
		mkdirSync(join(repo, ".git"));
		write(join(repo, "AGENTS.md"), "root instructions");
		write(join(repo, ".tsuna", "AGENTS.md"), "tsuna folder instructions");
		write(join(repo, ".claude", "AGENTS.md"), "claude folder instructions");
		write(join(repo, ".agents", "skills", "deploy", "SKILL.md"), "---\nname: deploy-it\ndescription: Ship the thing\n---\nbody");
		write(join(repo, ".agents", "skills", "plain", "SKILL.md"), "no frontmatter");
		write(join(repo, "home", "AGENTS.md"), "home instructions");
		write(join(repo, "home", "work", ".agents", "AGENTS.md"), "deep agents instructions");
		write(join(repo, "home", "work", "empty", "AGENTS.md"), "   \n");
		const outside = realpathSync(tempDir("tsuna-escape-"));
		write(join(outside, "AGENTS.md"), "escaped instructions");
		mkdirSync(join(outside, "evil"), { recursive: true });
		write(join(outside, "evil", "SKILL.md"), "---\nname: evil\ndescription: escaped\n---");
		symlinkSync(join(outside, "AGENTS.md"), join(repo, "home", "work", "AGENTS.md"));
		mkdirSync(join(repo, "home", "work", ".tsuna", "skills"), { recursive: true });
		symlinkSync(join(outside, "evil"), join(repo, "home", "work", ".tsuna", "skills", "evil"));
		const cwd = join(repo, "home", "work");
		return { repo, cwd, home: join(repo, "home"), outside };
	}

	test("walks from the git root to cwd, skipping $HOME, foreign folders and escaping symlinks", () => {
		const { repo, cwd, home } = fixture();
		const ctx = loadProjectContext(cwd, DEFAULT_INSTRUCTION_FOLDERS, home);
		expect(ctx.instructions.map(i => i.path)).toEqual([join(repo, "AGENTS.md"), join(repo, ".tsuna", "AGENTS.md"), join(cwd, ".agents", "AGENTS.md")]);
		expect(ctx.instructions.map(i => i.content)).toEqual(["root instructions", "tsuna folder instructions", "deep agents instructions"]);
		const all = JSON.stringify(ctx);
		expect(all).not.toContain("claude folder instructions");
		expect(all).not.toContain("home instructions");
		expect(all).not.toContain("escaped");
		expect(ctx.skills).toEqual([
			{ name: "deploy-it", description: "Ship the thing", path: join(repo, ".agents", "skills", "deploy", "SKILL.md") },
			{ name: "plain", description: "", path: join(repo, ".agents", "skills", "plain", "SKILL.md") },
		]);
	});

	test(".claude is only read when explicitly listed; $HOME is included when it is not home", () => {
		const { repo, cwd } = fixture();
		expect(DEFAULT_INSTRUCTION_FOLDERS).not.toContain(".claude");
		const ctx = loadProjectContext(cwd, [...DEFAULT_INSTRUCTION_FOLDERS, ".claude"], "/nonexistent-home");
		const paths = ctx.instructions.map(i => i.path);
		expect(paths).toContain(join(repo, ".claude", "AGENTS.md"));
		expect(paths).toContain(join(repo, "home", "AGENTS.md"));
	});

	test("renderProjectContext includes instruction paths and skills", () => {
		const { repo, cwd, home } = fixture();
		const text = renderProjectContext(loadProjectContext(cwd, DEFAULT_INSTRUCTION_FOLDERS, home));
		expect(text).toContain("## Project instructions");
		expect(text).toContain(`### ${join(repo, "AGENTS.md")}`);
		expect(text).toContain("root instructions");
		expect(text).toContain(`- deploy-it: Ship the thing (${join(repo, ".agents", "skills", "deploy", "SKILL.md")})`);
		expect(renderProjectContext({ instructions: [], skills: [] })).toBe("");
	});

	test("without a git repo only cwd itself is considered", () => {
		const parent = realpathSync(tempDir("tsuna-nogit-"));
		write(join(parent, "AGENTS.md"), "parent");
		const cwd = join(parent, "child");
		write(join(cwd, "AGENTS.md"), "child");
		const ctx = loadProjectContext(cwd, DEFAULT_INSTRUCTION_FOLDERS, "/nonexistent-home");
		expect(ctx.instructions.map(i => i.content)).toEqual(["child"]);
	});

	test("project instructions reach the primary system prompt", async () => {
		const workspace = tempDir("tsuna-ws-");
		writeFileSync(join(workspace, "AGENTS.md"), "PROJECT-RULE-91: prefer tabs");
		setScript(() => ({ text: "ok" }));
		const r = await rig({ workspace });
		expect(r.harness.runtime.record(r.harness.runtime.primaryId)!.contract.systemPrompt).toContain("PROJECT-RULE-91");
	});
});
