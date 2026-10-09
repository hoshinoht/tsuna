import { afterEach, describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { FixtureReply, FixtureTurn } from "../src/backend/fixture-types.ts";
import { createBackendSession, type BackendSessionSpec } from "../src/backend/pi.ts";
import { checkToolPairing } from "../src/context/transforms.ts";
import type { RuntimeEvent } from "../src/orchestration/types.ts";
import { treePack } from "./helpers/pack.ts";
import { call, setScript, startRig, tempDir, testConfig, type TestRig } from "./helpers/harness.ts";

const rigs: TestRig[] = [];
afterEach(async () => {
	for (const rig of rigs.splice(0)) await rig.harness.shutdown();
});

async function rig(opts: Parameters<typeof startRig>[0] = {}) {
	const r = await startRig(opts);
	rigs.push(r);
	return r;
}

function treeConfig(extra: Record<string, unknown> = {}) {
	const dir = tempDir("tsuna-pack-");
	return testConfig({ agentPacks: [treePack(dir)], primaryRole: "root", ...extra });
}

function directive(text: string | undefined, key: string): string | undefined {
	return new RegExp(`${key}=(\\S+)`).exec(text ?? "")?.[1];
}

/**
 * Leaf worker behaviour driven by its assignment text:
 *   delay=N      wait N ms before answering (abortable)
 *   fail         yield a failure
 *   steerable    first turn does a non-terminal glob after the delay, then reports steering it saw
 *   noyield      never yields (tests the reminder ladder)
 *   mixed        first calls glob+yield in one message (mixed batch), then yields alone
 */
function worker(turn: FixtureTurn): FixtureReply {
	const assignment = turn.userTexts.filter(t => !t.startsWith("[tsuna]")).at(-1) ?? "";
	const delay = Number(directive(assignment, "delay") ?? 0);
	const steers = turn.userTexts.filter(t => t.includes("[steering"));
	if (assignment.includes("noyield")) return { text: "I refuse to yield" };
	if (assignment.includes("mixed") && turn.lastToolResults.length === 0) {
		return { toolCalls: [{ name: "glob", arguments: { pattern: "*" } }, { name: "yield", arguments: { data: { early: true } } }] };
	}
	if (assignment.includes("steerable") && turn.lastToolResults.length === 0) {
		return { delayMs: delay, toolCalls: [{ name: "glob", arguments: { pattern: "*" } }] };
	}
	if (assignment.includes("fail")) return { delayMs: delay, ...call("yield", { status: "failure", error: "worker failed on purpose" }) };
	return {
		delayMs: delay,
		...call("yield", { data: { task: assignment.slice(0, 80), steers, assistantTurns: turn.assistantCount, mixedRejected: turn.lastToolResults.some(r => r.name === "yield" && r.isError) } }),
	};
}

function concurrency(events: RuntimeEvent[], prefix = "worker-") {
	let running = 0;
	let max = 0;
	for (const e of events) {
		if (e.type !== "agent_status" || !String(e.agentId).startsWith(prefix)) continue;
		if (e.status === "running") running++;
		if (e.prior === "running") running--;
		max = Math.max(max, running);
	}
	return max;
}

describe("scheduling", () => {
	test("parallel dispatch respects the per-parent limit of four and returns every result", async () => {
		setScript(turn => {
			if (turn.role === "root") {
				if (turn.lastToolResults.length === 0) {
					return call("dispatch", { tasks: Array.from({ length: 7 }, (_, i) => ({ agent: "worker", task: `job ${i} delay=120` })) });
				}
				return { text: "all done" };
			}
			return worker(turn);
		});
		const r = await rig({ config: treeConfig() });
		await r.harness.runtime.promptPrimary("start");
		const workers = r.harness.runtime.records().filter(x => x.role === "worker");
		expect(workers).toHaveLength(7);
		expect(workers.every(w => w.results[0]?.status === "success")).toBe(true);
		expect(concurrency(r.events)).toBe(4);
		expect(r.harness.runtime.diagnostics().permitsInFlight).toBe(0);
	});

	test("nested delegation cannot exhaust permits and deadlock", async () => {
		setScript(turn => {
			if (turn.role === "root") {
				if (turn.lastToolResults.length === 0) return call("dispatch", { tasks: Array.from({ length: 4 }, (_, i) => ({ agent: "lead", task: `lead ${i}` })) });
				return { text: "tree done" };
			}
			if (turn.role === "lead") {
				if (turn.lastToolResults.length === 0) return call("dispatch", { tasks: Array.from({ length: 4 }, (_, i) => ({ agent: "worker", task: `leaf ${i} delay=30` })) });
				if (turn.lastToolResults[0]!.name === "dispatch") return call("yield", { data: { children: turn.lastToolResults[0]!.text.split("[tsuna-result").length - 1 } });
			}
			return worker(turn);
		});
		const r = await rig({ config: treeConfig() });
		const finished = await Promise.race([r.harness.runtime.promptPrimary("go").then(() => "done"), Bun.sleep(20_000).then(() => "deadlock")]);
		expect(finished).toBe("done");
		const leads = r.harness.runtime.records().filter(x => x.role === "lead");
		expect(leads.map(l => (l.results[0]?.data as { children: number }).children)).toEqual([4, 4, 4, 4]);
		expect(r.harness.runtime.records().filter(x => x.role === "worker")).toHaveLength(16);
		expect(r.harness.runtime.diagnostics()).toMatchObject({ permitsInFlight: 0, waiting: 0 });
	});

	test("maximum depth is enforced (a depth-2 lead cannot spawn)", async () => {
		const r = await rig({ config: treeConfig({ scheduler: { maxConcurrency: 4, maxDepth: 1 } }) });
		const rt = r.harness.runtime;
		setScript(worker);
		const { id } = rt.spawn(rt.primaryId, { agent: "lead", task: "x" });
		expect(rt.record(id)!.contract.tools).not.toContain("task");
		expect(rt.record(id)!.contract.spawns).toEqual([]);
		expect(() => rt.spawn(id, { agent: "worker", task: "y" })).toThrow(/may not spawn/);
		await rt.quiesce();
	});

	test("startup failure and cancellation release permits; queued work proceeds", async () => {
		let failNext = 2;
		const createSession = async (spec: BackendSessionSpec) => {
			if (spec.systemPrompt.includes("Tsuna-Role: worker") && failNext-- > 0) throw new Error("simulated startup failure");
			return createBackendSession(spec);
		};
		setScript(worker);
		const r = await rig({ config: treeConfig(), createSession });
		const rt = r.harness.runtime;
		const human = { kind: "human" as const, rootId: rt.rootId };
		// 8 spawns: two fail at startup, four run, two wait for a permit.
		const spawned = Array.from({ length: 8 }, (_, i) => rt.spawn(rt.primaryId, { agent: "worker", task: `w${i} delay=150` }));
		await Bun.sleep(20);
		// Cancel one running and one still queued for a permit.
		const queued = spawned.find(s => rt.record(s.id)!.status === "queued")!;
		const running = spawned.find(s => rt.record(s.id)!.status === "running")!;
		await rt.cancel(human, queued.id);
		await rt.cancel(human, running.id);
		await Promise.allSettled(spawned.map(s => s.done));
		await rt.quiesce();
		const states = spawned.map(s => rt.record(s.id)!.status);
		expect(states.filter(s => s === "failed")).toHaveLength(2);
		expect(states.filter(s => s === "cancelled")).toHaveLength(2);
		expect(states.filter(s => s === "idle")).toHaveLength(4);
		expect(rt.diagnostics()).toMatchObject({ permitsInFlight: 0, waiting: 0 });
		const failed = spawned.map(s => rt.record(s.id)!).filter(x => x.status === "failed");
		expect(failed[0]!.failure).toContain("simulated startup failure");
	});

	test("one failing child does not erase successful sibling results", async () => {
		setScript(turn => {
			if (turn.role === "root") {
				if (turn.lastToolResults.length === 0) return call("dispatch", { tasks: [{ agent: "worker", task: "a" }, { agent: "worker", task: "b fail" }, { agent: "nobody", task: "c" }, { agent: "worker", task: "d" }] });
				return { text: turn.lastToolResults[0]!.text };
			}
			return worker(turn);
		});
		const r = await rig({ config: treeConfig() });
		await r.harness.runtime.promptPrimary("go");
		const dispatchText = r.harness.runtime.read({ kind: "human", rootId: r.harness.runtime.rootId }, r.harness.runtime.primaryId);
		if (typeof dispatchText === "string") throw new Error(dispatchText);
		const last = dispatchText.transcript.at(-1)!.text;
		expect(last).toContain("rejected nobody");
		expect(last.match(/ success/g)).toHaveLength(2);
		expect(last).toContain("worker failed on purpose");
		const workers = r.harness.runtime.records().filter(x => x.role === "worker");
		expect(workers.map(w => w.results[0]!.status).sort()).toEqual(["failure", "success", "success"]);
	});
});

describe("messaging and lifecycle", () => {
	test("steering reaches a running child's current run", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const { id, done } = rt.spawn(rt.primaryId, { agent: "worker", task: "steerable delay=300" });
		await Bun.sleep(80);
		expect(rt.record(id)!.status).toBe("running");
		const sent = await rt.send({ kind: "human", rootId: rt.rootId }, id, "focus on src/", "steer");
		expect(sent.outcome).toBe("delivered");
		const result = await done;
		expect((result!.data as { steers: string[] }).steers.join()).toContain("focus on src/");
		expect(rt.record(id)!.results).toHaveLength(1);
	});

	test("steering an idle child is queued for its next run; agents may only control direct children", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "a" });
		const b = rt.spawn(rt.primaryId, { agent: "worker", task: "b" });
		await Promise.all([a.done, b.done]);
		expect((await rt.send({ kind: "human", rootId: rt.rootId }, a.id, "later", "steer")).outcome).toBe("queued");
		const sibling = await rt.send({ kind: "agent", agentId: a.id }, b.id, "hi", "followup");
		expect(sibling.outcome).toBe("rejected");
		expect(sibling.reason).toContain("only the parent");
		expect((await rt.interrupt({ kind: "agent", agentId: a.id }, b.id)).ok).toBe(false);
		expect((await rt.cancel({ kind: "agent", agentId: a.id }, b.id)).ok).toBe(false);
		const follow = await rt.send({ kind: "agent", agentId: rt.primaryId }, a.id, "next please", "followup");
		expect(follow.outcome).toBe("delivered");
		await rt.quiesce();
		const second = rt.record(a.id)!.results[1]!;
		expect((second.data as { steers: string[] }).steers.join()).toContain("later");
	});

	test("follow-up continues the same child session and resets run state", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const { id, done } = rt.spawn(rt.primaryId, { agent: "worker", task: "first" });
		await done;
		const file = rt.record(id)!.sessionFile;
		expect(file).toBeTruthy();
		expect((await rt.send({ kind: "human", rootId: rt.rootId }, id, "second", "followup")).outcome).toBe("delivered");
		await rt.quiesce();
		const record = rt.record(id)!;
		expect(record.sessionFile).toBe(file);
		expect(record.results.map(x => x.resultId)).toEqual([`${id}/r1`, `${id}/r2`]);
		expect((record.results[1]!.data as { assistantTurns: number }).assistantTurns).toBeGreaterThan(0);
		expect((record.results[1]!.data as { task: string }).task).toContain("second");
	});

	test("interrupt keeps the session; cancel is permanent and rejects stale updates", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const human = { kind: "human" as const, rootId: rt.rootId };
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "slow delay=2000" });
		await Bun.sleep(80);
		expect((await rt.interrupt(human, a.id)).ok).toBe(true);
		expect(rt.record(a.id)!.status).toBe("idle");
		expect(rt.record(a.id)!.results[0]).toMatchObject({ status: "failure", interrupted: true });
		expect((await rt.send(human, a.id, "quick", "followup")).outcome).toBe("delivered");
		await rt.quiesce();
		expect(rt.record(a.id)!.results[1]!.status).toBe("success");

		const b = rt.spawn(rt.primaryId, { agent: "worker", task: "slow delay=400" });
		await Bun.sleep(80);
		await rt.cancel(human, b.id);
		// The provider's late answer (after 400 ms) must not resurrect or overwrite the agent.
		await Bun.sleep(600);
		const rec = rt.record(b.id)!;
		expect(rec.status).toBe("cancelled");
		expect(rec.results).toHaveLength(1);
		expect(rec.results[0]!.error).toBe("cancelled");
		expect(rt.yieldResult(b.id, "late", { data: 1 }).isError).toBe(true);
		expect((await rt.send(human, b.id, "again", "followup")).outcome).toBe("rejected");
		expect((await rt.resume(human, b.id)).outcome).toBe("rejected");
	});

	test("concurrent park and revive are safe and coalesce", async () => {
		let created = 0;
		const createSession = async (spec: BackendSessionSpec) => {
			if (spec.systemPrompt.includes("Tsuna-Role: worker")) created++;
			await Bun.sleep(30);
			return createBackendSession(spec);
		};
		setScript(worker);
		const r = await rig({ config: treeConfig(), createSession });
		const rt = r.harness.runtime;
		const human = { kind: "human" as const, rootId: rt.rootId };
		const { id, done } = rt.spawn(rt.primaryId, { agent: "worker", task: "x" });
		await done;
		expect(created).toBe(1);
		// Park while two revivals race it: park detaches first, both revivals share one session.
		const [park, r1, r2] = await Promise.all([rt.park(id, human), rt.resume(human, id), rt.resume(human, id)]);
		expect(park.ok).toBe(true);
		expect([r1.outcome, r2.outcome]).toEqual(["revived", "revived"]);
		expect(created).toBe(2);
		expect(rt.record(id)!.status).toBe("idle");
		// A park requested while a revival is in flight is refused rather than disposing it.
		await rt.park(id, human);
		const revive = rt.resume(human, id);
		const parkDuring = await rt.park(id, human);
		expect(parkDuring.ok).toBe(false);
		expect((await revive).outcome).toBe("revived");
		expect(rt.diagnostics().liveSessions).toBe(2); // primary + worker
	});

	test("a terminal yield mixed with other tool calls is rejected; siblings still run", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const { done } = rt.spawn(rt.primaryId, { agent: "worker", task: "mixed" });
		const result = await done;
		expect(result!.status).toBe("success");
		expect(result!.data).toMatchObject({ mixedRejected: true });
		expect((result!.data as { early?: boolean }).early).toBeUndefined();
	});

	test("a child that never yields gets bounded reminders, then a failure result", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const { id, done } = rt.spawn(rt.primaryId, { agent: "worker", task: "noyield" });
		const result = await done;
		expect(result).toMatchObject({ status: "failure", error: "child settled without a terminal yield" });
		const view = rt.read({ kind: "human", rootId: rt.rootId }, id);
		if (typeof view === "string") throw new Error(view);
		expect(view.transcript.filter(m => m.text.startsWith("[tsuna] Your assignment")).length).toBe(2);
	});
});

