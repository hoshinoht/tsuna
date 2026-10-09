/**
 * Built-in Tsuna tools. Policy has already been checked by the runtime before
 * `execute` runs; implementations re-resolve paths relative to the agent's
 * workspace and never take identity from input.
 */
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, isAbsolute, relative, resolve } from "node:path";
import { Type } from "typebox";
import { SupervisorClient, TERMINAL_JOB_STATES, type JobStatus } from "../jobs/supervisor.ts";
import { loadNatives } from "./natives.ts";
import { fail, ok, type ToolContext, type ToolImpl } from "./types.ts";

export interface BuiltinDeps {
	nativesDir: string;
	supervisor: SupervisorClient;
	/** Record/lookup external jobs owned by an agent (runtime-owned bookkeeping). */
	jobs: {
		record(agentId: string, job: { runDirectory: string; jobId: string; command: string; toolCallId: string; startedAt: number }): void;
		owned(agentId: string, jobId: string): string | undefined;
	};
}

function abs(ctx: ToolContext, path: string): string {
	return isAbsolute(path) ? resolve(path) : resolve(ctx.cwd, path);
}

/**
 * Content tag used for stale-edit rejection: the pi-natives hashline tag
 * (4 hex) plus a sha256 prefix, so a coincidental 16-bit collision cannot
 * authorise an edit against changed content.
 */
export async function contentTag(nativesDir: string, text: string): Promise<string> {
	const natives = await loadNatives(nativesDir);
	return `${natives.hashlineFileHash(text)}.${createHash("sha256").update(text).digest("hex").slice(0, 16)}`;
}

function jobSummary(status: JobStatus, supervisor: SupervisorClient): string {
	const lines = [`job ${status.id}: ${status.state}`];
	if (status.state === "completed") lines.push(`exit_code: ${status.exit_code ?? `signal ${status.signal}`}`);
	if (status.state === "unknown") lines.push("outcome unknown: the supervisor is gone without a terminal record; the command may still be running and was NOT replayed");
	if (status.detail) lines.push(`detail: ${status.detail}`);
	const out = supervisor.tail(status.run_directory, "stdout");
	const err = supervisor.tail(status.run_directory, "stderr");
	if (out) lines.push(`--- stdout ---\n${out}`);
	if (err) lines.push(`--- stderr ---\n${err}`);
	return lines.join("\n");
}

