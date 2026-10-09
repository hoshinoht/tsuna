import { afterEach, expect, test } from "bun:test";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { DEFAULT_SUPERVISOR } from "../src/config.ts";
import { SupervisorClient } from "../src/jobs/supervisor.ts";
import { setScript, startRig, tempDir, testConfig, type TestRig } from "./helpers/harness.ts";
import { writePack } from "./helpers/pack.ts";

const built = existsSync(DEFAULT_SUPERVISOR);
const rigs: TestRig[] = [];
afterEach(async () => {
	for (const r of rigs.splice(0)) await r.harness.shutdown();
});

/** Alive and not a zombie (containers may not reap re-parented processes promptly). */
function alive(pid: number) {
	try {
		const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
		return stat.slice(stat.lastIndexOf(") ") + 2, stat.lastIndexOf(") ") + 3) !== "Z";
	} catch {
		try {
			process.kill(pid, 0);
			return process.platform !== "linux";
		} catch {
			return false;
		}
	}
}

test.skipIf(!built)("exit, timeout, cancellation and startup failure stay distinct states", async () => {
	const s = new SupervisorClient(DEFAULT_SUPERVISOR, tempDir("tsuna-jobs-"));
	const cwd = tempDir("tsuna-cwd-");
	const ok = await s.start({ id: "ok", cwd, timeoutSeconds: 5, command: ["/bin/sh", "-c", "exit 124"] });
	expect(await s.wait(ok.run_directory, 5)).toMatchObject({ state: "completed", exit_code: 124 });
	const slow = await s.start({ id: "slow", cwd, timeoutSeconds: 1, command: ["/bin/sh", "-c", "sleep 10"] });
	expect((await s.wait(slow.run_directory, 5)).state).toBe("timed_out");
	const cancel = await s.start({ id: "cancel", cwd, timeoutSeconds: 30, command: ["/bin/sh", "-c", "sleep 30"] });
	await s.cancel(cancel.run_directory);
	expect((await s.wait(cancel.run_directory, 5)).state).toBe("cancelled");
	const missing = await s.start({ id: "missing", cwd: join(cwd, "nope"), timeoutSeconds: 5, command: ["/bin/true"] });
	expect((await s.wait(missing.run_directory, 5)).state).toBe("startup_failed");
	// A wait timeout neither cancels nor restarts the producer.
	const long = await s.start({ id: "long", cwd, timeoutSeconds: 30, command: ["/bin/sh", "-c", "sleep 2; echo done"] });
	const early = await s.wait(long.run_directory, 1);
	expect(early.state).toBe("running");
	expect((await s.wait(long.run_directory, 10)).state).toBe("completed");
	expect(s.tail(long.run_directory, "stdout")).toBe("done\n");
}, 30_000);

test.skipIf(!built)("a killed supervisor reports unknown promptly and never fabricates a result; PID reuse cannot fake liveness", async () => {
	const s = new SupervisorClient(DEFAULT_SUPERVISOR, tempDir("tsuna-jobs-"));
	const cwd = tempDir("tsuna-cwd-");
	const pidFile = join(cwd, "victim.pid");
	const job = await s.start({ id: "victim", cwd, timeoutSeconds: 60, command: ["/bin/sh", "-c", `echo $$ > ${pidFile}; exec sleep 30`] });
	const status = await s.status(job.run_directory);
	expect(status.state).toBe("running");
	process.kill(status.supervisor_pid!, "SIGKILL");
	await Bun.sleep(100);
	const t0 = Date.now();
	const after = await s.wait(job.run_directory, 30);
	expect(Date.now() - t0).toBeLessThan(3000);
	expect(after.state).toBe("unknown");
	expect(after.exit_code).toBeNull();
	// Simulated PID reuse: a live process id with a different birth identity.
	const record = JSON.parse(readFileSync(join(job.run_directory, "status.json"), "utf8"));
	record.supervisor_pid = process.pid;
	writeFileSync(join(job.run_directory, "status.json"), JSON.stringify(record));
	expect((await s.status(job.run_directory)).state).toBe("unknown");
	// The orphaned command may still be running: unknown does not mean stopped. Clean up exactly it.
	const orphan = Number(readFileSync(pidFile, "utf8").trim());
	expect(alive(orphan)).toBe(true);
	process.kill(orphan, "SIGKILL");
}, 30_000);

test.skipIf(!built)("a caller restart observes a surviving supervised command without replaying it", async () => {
	const dir = tempDir("tsuna-pack-");
	const pack = writePack(dir, "jobs", [
		{ name: "boss", model: "primary", primary: true, tools: ["bash", "job_status", "job_wait"], permissions: [["deny", "*", "*"], ["allow", "shell", "*"]] },
	]);
	// Auto mode: ordinary compound commands need no approval (destructive guards remain).
	const config = testConfig({ agentPacks: [pack], primaryRole: "boss", approvalMode: "auto" });
	const state = tempDir("tsuna-state-");
	const workspace = tempDir("tsuna-ws-");
	setScript(() => ({ text: "ok" }));
	const first = await startRig({ config, state, workspace });
	const rt = first.harness.runtime;
	const marker = join(workspace, "runs.log");
	const started = await rt.invokeTool(rt.primaryId, "bash", "bg-1", { command: `echo run >> ${marker}; sleep 1; echo finished`, background: true, timeout_seconds: 30 });
	expect(started.text).toContain("Started job");
	const jobId = rt.record(rt.primaryId)!.jobs[0]!.jobId;
	await first.harness.shutdown();

	const second = await startRig({ config, state, workspace, resume: rt.rootId });
	rigs.push(second);
	const rt2 = second.harness.runtime;
	expect(rt2.record(rt2.primaryId)!.jobs.map(j => j.jobId)).toEqual([jobId]);
	const waited = await rt2.invokeTool(rt2.primaryId, "job_wait", "w-1", { id: jobId, timeout_seconds: 10 });
	expect(waited.text).toContain("completed");
	expect(waited.text).toContain("finished");
	// Re-issuing the original tool call id does not start the command again.
	const again = await rt2.invokeTool(rt2.primaryId, "bash", "bg-1", { command: `echo run >> ${marker}`, background: true });
	expect(again.text).toContain("already existed; not started again");
	await Bun.sleep(200);
	expect(readFileSync(marker, "utf8")).toBe("run\n");
	// Another agent's job id is not visible.
	expect((await rt2.invokeTool(rt2.primaryId, "job_status", "s-1", { id: "someone-else" })).text).toContain("not owned");
}, 30_000);

test("a pending supervisor wait is released on abort without cancelling the job", async () => {
	if (!built) return;
	const s = new SupervisorClient(DEFAULT_SUPERVISOR, tempDir("tsuna-jobs-"));
	const job = await s.start({ id: "abort-wait", cwd: tempDir("tsuna-cwd-"), timeoutSeconds: 10, command: ["/bin/sh", "-c", "sleep 1"] });
	const controller = new AbortController();
	setTimeout(() => controller.abort(), 100);
	const t0 = Date.now();
	const status = await s.wait(job.run_directory, 10, controller.signal);
	expect(Date.now() - t0).toBeLessThan(900);
	expect(status.state).toBe("running");
	expect((await s.wait(job.run_directory, 5)).state).toBe("completed");
	const pid = status.supervisor_pid!;
	await Bun.sleep(100);
	expect(alive(pid)).toBe(false);
}, 30_000);