describe("results, delivery and waiting", () => {
	test("background results are delivered once via wait; wait is ownership-scoped and bounded", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "a delay=100" });
		const b = rt.spawn(rt.primaryId, { agent: "lead", task: "lead with no script" });
		await expect(rt.wait(a.id, { ids: [b.id], timeoutMs: 100 })).rejects.toThrow(/not a child/);
		const t0 = Date.now();
		const first = await rt.wait(rt.primaryId, { ids: [a.id], timeoutMs: 5000 });
		expect(first.reason).toBe("ready");
		expect(first.results).toHaveLength(1);
		expect(first.results[0]).toContain(`[tsuna-result ${a.id}/r1]`);
		expect(Date.now() - t0).toBeLessThan(3000);
		const again = await rt.wait(rt.primaryId, { ids: [a.id], timeoutMs: 200 });
		expect(again.results).toHaveLength(0);
		expect(again.reason).toContain("nothing to wait for");
		await Promise.allSettled([b.done]);
		const nothing = await rt.wait(b.id, { timeoutMs: 50 });
		expect(nothing.reason).toContain("nothing to wait for");
	});

	test("wait returns partial status at its bound without cancelling the child", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "slow delay=800" });
		const t0 = Date.now();
		const w = await rt.wait(rt.primaryId, { timeoutMs: 150 });
		expect(w.reason).toBe("timeout");
		expect(Date.now() - t0).toBeLessThan(700);
		expect(w.status[0]).toMatchObject({ id: a.id, status: "running" });
		await a.done;
		expect(rt.record(a.id)!.results[0]!.status).toBe("success");
	});

	test("results completing while the parent runs are steered in exactly once", async () => {
		let parentTurns = 0;
		setScript(turn => {
			if (turn.role === "root") {
				parentTurns++;
				if (turn.lastToolResults.length === 0) return call("task", { agent: "worker", task: "bg delay=50", background: true });
				if (parentTurns === 2) return { delayMs: 400, ...call("glob", { pattern: "*" }) };
				return { text: `saw: ${turn.userTexts.filter(t => t.includes("[tsuna-result")).length}` };
			}
			return worker(turn);
		});
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		await rt.promptPrimary("go");
		await rt.quiesce();
		const child = rt.records().find(x => x.role === "worker")!;
		expect(child.results[0]!.delivery).toMatchObject({ state: "delivered", via: "steer" });
		const transcript = readFileSync(rt.record(rt.primaryId)!.sessionFile!, "utf8");
		expect(transcript.split(`[tsuna-result ${child.id}/r1]`).length - 1).toBe(1);
		// Nothing left to deliver on the next prompt.
		await rt.promptPrimary("anything new?");
		const after = readFileSync(rt.record(rt.primaryId)!.sessionFile!, "utf8");
		expect(after.split(`[tsuna-result ${child.id}/r1]`).length - 1).toBe(1);
	});

	test("results of children that finish while the parent is idle are injected into its next prompt", async () => {
		setScript(worker);
		const r = await rig({ config: treeConfig() });
		const rt = r.harness.runtime;
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "idle-parent" });
		await a.done;
		expect(rt.record(a.id)!.results[0]!.delivery.state).toBe("pending");
		setScript(turn => (turn.role === "root" ? { text: "ok" } : worker(turn)));
		await rt.promptPrimary("hello");
		expect(rt.record(a.id)!.results[0]!.delivery).toMatchObject({ state: "delivered", via: "prompt" });
	});
});