export function builtinTools(deps: BuiltinDeps): Map<string, ToolImpl> {
	const { supervisor } = deps;
	const tools: ToolImpl[] = [
		{
			name: "read",
			description: "Read a text file. Returns a content tag ([path#TAG]) that edit/write require to prove the file has not changed since you read it.",
			parameters: Type.Object({
				path: Type.String(),
				offset: Type.Optional(Type.Integer({ minimum: 1 })),
				limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 5000 })),
			}),
			async execute(ctx, input) {
				const path = abs(ctx, String(input.path));
				if (!existsSync(path)) return fail(`No such file: ${path}`);
				const text = readFileSync(path, "utf8");
				const natives = await loadNatives(deps.nativesDir);
				const tag = await contentTag(deps.nativesDir, text);
				const lines = text.split("\n");
				const start = ((input.offset as number | undefined) ?? 1) - 1;
				const limit = (input.limit as number | undefined) ?? 2000;
				const slice = lines.slice(start, start + limit).join("\n");
				const body = natives.hashlineFormatNumberedLines(slice, start + 1);
				const more = start + limit < lines.length ? `\n… ${lines.length - start - limit} more lines (use offset)` : "";
				return ok(`[${relative(ctx.cwd, path) || path}#${tag}]\n${body}${more}`, { path, tag });
			},
		},
		{
			name: "write",
			description: "Create a file, or replace an existing file when expected_tag matches its current content tag.",
			parameters: Type.Object({ path: Type.String(), content: Type.String(), expected_tag: Type.Optional(Type.String()) }),
			async execute(ctx, input) {
				const path = abs(ctx, String(input.path));
				if (existsSync(path)) {
					const current = await contentTag(deps.nativesDir, readFileSync(path, "utf8"));
					if (input.expected_tag !== current) return fail(`Stale or missing expected_tag for existing file ${path}; read it again (current tag ${current}).`);
				}
				mkdirSync(dirname(path), { recursive: true });
				writeFileSync(path, String(input.content));
				(await loadNatives(deps.nativesDir)).invalidateFsScanCache(path);
				return ok(`Wrote ${path} (tag ${await contentTag(deps.nativesDir, String(input.content))})`);
			},
		},
		{
			name: "edit",
			description: "Replace exact text in a file. expected_tag must equal the tag from your latest read; stale edits are rejected.",
			parameters: Type.Object({
				path: Type.String(),
				expected_tag: Type.String(),
				edits: Type.Array(Type.Object({ old_text: Type.String(), new_text: Type.String(), replace_all: Type.Optional(Type.Boolean()) }), { minItems: 1 }),
			}),
			async execute(ctx, input) {
				const path = abs(ctx, String(input.path));
				if (!existsSync(path)) return fail(`No such file: ${path}`);
				let text = readFileSync(path, "utf8");
				const current = await contentTag(deps.nativesDir, text);
				if (input.expected_tag !== current) return fail(`Stale edit rejected: ${path} changed since it was read (current tag ${current}). Read it again.`);
				for (const [index, edit] of (input.edits as { old_text: string; new_text: string; replace_all?: boolean }[]).entries()) {
					const count = edit.old_text ? text.split(edit.old_text).length - 1 : 0;
					if (count === 0) return fail(`edits[${index}]: old_text not found; no changes written`);
					if (count > 1 && !edit.replace_all) return fail(`edits[${index}]: old_text matches ${count} times; add context or set replace_all`);
					text = edit.replace_all ? text.split(edit.old_text).join(edit.new_text) : text.replace(edit.old_text, () => edit.new_text);
				}
				writeFileSync(path, text);
				(await loadNatives(deps.nativesDir)).invalidateFsScanCache(path);
				return ok(`Edited ${path} (new tag ${await contentTag(deps.nativesDir, text)})`);
			},
		},
		{
			name: "grep",
			description: "Search file contents with a regex (ripgrep engine, in-process via pi-natives). Respects .gitignore.",
			parameters: Type.Object({
				pattern: Type.String(),
				path: Type.Optional(Type.String()),
				glob: Type.Optional(Type.String()),
				ignore_case: Type.Optional(Type.Boolean()),
				max_results: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })),
			}),
			async execute(ctx, input) {
				const natives = await loadNatives(deps.nativesDir);
				const result = await natives.grep({
					pattern: String(input.pattern),
					path: abs(ctx, (input.path as string | undefined) ?? "."),
					glob: input.glob as string | undefined,
					ignoreCase: input.ignore_case === true,
					maxCount: (input.max_results as number | undefined) ?? 200,
					timeoutMs: 30_000,
				});
				const lines = result.matches.map(m => `${m.path}:${m.lineNumber}: ${m.line}`);
				return ok(lines.length ? `${lines.join("\n")}${result.limitReached ? "\n… limit reached" : ""}` : "No matches.", { total: result.totalMatches });
			},
		},
		{
			name: "glob",
			description: "Find files by glob pattern (in-process walker via pi-natives). Respects .gitignore.",
			parameters: Type.Object({ pattern: Type.String(), path: Type.Optional(Type.String()), max_results: Type.Optional(Type.Integer({ minimum: 1, maximum: 5000 })) }),
			async execute(ctx, input) {
				const natives = await loadNatives(deps.nativesDir);
				const result = await natives.glob({
					pattern: String(input.pattern),
					path: abs(ctx, (input.path as string | undefined) ?? "."),
					maxResults: (input.max_results as number | undefined) ?? 500,
					cache: false,
					timeoutMs: 30_000,
				});
				return ok(result.matches.map(m => m.path).join("\n") || "No files.", { total: result.totalMatches });
			},
		},
		{
			name: "bash",
			description: "Run a shell command under the Tsuna supervisor (process group, deadline, durable exit status). Foreground waits up to the deadline; background returns a job id for job_status/job_wait.",
			parameters: Type.Object({
				command: Type.String(),
				timeout_seconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 3600 })),
				background: Type.Optional(Type.Boolean()),
			}),
			async execute(ctx, input) {
				const command = String(input.command);
				const timeout = (input.timeout_seconds as number | undefined) ?? 120;
				// One result identity per tool call: a repeated start returns the existing run.
				const jobId = `${ctx.agentId}-${ctx.toolCallId}`.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 96);
				const started = await supervisor.start({ id: jobId, cwd: ctx.cwd, timeoutSeconds: timeout, command: ["/bin/sh", "-c", command] });
				deps.jobs.record(ctx.agentId, { runDirectory: started.run_directory, jobId: started.id, command, toolCallId: ctx.toolCallId, startedAt: Date.now() });
				if (input.background === true) {
					return ok(`Started job ${started.id}${started.created ? "" : " (already existed; not started again)"}. Use job_wait/job_status.`, { job: started.id });
				}
				const status = await supervisor.wait(started.run_directory, timeout + 5, ctx.signal);
				if (ctx.signal?.aborted && !TERMINAL_JOB_STATES.has(status.state)) {
					const cancelled = await supervisor.cancel(started.run_directory).catch(() => status);
					return fail(`Interrupted; cancellation requested for job ${started.id} (state ${cancelled.state}).`);
				}
				const text = jobSummary(status, supervisor);
				return { text, isError: status.state !== "completed" || status.exit_code !== 0, details: { job: started.id, state: status.state, exit_code: status.exit_code } };
			},
		},
		{
			name: "job_status",
			description: "Status and log tail of one of your supervised jobs.",
			parameters: Type.Object({ id: Type.String() }),
			async execute(ctx, input) {
				const dir = deps.jobs.owned(ctx.agentId, String(input.id));
				if (!dir) return fail(`job ${input.id} is not owned by ${ctx.agentId}`);
				return ok(jobSummary(await supervisor.status(dir), supervisor));
			},
		},
		{
			name: "job_wait",
			description: "Wait (bounded) for one of your supervised jobs. A timeout leaves the job running; it is never restarted or cancelled by waiting.",
			parameters: Type.Object({ id: Type.String(), timeout_seconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 600 })) }),
			async execute(ctx, input) {
				const dir = deps.jobs.owned(ctx.agentId, String(input.id));
				if (!dir) return fail(`job ${input.id} is not owned by ${ctx.agentId}`);
				const status = await supervisor.wait(dir, (input.timeout_seconds as number | undefined) ?? 60, ctx.signal);
				return ok(`${TERMINAL_JOB_STATES.has(status.state) ? "" : "wait timed out; job still running\n"}${jobSummary(status, supervisor)}`);
			},
		},
		{
			name: "job_cancel",
			description: "Request process-group cancellation of one of your running supervised jobs.",
			parameters: Type.Object({ id: Type.String() }),
			async execute(ctx, input) {
				const dir = deps.jobs.owned(ctx.agentId, String(input.id));
				if (!dir) return fail(`job ${input.id} is not owned by ${ctx.agentId}`);
				const status = await supervisor.cancel(dir);
				return ok(`cancel requested: ${status.state}`);
			},
		},
	];
	return new Map(tools.map(t => [t.name, t]));
}
