/** Wrap a Tsuna ToolImpl as a Pi custom ToolDefinition. */
import { defineTool, type ToolDefinition } from "@earendil-works/pi-coding-agent";
import type { ToolImpl, ToolOutput } from "./types.ts";

export type Invoke = (
	name: string,
	toolCallId: string,
	input: Record<string, unknown>,
	signal?: AbortSignal,
	onUpdate?: (text: string) => void,
) => Promise<ToolOutput>;

export function toPiTool(impl: ToolImpl, invoke: Invoke): ToolDefinition {
	return defineTool({
		name: impl.name,
		label: impl.name,
		description: impl.description,
		parameters: impl.parameters,
		executionMode: impl.sequential ? "sequential" : undefined,
		async execute(toolCallId: string, params: unknown, signal?: AbortSignal, onUpdate?: (u: unknown) => void) {
			const out = await invoke(impl.name, toolCallId, (params ?? {}) as Record<string, unknown>, signal, text =>
				onUpdate?.({ content: [{ type: "text", text }], details: {} } as never),
			);
			return {
				content: [{ type: "text", text: out.text }],
				details: out.details ?? {},
				isError: out.isError || undefined,
				terminate: out.terminate || undefined,
			} as never;
		},
	} as never) as ToolDefinition;
}