describe("restart recovery", () => {
	test("restart restores the tree and contract; revival keeps original permissions", async () => {
		setScript(worker);
		const state = tempDir("tsuna-state-");
		const workspace = tempDir("tsuna-ws-");
		const config = treeConfig();
		const first = await startRig({ config, state, workspace });
		const rt = first.harness.runtime;
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "before restart" });
		await a.done;
		const before = structuredClone(rt.record(a.id)!);
		const rootId = rt.rootId;
		// A second process cannot open the same root while the first owns it.
		await expect(startRig({ config, state, workspace, resume: rootId })).rejects.toThrow(/owned by live process|owned/);
		await first.harness.shutdown();

		setScript(turn => {
			if (turn.role === "worker" && turn.lastToolResults.length === 0) {
				// After revival, try a denied command and an out-of-catalog tool.
				return { toolCalls: [{ name: "bash", arguments: { command: "rm -rf /tmp/x" } }, { name: "task", arguments: { agent: "worker", task: "x" } }] };
			}
			if (turn.role === "worker") return call("yield", { data: { results: turn.lastToolResults.map(r => r.text) } });
			return { text: "ok" };
		});
		const second = await rig({ config, state, workspace, resume: rootId });
		const rt2 = second.harness.runtime;
		const restored = rt2.record(a.id)!;
		expect(restored.status).toBe("parked");
		expect(restored.contract).toEqual(before.contract);
		expect(restored.contractHash).toBe(before.contractHash);
		expect(restored.parentId).toBe(rt2.primaryId);
		const human = { kind: "human" as const, rootId };
		expect((await rt2.resume(human, a.id, "continue")).outcome).toBe("revived");
		await rt2.quiesce();
		const data = rt2.record(a.id)!.results.at(-1)!.data as { results: string[] };
		expect(data.results[0]).toContain("Permission denied");
		expect(data.results[1]).toMatch(/Tool task not found|not in this agent's catalog/);
		expect(rt2.record(a.id)!.sessionFile).toBe(before.sessionFile);
	});

	test("in-flight runs become recoverable interruptions and are never replayed", async () => {
		let workerCalls = 0;
		setScript(turn => {
			if (turn.role === "worker") workerCalls++;
			return worker(turn);
		});
		const state = tempDir("tsuna-state-");
		const workspace = tempDir("tsuna-ws-");
		const config = treeConfig();
		const first = await startRig({ config, state, workspace });
		const rt = first.harness.runtime;
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "long delay=5000" });
		await Bun.sleep(100);
		// Simulate a crash: persist "running" and release the lock without settling.
		const record = rt.record(a.id)!;
		const file = join(rt.store.recordsDir, `${a.id}.json`);
		const snapshot = readFileSync(file, "utf8");
		await first.harness.shutdown();
		writeFileSync(file, snapshot);
		expect(JSON.parse(snapshot).status).toBe("running");
		const callsBefore = workerCalls;
		const second = await rig({ config, state, workspace, resume: rt.rootId });
		const restored = second.harness.runtime.record(a.id)!;
		expect(restored.status).toBe("parked");
		expect(restored.interruptedRunId).toBe(record.currentRunId);
		expect(restored.results.at(-1)).toMatchObject({ status: "failure", interrupted: true });
		await Bun.sleep(200);
		expect(workerCalls).toBe(callsBefore);
	});

	test("missing or corrupt recovery metadata fails closed; transcripts stay readable", async () => {
		setScript(worker);
		const state = tempDir("tsuna-state-");
		const workspace = tempDir("tsuna-ws-");
		const config = treeConfig();
		const first = await startRig({ config, state, workspace });
		const rt = first.harness.runtime;
		const a = rt.spawn(rt.primaryId, { agent: "worker", task: "tamper me" });
		const b = rt.spawn(rt.primaryId, { agent: "worker", task: "corrupt me" });
		const c = rt.spawn(rt.primaryId, { agent: "worker", task: "lose my model" });
		await Promise.all([a.done, b.done, c.done]);
		await first.harness.shutdown();
		const dir = rt.store.recordsDir;
		// a: widen its permissions on disk (hash no longer matches)
		const ra = JSON.parse(readFileSync(join(dir, `${a.id}.json`), "utf8"));
		ra.contract.permissions.push({ effect: "allow", action: "shell", resource: "*" });
		writeFileSync(join(dir, `${a.id}.json`), JSON.stringify(ra));
		// b: corrupt JSON
		writeFileSync(join(dir, `${b.id}.json`), "{ not json");
		// c: its model entry now points elsewhere
		const config2 = structuredClone(config);
		config2.providers.models.fast = { ...config2.providers.models.fast!, id: "different-model" };
		config2.providers.models.primary = config.providers.models.primary!;
		const second = await rig({ config: config2, state, workspace, resume: rt.rootId });
		const rt2 = second.harness.runtime;
		const human = { kind: "human" as const, rootId: rt.rootId };
		expect(rt2.record(a.id)!.status).toBe("failed");
		expect(rt2.record(a.id)!.failure).toContain("contract hash mismatch");
		expect(rt2.record(b.id)).toBeUndefined();
		expect(rt2.warnings.some(w => w.includes("corrupt agent record"))).toBe(true);
		expect(rt2.record(c.id)!.status).toBe("failed");
		expect(rt2.record(c.id)!.failure).toContain("now resolves to");
		expect((await rt2.resume(human, a.id, "go")).outcome).toBe("rejected");
		const view = rt2.read(human, a.id);
		if (typeof view === "string") throw new Error(view);
		expect(view.transcript.length).toBeGreaterThan(0);
	});
});

