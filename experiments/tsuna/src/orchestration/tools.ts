/**
 * Model-facing orchestration tools. Identity is always the calling session's
 * trusted agent id (ToolContext.agentId); no tool accepts a caller identity,
 * role, model or capability argument.
 *
 * Differences from OMP (which exposes `task`, `wait`, `yield` and routes the
 * rest through `agent://`/`proc://` URLs on read/write): Tsuna exposes explicit
 * tools so each operation has its own permission action.
 */
import { Type } from "typebox";
import { fail, ok, type ToolImpl } from "../tools/types.ts";
import type { AgentRuntime } from "./runtime.ts";
import type { AgentRecord } from "./types.ts";

function str(input: Record<string, unknown>, key: string): string | undefined {
	const v = input[key];
	return typeof v === "string" && v.length > 0 ? v : undefined;
}

function summary(record: AgentRecord): string {
	const last = record.results.at(-1);
	return [
		`${record.id} role=${record.role} parent=${record.parentId ?? "-"} depth=${record.depth} status=${record.status}`,
		record.activity ? `  activity: ${record.activity}` : "",
		record.interruptedRunId ? `  interrupted run: ${record.interruptedRunId} (outcome unknown, not replayed)` : "",
		record.failure ? `  failure: ${record.failure}` : "",
		last ? `  last result: ${last.resultId} ${last.status} delivery=${last.delivery.state}` : "",
	].filter(Boolean).join("\n");
}

export function orchestrationTools(runtime: AgentRuntime): Map<string, ToolImpl> {
	const tools: ToolImpl[] = [
		{
			name: "task",
			description: "Delegate one assignment to a specialist from your allowed spawn list. Foreground (default) waits and returns the child's accepted result; background returns the child id immediately (collect with `wait`).",
			parameters: Type.Object({
				agent: Type.String({ description: "Specialist name" }),
				task: Type.String({ description: "Complete brief: goal, scope, constraints, stop condition, validation" }),
				background: Type.Optional(Type.Boolean()),
			}),
			async execute(ctx, input) {
				const agent = str(input, "agent")!;
				const task = str(input, "task") ?? "";
				try {
					if (input.background === true) {
						const { id } = runtime.spawn(ctx.agentId, { agent, task });
						return ok(`Spawned ${id} (${agent}) in the background. Use wait to collect its result.`, { id });
					}
					const { id, done } = runtime.spawn(ctx.agentId, { agent, task, foreground: true });
					ctx.onUpdate?.(`spawned ${id}`);
					const result = await done;
					const record = runtime.record(id)!;
					const final = result ?? record.results.at(-1);
					if (!final) return fail(`${id} ended without a result (status ${record.status})`, { id });
					return { text: runtime.formatForDelivery(final), isError: final.status !== "success", details: { id, resultId: final.resultId } };
				} catch (error) {
					return fail((error as Error).message);
				}
			},
		},
		{
			name: "dispatch",
			description: "Delegate several independent assignments at once. Foreground (default) waits for all and returns every result; one failure never discards successful siblings.",
			parameters: Type.Object({
				tasks: Type.Array(Type.Object({ agent: Type.String(), task: Type.String() }), { minItems: 1, maxItems: 16 }),
				background: Type.Optional(Type.Boolean()),
			}),
			async execute(ctx, input) {
				const items = (input.tasks as { agent: string; task: string }[]) ?? [];
				const spawned: { agent: string; id?: string; error?: string; done?: Promise<unknown> }[] = [];
				for (const item of items) {
					try {
						const { id, done } = runtime.spawn(ctx.agentId, { agent: item.agent, task: item.task, foreground: input.background !== true });
						spawned.push({ agent: item.agent, id, done });
					} catch (error) {
						spawned.push({ agent: item.agent, error: (error as Error).message });
					}
				}
				if (input.background === true) {
					return ok(spawned.map(s => (s.id ? `spawned ${s.id} (${s.agent})` : `rejected ${s.agent}: ${s.error}`)).join("\n"), { ids: spawned.map(s => s.id) });
				}
				await Promise.allSettled(spawned.map(s => s.done));
				const lines = spawned.map(s => {
					if (!s.id) return `rejected ${s.agent}: ${s.error}`;
					const record = runtime.record(s.id)!;
					const result = record.results.at(-1);
					return result ? runtime.formatForDelivery(result) : `${s.id} ended without a result (status ${record.status})`;
				});
				return ok(lines.join("\n\n"), { ids: spawned.map(s => s.id) });
			},
		},
		{
			name: "agent_list",
			description: "List agents in your session tree with status and current activity.",
			parameters: Type.Object({}),
			async execute(ctx) {
				const records = runtime.list({ kind: "agent", agentId: ctx.agentId });
				return ok(records.map(summary).join("\n"));
			},
		},
		{
			name: "agent_read",
			description: "Read an agent's accepted results, incremental outputs and recent transcript (same session tree only).",
			parameters: Type.Object({ id: Type.String(), messages: Type.Optional(Type.Integer({ minimum: 1, maximum: 200 })) }),
			async execute(ctx, input) {
				const view = runtime.read({ kind: "agent", agentId: ctx.agentId }, str(input, "id")!, { messages: input.messages as number | undefined });
				if (typeof view === "string") return fail(view);
				const r = view.record;
				const parts = [summary(r)];
				for (const res of r.results) parts.push(`result ${res.resultId}: ${res.status} ${JSON.stringify(res.data ?? res.error ?? res.text ?? null).slice(0, 2000)}`);
				for (const out of r.outputs) parts.push(`output ${out.seq} [${out.label}] ${JSON.stringify(out.data).slice(0, 1000)}`);
				parts.push("--- transcript (latest) ---");
				for (const m of view.transcript) parts.push(`${m.role}: ${m.text.slice(0, 1000)}`);
				return ok(parts.join("\n"));
			},
		},
		{
			name: "agent_send",
			description: "Message one of your direct children. mode=steer adjusts a running turn; mode=followup assigns new work (reviving a parked child). Reports delivered/queued/revived/rejected.",
			parameters: Type.Object({ id: Type.String(), message: Type.String(), mode: Type.Union([Type.Literal("steer"), Type.Literal("followup")]) }),
			async execute(ctx, input) {
				const r = await runtime.send({ kind: "agent", agentId: ctx.agentId }, str(input, "id")!, String(input.message ?? ""), input.mode === "followup" ? "followup" : "steer");
				return r.outcome === "rejected" ? fail(`rejected: ${r.reason}`, r) : ok(`${r.outcome}${r.reason ? `: ${r.reason}` : ""}`, r);
			},
		},
		{
			name: "agent_interrupt",
			description: "Interrupt a direct child's current run, keeping its session for later follow-up.",
			parameters: Type.Object({ id: Type.String() }),
			async execute(ctx, input) {
				const r = await runtime.interrupt({ kind: "agent", agentId: ctx.agentId }, str(input, "id")!);
				return r.ok ? ok("interrupted") : fail(r.reason ?? "not interrupted");
			},
		},
		{
			name: "agent_cancel",
			description: "Permanently cancel a direct child and its descendants.",
			parameters: Type.Object({ id: Type.String() }),
			async execute(ctx, input) {
				const r = await runtime.cancel({ kind: "agent", agentId: ctx.agentId }, str(input, "id")!);
				return r.ok ? ok(`cancelled: ${r.cancelled.join(", ")}`) : fail(r.reason ?? "not cancelled");
			},
		},
		{
			name: "agent_resume",
			description: "Revive an idle or parked direct child with its original contract, optionally with a follow-up message.",
			parameters: Type.Object({ id: Type.String(), message: Type.Optional(Type.String()) }),
			async execute(ctx, input) {
				const r = await runtime.resume({ kind: "agent", agentId: ctx.agentId }, str(input, "id")!, str(input, "message"));
				return r.outcome === "rejected" ? fail(`rejected: ${r.reason}`) : ok(r.outcome);
			},
		},
		{
			name: "wait",
			description: "Wait for your own children: returns their undelivered results or messages for you, or partial status at the timeout. Does not wait on peers, ancestors or external commands (use job_wait).",
			parameters: Type.Object({
				ids: Type.Optional(Type.Array(Type.String())),
				timeout_seconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 600 })),
			}),
			async execute(ctx, input) {
				try {
					const w = await runtime.wait(ctx.agentId, {
						ids: input.ids as string[] | undefined,
						timeoutMs: ((input.timeout_seconds as number | undefined) ?? 60) * 1000,
						signal: ctx.signal,
					});
					const lines = [`wait: ${w.reason}`, ...w.results, ...w.messages.map(m => `[message] ${m}`), "status:", ...w.status.map(s => `  ${s.id} ${s.status}${s.activity ? ` — ${s.activity}` : ""} (outputs: ${s.outputs})`)];
					return ok(lines.join("\n"), w);
				} catch (error) {
					return fail((error as Error).message);
				}
			},
		},
	];
	return new Map(tools.map(t => [t.name, t]));
}

