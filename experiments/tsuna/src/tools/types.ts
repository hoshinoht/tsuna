import type { TSchema } from "typebox";
import type { GateSession } from "../policy/gate.ts";

/** What a Tsuna tool implementation sees about its caller. */
export interface ToolContext {
	agentId: string;
	rootId: string;
	role: string;
	cwd: string;
	toolCallId: string;
	signal?: AbortSignal;
	gate: GateSession;
	/** Invoke another tool through the same catalog + permission path (nested calls). */
	invoke(name: string, input: Record<string, unknown>, signal?: AbortSignal): Promise<ToolOutput>;
	onUpdate?(text: string): void;
}

export interface ToolOutput {
	text: string;
	details?: unknown;
	isError?: boolean;
	/** Stop the agent loop after this batch (only when every result in the batch sets it). */
	terminate?: boolean;
}

export interface ToolImpl {
	name: string;
	description: string;
	parameters: TSchema;
	/** Run the whole batch sequentially when this tool is present (Pi semantics). */
	sequential?: boolean;
	/** Schema supplied by an external server (MCP); argument values do not affect policy. */
	external?: boolean;
	execute(ctx: ToolContext, input: Record<string, unknown>): Promise<ToolOutput>;
}

export function ok(text: string, details?: unknown): ToolOutput {
	return { text, details };
}

export function fail(text: string, details?: unknown): ToolOutput {
	return { text, details, isError: true };
}