describe("isolation between root sessions", () => {
	test("cross-session messaging and transcript reads are rejected", async () => {
		setScript(worker);
		const state = tempDir("tsuna-state-");
		const one = await rig({ config: treeConfig(), state });
		const two = await rig({ config: treeConfig(), state });
		const a = one.harness.runtime.spawn(one.harness.runtime.primaryId, { agent: "worker", task: "a" });
		await a.done;
		const rt2 = two.harness.runtime;
		expect((await rt2.send({ kind: "agent", agentId: rt2.primaryId }, a.id, "hi", "followup")).outcome).toBe("rejected");
		expect(rt2.read({ kind: "agent", agentId: rt2.primaryId }, a.id)).toContain("unknown agent");
		expect(one.harness.runtime.read({ kind: "human", rootId: rt2.rootId }, a.id)).toContain("another session tree");
		const viaTool = await rt2.invokeTool(rt2.primaryId, "agent_read", "t1", { id: a.id });
		expect(viaTool.isError).toBe(true);
	});
});

test("tool call / result pairing stays valid in stored transcripts", async () => {
	setScript(worker);
	const r = await rig({ config: treeConfig() });
	const rt = r.harness.runtime;
	const { id, done } = rt.spawn(rt.primaryId, { agent: "worker", task: "mixed" });
	await done;
	const view = rt.read({ kind: "human", rootId: rt.rootId }, id);
	if (typeof view === "string") throw new Error(view);
	const lines = readFileSync(rt.record(id)!.sessionFile!, "utf8").trim().split("\n").map(l => JSON.parse(l));
	const messages = lines.filter(l => l.type === "message").map(l => l.message);
	expect(checkToolPairing(messages)).toEqual([]);
});

