/**
 * Stage 6: native-backed built-in tools (read/edit/write/grep/glob) and the
 * supervised `bash` tool, invoked through the real runtime and gate.
 */
import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { DEFAULT_SUPERVISOR } from "../src/config.ts";
import { isInside } from "../src/paths.ts";
import { nativesDirInUse } from "../src/tools/natives.ts";
import { setScript, startRig, type TestRig } from "./helpers/harness.ts";

const rigs: TestRig[] = [];
afterEach(async () => {
	for (const rig of rigs.splice(0)) await rig.harness.shutdown();
});

async function rig(opts: Parameters<typeof startRig>[0] = {}) {
	const r = await startRig(opts);
	rigs.push(r);
	return r;
}

// Children must satisfy their role's output schema, or the fixture would retry forever.
const yieldNow = (turn: { role: string }) => ({
	toolCalls: [{ name: "yield", arguments: { data: turn.role === "tester" ? { status: "PASS", checks: [] } : { answer: "x", findings: [] } } }],
});

function tagOf(text: string): string {
	const tag = /^\[[^\]#]+#([^\]]+)\]/.exec(text)?.[1];
	if (!tag) throw new Error(`no tag header in: ${text.slice(0, 80)}`);
	return tag;
}

describe("native file tools", () => {
	test("read returns a [path#TAG] header; edit with the current tag succeeds", async () => {
		setScript(yieldNow);
		const nativesBefore = nativesDirInUse();
		const r = await rig();
		const rt = r.harness.runtime;
		const file = join(r.workspace, "a.txt");
		writeFileSync(file, "alpha\nbeta\n");
		const out = await rt.invokeTool(rt.primaryId, "read", "r1", { path: "a.txt" });
		expect(out.isError).toBeFalsy();
		expect(out.text).toMatch(/^\[a\.txt#[0-9A-Za-z]+\.[0-9a-f]{16}\]\n/);
		expect(out.text).toContain("alpha");
		const tag = tagOf(out.text);
		expect((out.details as { tag: string }).tag).toBe(tag);
		// Same content yields the same tag.
		expect(tagOf((await rt.invokeTool(rt.primaryId, "read", "r1b", { path: "a.txt" })).text)).toBe(tag);

		const edit = await rt.invokeTool(rt.primaryId, "edit", "e1", { path: "a.txt", expected_tag: tag, edits: [{ old_text: "beta", new_text: "gamma" }] });
		expect(edit.isError).toBeFalsy();
		expect(edit.text).toContain("Edited");
		expect(readFileSync(file, "utf8")).toBe("alpha\ngamma\n");
		// The old tag is now stale.
		const again = await rt.invokeTool(rt.primaryId, "edit", "e2", { path: "a.txt", expected_tag: tag, edits: [{ old_text: "alpha", new_text: "x" }] });
		expect(again.isError).toBe(true);
		expect(again.text).toContain("Stale edit rejected");

		// pi-natives must not touch ~/.omp and must use a Tsuna state natives dir.
		const home = process.env.HOME!;
		expect(existsSync(join(home, ".omp"))).toBe(false);
		const dir = process.env.PI_NATIVES_DIR!;
		expect(dir).toBe(nativesDirInUse()!);
		expect(dir.endsWith("/natives")).toBe(true);
		expect(isInside(dir, home)).toBe(false);
		if (nativesBefore === undefined) {
			// First load in this process: it is exactly this rig's state.
			expect(dir).toBe(r.harness.paths.natives);
			expect(isInside(dir, r.state)).toBe(true);
		} else {
			// The addon is process-global: an earlier rig in this process loaded it.
			expect(dir).toBe(nativesBefore);
		}
	});

	test("an edit against content changed since the read is rejected and the file is unchanged", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		const file = join(r.workspace, "b.txt");
		writeFileSync(file, "one\ntwo\n");
		const tag = tagOf((await rt.invokeTool(rt.primaryId, "read", "r1", { path: "b.txt" })).text);
		writeFileSync(file, "one\ntwo\nthree (someone else)\n");
		const edit = await rt.invokeTool(rt.primaryId, "edit", "e1", { path: "b.txt", expected_tag: tag, edits: [{ old_text: "two", new_text: "TWO" }] });
		expect(edit.isError).toBe(true);
		expect(edit.text).toContain("Stale edit rejected");
		expect(readFileSync(file, "utf8")).toBe("one\ntwo\nthree (someone else)\n");
	});

	test("write needs the current tag to replace an existing file", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		const file = join(r.workspace, "c.txt");
		writeFileSync(file, "keep me\n");
		const blind = await rt.invokeTool(rt.primaryId, "write", "w1", { path: "c.txt", content: "clobbered" });
		expect(blind.isError).toBe(true);
		expect(blind.text).toContain("Stale or missing expected_tag");
		expect(readFileSync(file, "utf8")).toBe("keep me\n");
		const wrong = await rt.invokeTool(rt.primaryId, "write", "w2", { path: "c.txt", content: "clobbered", expected_tag: "0000.0000000000000000" });
		expect(wrong.isError).toBe(true);
		expect(readFileSync(file, "utf8")).toBe("keep me\n");
		const tag = tagOf((await rt.invokeTool(rt.primaryId, "read", "r1", { path: "c.txt" })).text);
		const ok = await rt.invokeTool(rt.primaryId, "write", "w3", { path: "c.txt", content: "replaced\n", expected_tag: tag });
		expect(ok.isError).toBeFalsy();
		expect(readFileSync(file, "utf8")).toBe("replaced\n");
		// New files need no tag.
		const fresh = await rt.invokeTool(rt.primaryId, "write", "w4", { path: "sub/new.txt", content: "new\n" });
		expect(fresh.isError).toBeFalsy();
		expect(readFileSync(join(r.workspace, "sub", "new.txt"), "utf8")).toBe("new\n");
	});

	test("grep and glob search the workspace", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		mkdirSync(join(r.workspace, "src", "deep"), { recursive: true });
		writeFileSync(join(r.workspace, "src", "one.ts"), "export const NEEDLE_42 = 1;\n");
		writeFileSync(join(r.workspace, "src", "deep", "two.ts"), "// nothing\nconst x = NEEDLE_42;\n");
		writeFileSync(join(r.workspace, "notes.md"), "no match here\n");
		const grep = await rt.invokeTool(rt.primaryId, "grep", "g1", { pattern: "NEEDLE_\\d+" });
		expect(grep.isError).toBeFalsy();
		expect(grep.text).toContain("one.ts:1:");
		expect(grep.text).toContain("two.ts:2:");
		expect(grep.text).not.toContain("notes.md");
		expect((grep.details as { total: number }).total).toBe(2);
		expect((await rt.invokeTool(rt.primaryId, "grep", "g2", { pattern: "ZZZ_NOT_THERE" })).text).toBe("No matches.");

		const glob = await rt.invokeTool(rt.primaryId, "glob", "f1", { pattern: "**/*.ts" });
		expect(glob.isError).toBeFalsy();
		const files = glob.text.split("\n").sort();
		expect(files).toHaveLength(2);
		expect(files.some(f => f.endsWith("one.ts"))).toBe(true);
		expect(files.some(f => f.endsWith("two.ts"))).toBe(true);
		expect((await rt.invokeTool(rt.primaryId, "glob", "f2", { pattern: "*.nothing" })).text).toBe("No files.");
	});
});

