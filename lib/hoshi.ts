export interface Compression { toolCallIds: string[]; summary: string }

/** Preserve every user/assistant message and tool-call pairing; replace only selected tool output. */
export function compressToolResults<T>(messages: T[], summaries: ReadonlyMap<string, string>): T[] {
  return messages.map(message => {
    const value = message as Record<string, unknown>;
    if (value.role !== "toolResult" || typeof value.toolCallId !== "string") return message;
    const summary = summaries.get(value.toolCallId);
    if (!summary) return message;
    return { ...value, content: [{ type: "text", text: `[Compressed tool result]\n${summary}` }] } as T;
  });
}