test("invalid role or model selections are rejected before any work launches", async () => {
	const dir = tempDir("tsuna-pack-");
	const { writePack, ORCH_RULES, ORCH_TOOLS } = await import("./helpers/pack.ts");
	const pack = writePack(dir, "bad", [
		{ name: "root", model: "primary", primary: true, tools: [...ORCH_TOOLS], spawns: ["ghost-model", "too-deep"], permissions: ORCH_RULES },
		{ name: "ghost-model", model: "no-such-entry" },
		{ name: "too-deep", model: "strong", reasoning: { default: "xhigh" } },
	]);
	const config = testConfig({ agentPacks: [pack], primaryRole: "root" });
	config.providers.models.strong = { ...config.providers.models.strong!, reasoningLevels: ["low", "medium", "high"] };
	setScript(worker);
	const r = await rig({ config });
	const rt = r.harness.runtime;
	expect(() => rt.spawn(rt.primaryId, { agent: "ghost-model", task: "x" })).toThrow(/unknown model entry/);
	expect(() => rt.spawn(rt.primaryId, { agent: "too-deep", task: "x" })).toThrow(/does not support reasoning level xhigh/);
	expect(() => rt.spawn(rt.primaryId, { agent: "orchestrator", task: "x" })).toThrow(/may not spawn/);
	expect(rt.records()).toHaveLength(1);
	expect(rt.diagnostics().permitsInFlight).toBe(0);
});