const HAS_SUPERVISOR = existsSync(DEFAULT_SUPERVISOR);
if (!HAS_SUPERVISOR) console.warn(`[natives-tools] skipping bash tests: supervisor binary missing at ${DEFAULT_SUPERVISOR} (build native/supervisor first)`);

describe("supervised bash", () => {
	test.skipIf(!HAS_SUPERVISOR)("foreground command completes with exit 0 and stdout", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		const out = await rt.invokeTool(rt.primaryId, "bash", "b1", { command: "echo hello" });
		expect(out.isError).toBe(false);
		expect(out.text).toContain(": completed");
		expect(out.text).toContain("exit_code: 0");
		expect(out.text).toContain("hello");
		expect(out.details).toMatchObject({ state: "completed", exit_code: 0 });
	});

	test.skipIf(!HAS_SUPERVISOR)("non-zero exit is reported as an error with its code", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		const out = await rt.invokeTool(rt.primaryId, "bash", "b2", { command: "exit 7" });
		expect(out.isError).toBe(true);
		expect(out.text).toContain("exit_code: 7");
		expect(out.details).toMatchObject({ state: "completed", exit_code: 7 });
	});

	test.skipIf(!HAS_SUPERVISOR)("deadline produces timed_out", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		const started = Date.now();
		const out = await rt.invokeTool(rt.primaryId, "bash", "b3", { command: "sleep 5", timeout_seconds: 1 });
		expect(out.isError).toBe(true);
		expect(out.text).toContain("timed_out");
		expect((out.details as { state: string }).state).toBe("timed_out");
		expect(Date.now() - started).toBeLessThan(4500);
	});

	test.skipIf(!HAS_SUPERVISOR)("background jobs: job_status/job_wait for the owner only", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		const bg = await rt.invokeTool(rt.primaryId, "bash", "bg-1", { command: "sleep 1", background: true });
		expect(bg.isError).toBeFalsy();
		const jobId = (bg.details as { job: string }).job;
		expect(bg.text).toContain(`Started job ${jobId}`);
		expect(rt.record(rt.primaryId)!.jobs.map(j => j.jobId)).toContain(jobId);

		const status = await rt.invokeTool(rt.primaryId, "job_status", "s1", { id: jobId });
		expect(status.isError).toBeFalsy();
		expect(status.text).toMatch(new RegExp(`job ${jobId}: (starting|running|completed)`));

		const waited = await rt.invokeTool(rt.primaryId, "job_wait", "w1", { id: jobId, timeout_seconds: 10 });
		expect(waited.isError).toBeFalsy();
		expect(waited.text).toContain(`job ${jobId}: completed`);
		expect(waited.text).toContain("exit_code: 0");

		// Another agent (tester has job_status/job_wait) cannot observe it.
		const tester = rt.spawn(rt.primaryId, { agent: "tester", task: "idle" });
		await tester.done;
		expect(rt.record(tester.id)!.contract.tools).toContain("job_status");
		const foreign = await rt.invokeTool(tester.id, "job_status", "s2", { id: jobId });
		expect(foreign.isError).toBe(true);
		expect(foreign.text).toContain("not owned");
		const foreignWait = await rt.invokeTool(tester.id, "job_wait", "w2", { id: jobId, timeout_seconds: 1 });
		expect(foreignWait.text).toContain("not owned");
		expect((await rt.invokeTool(rt.primaryId, "job_status", "s3", { id: "made-up" })).text).toContain("not owned");
	});

	test.skipIf(!HAS_SUPERVISOR)("a repeated tool call id never runs the command twice", async () => {
		setScript(yieldNow);
		const r = await rig();
		const rt = r.harness.runtime;
		writeFileSync(join(r.workspace, "append.sh"), "echo run >> log.txt\n");
		const first = await rt.invokeTool(rt.primaryId, "bash", "dup-1", { command: "sh append.sh" });
		expect(first.isError).toBe(false);
		const second = await rt.invokeTool(rt.primaryId, "bash", "dup-1", { command: "sh append.sh" });
		expect(second.isError).toBe(false);
		expect((second.details as { job: string }).job).toBe((first.details as { job: string }).job);
		expect(readFileSync(join(r.workspace, "log.txt"), "utf8")).toBe("run\n");
		// A different call id is a new run.
		await rt.invokeTool(rt.primaryId, "bash", "dup-2", { command: "sh append.sh" });
		expect(readFileSync(join(r.workspace, "log.txt"), "utf8")).toBe("run\nrun\n");
		// Background start with a reused id reports the existing job.
		const bg = await rt.invokeTool(rt.primaryId, "bash", "dup-1", { command: "sh append.sh", background: true });
		expect(bg.text).toContain("already existed; not started again");
		expect(readFileSync(join(r.workspace, "log.txt"), "utf8")).toBe("run\nrun\n");
	});
});
