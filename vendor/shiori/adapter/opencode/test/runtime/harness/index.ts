// Test-only OpenCode plugin for the opt-in runtime smoke. It relays a tool
// invocation to the effective native catalog (ctx.tool.list) so the smoke
// can exercise the real host registration, permission engine and event
// stream without any model call. The test supplies the caller identity;
// the AbortSignal is the host RPC call's own signal. It never replies to
// permissions: the test acts as the user through the public API.
const TOOLS = [
  "workplan_create", "workplan_update", "workplan_inspect", "workplan_validate", "workplan_read", "workplan_list",
  "workplan_patch", "workplan_reset", "workplan_resume", "workplan_checkpoint", "workplan_compact", "workplan_doctor",
  "workplan_compact_preview",
];

export const HarnessRpc = {
  id: "shiori-runtime-harness",
  methods: {
    run: {
      input: {
        type: "object",
        properties: {
          toolName: { type: "string", enum: TOOLS },
          args: { type: "object", additionalProperties: true },
          sessionID: { type: "string" },
          agent: { type: "string" },
          messageID: { type: "string" },
          toolCallID: { type: "string" },
        },
        required: ["toolName", "args", "sessionID", "agent", "messageID", "toolCallID"],
        additionalProperties: false,
      },
      output: {
        type: "object",
        properties: { ok: { type: "boolean" }, content: { type: "string" }, error: { type: "string" }, completed: { type: "integer" } },
        required: ["ok", "completed"],
        additionalProperties: false,
      },
    },
    status: {
      input: { type: "object", additionalProperties: false },
      output: {
        type: "object",
        properties: { completed: { type: "integer" }, nativeTools: { type: "array", items: { type: "string" } } },
        required: ["completed", "nativeTools"],
        additionalProperties: false,
      },
    },
  },
  events: {},
} as const;

export default {
  id: "shiori-runtime-harness",
  async setup(context: any) {
    let completed = 0;
    const registration = await context.rpc.register(HarnessRpc, {
      async run(input: any, rpcContext: { signal: AbortSignal }) {
        const tool = (await context.tool.list()).find((t: any) => t.id === input.toolName);
        if (!tool) return { ok: false, error: `not registered: ${input.toolName}`, completed };
        try {
          const result = await tool.execute(input.args, {
            sessionID: input.sessionID,
            agent: input.agent,
            messageID: input.messageID,
            id: input.toolCallID,
            signal: rpcContext.signal,
            async progress() {},
          });
          completed++;
          const content = typeof result === "string" ? result : typeof result?.content === "string" ? result.content : JSON.stringify(result);
          return { ok: true, content, completed };
        } catch (error) {
          return { ok: false, error: error instanceof Error ? error.message : String(error), completed };
        }
      },
      async status() {
        const nativeTools = (await context.tool.list()).map((t: any) => t.id).filter((n: string) => TOOLS.includes(n)).sort();
        return { completed, nativeTools };
      },
    });
    return () => registration.dispose();
  },
};
