/**
 * Client for the experiment-local Rust supervisor (`native/supervisor`).
 *
 * The supervisor owns external-process lifecycle; this client only starts,
 * observes, waits on and cancels runs under `<state>/jobs`. Lifecycle state is
 * read from the structured record, never inferred from numeric exit codes
 * (124/125 can also be a command's own exit status).
 */
import { existsSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

export type JobState = "starting" | "running" | "completed" | "timed_out" | "startup_failed" | "cancelled" | "unknown";

export interface JobStatus {
	id: string;
	run_directory: string;
	state: JobState;
	supervisor_pid: number | null;
	exit_code: number | null;
	signal: number | null;
	detail: string | null;
	started_at_ms: number;
	finished_at_ms: number | null;
}

export class SupervisorError extends Error {}

const SAFE_ENV = ["PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "TZ"];

/**
 * Environment for supervised commands: a small allowlist plus explicit
 * additions. Provider keys and other secrets in the harness environment are
 * not inherited by agent-run commands.
 */
export function scrubbedEnv(extra: Record<string, string> = {}, env: NodeJS.ProcessEnv = process.env): Record<string, string> {
	const out: Record<string, string> = {};
	for (const key of SAFE_ENV) if (env[key] !== undefined) out[key] = env[key]!;
	return { ...out, ...extra };
}

export const TERMINAL_JOB_STATES: ReadonlySet<JobState> = new Set(["completed", "timed_out", "startup_failed", "cancelled", "unknown"]);

export class SupervisorClient {
	constructor(
		readonly binary: string,
		/** State root; runs live in `<root>/jobs`. */
		readonly root: string,
	) {}

	available(): boolean {
		return existsSync(this.binary);
	}

	private async run(args: string[], signal?: AbortSignal, env?: Record<string, string>): Promise<{ code: number; stdout: string; stderr: string }> {
		if (!this.available()) throw new SupervisorError(`supervisor binary not built: ${this.binary} (run: bun run build:native)`);
		const proc = Bun.spawn([this.binary, "--root", this.root, "job", ...args], { stdout: "pipe", stderr: "pipe", stdin: "ignore", env: env ?? scrubbedEnv() });
		const onAbort = () => proc.kill("SIGTERM");
		signal?.addEventListener("abort", onAbort, { once: true });
		try {
			const [stdout, stderr, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
			return { code, stdout, stderr };
		} finally {
			signal?.removeEventListener("abort", onAbort);
		}
	}

	private parse<T>(out: { code: number; stdout: string; stderr: string }): T {
		const line = out.stdout.trim().split("\n").at(-1);
		if (!line) throw new SupervisorError(out.stderr.trim() || `supervisor exited ${out.code} without output`);
		try {
			return JSON.parse(line) as T;
		} catch {
			throw new SupervisorError(`unparseable supervisor output: ${line}`);
		}
	}

	/** Start (or, for an existing `id`, look up) a run. Never starts a second worker for one id. */
	async start(opts: { id: string; cwd: string; timeoutSeconds: number; command: string[]; env?: Record<string, string> }): Promise<{ id: string; run_directory: string; created: boolean }> {
		const out = await this.run(["start", "--timeout", String(opts.timeoutSeconds), "--id", opts.id, "--cwd", opts.cwd, "--", ...opts.command], undefined, opts.env);
		return this.parse(out);
	}

	async status(runDirectory: string): Promise<JobStatus> {
		return this.parse(await this.run(["status", runDirectory]));
	}

	/** Bounded wait; a wait timeout never cancels or restarts the producer. */
	async wait(runDirectory: string, timeoutSeconds: number, signal?: AbortSignal): Promise<JobStatus> {
		const out = await this.run(["wait", runDirectory, "--timeout", String(Math.max(1, Math.ceil(timeoutSeconds)))], signal);
		if (signal?.aborted) return this.status(runDirectory);
		return this.parse(out);
	}

	async cancel(runDirectory: string): Promise<JobStatus> {
		return this.parse(await this.run(["cancel", runDirectory]));
	}

	runDirectory(id: string): string {
		return join(this.root, "jobs", id);
	}

	/** Last `bytes` of a run log. */
	tail(runDirectory: string, stream: "stdout" | "stderr", bytes = 16_000): string {
		const file = join(runDirectory, `${stream}.log`);
		try {
			const size = statSync(file).size;
			const text = readFileSync(file, "utf8");
			return size > bytes ? `…[${size - bytes} bytes omitted]\n${text.slice(-bytes)}` : text;
		} catch {
			return "";
		}
	}
}