/** Tools that every child receives or that need the runtime (yield, compress, batch). */
export function runtimeTools(runtime: AgentRuntime): Map<string, ToolImpl> {
	const tools: ToolImpl[] = [
		{
			name: "yield",
			description: "Submit your result. final=true (default) ends the assignment and must be the only tool call in its message; final=false records incremental output.",
			parameters: Type.Object({
				final: Type.Optional(Type.Boolean()),
				status: Type.Optional(Type.Union([Type.Literal("success"), Type.Literal("failure")])),
				label: Type.Optional(Type.String()),
				data: Type.Optional(Type.Unknown()),
				summary: Type.Optional(Type.String()),
				error: Type.Optional(Type.String()),
			}),
			async execute(ctx, input) {
				return runtime.yieldResult(ctx.agentId, ctx.toolCallId, input as never);
			},
		},
		{
			name: "compress",
			description: "Replace selected earlier tool results in your outgoing context with a summary. The stored transcript is unchanged.",
			parameters: Type.Object({ tool_call_ids: Type.Array(Type.String(), { minItems: 1 }), summary: Type.String() }),
			async execute(ctx, input) {
				const problem = runtime.addCompression(ctx.agentId, input.tool_call_ids as string[], String(input.summary ?? ""));
				return problem ? fail(problem) : ok(`Compressed ${(input.tool_call_ids as string[]).length} tool result(s) in outgoing context.`);
			},
		},
		{
			name: "batch",
			description: "Run several of your own tools in sequence. Each nested call is checked against your catalog and permissions exactly like a direct call.",
			parameters: Type.Object({
				calls: Type.Array(Type.Object({ tool: Type.String(), input: Type.Record(Type.String(), Type.Unknown()) }), { minItems: 1, maxItems: 20 }),
			}),
			sequential: true,
			async execute(ctx, input) {
				const calls = input.calls as { tool: string; input: Record<string, unknown> }[];
				const out: string[] = [];
				let errors = 0;
				for (const call of calls) {
					if (call.tool === "batch" || call.tool === "yield") {
						out.push(`${call.tool}: not allowed inside batch`);
						errors++;
						continue;
					}
					const r = await ctx.invoke(call.tool, call.input ?? {});
					if (r.isError) errors++;
					out.push(`${call.tool}${r.isError ? " (error)" : ""}: ${r.text}`);
				}
				return { text: out.join("\n\n"), isError: errors === calls.length };
			},
		},
	];
	return new Map(tools.map(t => [t.name, t]));
}
