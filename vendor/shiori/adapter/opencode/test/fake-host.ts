// Fakes of the OpenCode plugin context domains and the public host API for
// adapter tests. The real permission bridge runs against them; nothing here
// approves on the bridge's behalf except the test's explicit user replies.
import { BRIDGE_RPC_ID, type HostClient, type HostEvent, type PermissionCreateInput } from "../src/host-client";
import { registerNativePermissionBridge, type NativePermissionBridgeContext } from "../src/permission-bridge";

export class EventChannel {
  #events: HostEvent[] = [];
  #waiters = new Set<(event: HostEvent | undefined) => void>();
  #closed = false;
  activeReaders = 0;

  emit(event: HostEvent): void {
    const waiter = this.#waiters.values().next().value as ((event: HostEvent | undefined) => void) | undefined;
    if (waiter) waiter(event);
    else this.#events.push(event);
  }

  close(): void {
    this.#closed = true;
    for (const waiter of [...this.#waiters]) waiter(undefined);
  }

  subscribe(signal?: AbortSignal): AsyncIterable<HostEvent> {
    const channel = this;
    return {
      async *[Symbol.asyncIterator]() {
        channel.activeReaders++;
        try {
          while (!signal?.aborted) {
            const event = await channel.#next(signal);
            if (!event) return;
            yield event;
          }
        } finally {
          channel.activeReaders--;
        }
      },
    };
  }

  async #next(signal?: AbortSignal): Promise<HostEvent | undefined> {
    if (signal?.aborted || this.#closed) return undefined;
    const buffered = this.#events.shift();
    if (buffered) return buffered;
    return new Promise((resolveEvent) => {
      const onEvent = (event: HostEvent | undefined) => {
        cleanup();
        resolveEvent(this.#closed ? undefined : event);
      };
      const onAbort = () => {
        cleanup();
        resolveEvent(undefined);
      };
      const cleanup = () => {
        this.#waiters.delete(onEvent);
        signal?.removeEventListener("abort", onAbort);
      };
      this.#waiters.add(onEvent);
      signal?.addEventListener("abort", onAbort, { once: true });
      if (signal?.aborted) onAbort();
    });
  }
}

export type Effect = "allow" | "deny" | "ask";

export interface FakeHostOptions {
  effect?: Effect;
  /** Emitted before the matching ask (must never authorize). */
  unrelatedAsks?: Array<Partial<{ id: string; action: string; resources: string[]; source: any; metadata: any; location: string }>>;
  /** Replaces the matching ask (then no genuine ask is emitted). */
  askOverride?: Partial<{ id: string; action: string; resources: string[]; source: any; metadata: any; location: string }>;
  closeStreamOnAsk?: boolean;
  sessionDirectory?: string;
  hostVersion?: string;
}

export interface RegisteredTool {
  name: string;
  description: string;
  input: any;
  options?: { codemode?: boolean };
  execute(input: unknown, context: unknown): Promise<{ content: string }>;
}

export function createFakeHost(root: string, options: FakeHostOptions = {}) {
  const channel = new EventChannel();
  const requests: PermissionCreateInput[] = [];
  const registered: RegisteredTool[] = [];
  let prove: ((input: unknown, ctx: { signal: AbortSignal }) => Promise<unknown>) | undefined;
  const created: PermissionCreateInput[] = [];
  const createWaiters: Array<(input: PermissionCreateInput) => void> = [];
  let counter = 0;
  const location = (dir: string) => ({ directory: dir });

  const ctx = {
    app: { name: "opencode", version: options.hostVersion ?? "2.0.20", channel: "latest" },
    location: { project: { directory: root } },
    options: {} as Record<string, unknown>,
    rpc: {
      async register(_definition: unknown, handlers: { prove: typeof prove }) {
        prove = handlers.prove;
        return {
          events: {
            async emit(_name: "proof", data: { challenge: string; proof: string }) {
              channel.emit({ type: `rpc.${BRIDGE_RPC_ID}.proof`, location: location(root), data });
            },
          },
          async dispose() {},
        };
      },
    },
    tool: {
      async list() {
        return registered.map((tool) => ({ id: tool.name }));
      },
      async transform(edit: (editor: { add(tool: unknown): void }) => void) {
        edit({ add(tool) { registered.push(tool as RegisteredTool); } });
        return { async dispose() { registered.length = 0; } };
      },
    },
    plugin: {
      async list() {
        return { data: [{ id: "workplan-tools", state: { status: "active" } }, { id: "opencode.plan", state: { status: "disabled" } }] };
      },
    },
    agent: {
      async get() {
        return { data: { permissions: [{ action: "edit", resource: `${root}/.opencode/workplan/x.json`, effect: "ask" }] } };
      },
    },
    session: {
      async get({ sessionID }: { sessionID: string }) {
        return { id: sessionID, location: location(options.sessionDirectory ?? root), permissions: [] };
      },
    },
  };

  const client: HostClient = {
    server: { async info() { return { version: options.hostVersion ?? "2.0.20", pid: 1 }; } },
    event: { subscribe: (o) => channel.subscribe(o?.signal) },
    session: { get: async ({ sessionID }) => ctx.session.get({ sessionID }) },
    permission: {
      async create(input) {
        requests.push(input);
        counter++;
        const id = `per_${counter}`;
        const waiter = createWaiters.shift();
        if (waiter) waiter(input);
        else created.push(input);
        const effect = options.effect ?? "allow";
        if (effect === "ask") {
          for (const u of options.unrelatedAsks ?? []) channel.emit(askEvent(input, id, root, u));
          if (options.closeStreamOnAsk) {
            setTimeout(() => channel.close(), 5);
          } else {
            channel.emit(askEvent(input, id, root, options.askOverride ?? {}));
          }
        }
        return { id, effect };
      },
    },
    rpc: {
      async prove(input, o) {
        if (!prove) throw new Error("bridge RPC not registered");
        return prove(input, { signal: o?.signal ?? new AbortController().signal });
      },
    },
  };

  return {
    ctx,
    client,
    channel,
    requests,
    registered,
    /** Resolves with the next permission.create input. */
    nextAsk(): Promise<PermissionCreateInput> {
      const ready = created.shift();
      if (ready) return Promise.resolve(ready);
      return new Promise((r) => createWaiters.push(r));
    },
    /** A genuine user reply for the given request ID. */
    reply(requestID: string, reply: "once" | "always" | "reject", sessionID = "ses_test") {
      channel.emit({ type: "permission.replied", location: location(root), data: { sessionID, requestID, reply } });
    },
    tool(name: string): RegisteredTool {
      const t = registered.find((x) => x.name === name);
      if (!t) throw new Error(`tool ${name} not registered`);
      return t;
    },
    bridgeFactory() {
      return (c: NativePermissionBridgeContext) => registerNativePermissionBridge(c, {
        discover: async () => ({ url: "http://127.0.0.1:9", auth: { type: "basic", username: "opencode", password: "test-secret" } }),
        makeClient: () => client,
        proofTimeoutMs: 500,
      });
    },
  };
}

function askEvent(input: PermissionCreateInput, id: string, root: string, over: FakeHostOptions["askOverride"] = {}): HostEvent {
  return {
    type: "permission.asked",
    location: { directory: over?.location ?? root },
    data: {
      id: over?.id ?? id,
      sessionID: input.sessionID,
      action: over?.action ?? input.action,
      resources: [...(over?.resources ?? input.resources)],
      source: over?.source ?? input.source,
      metadata: over?.metadata ?? input.metadata,
    },
  };
}

/** A native ToolContext; pass null to simulate a host without an AbortSignal. */
export function toolContext(agent: string, signal: AbortSignal | null = new AbortController().signal, suffix = "1") {
  return {
    sessionID: "ses_test",
    agent,
    messageID: `msg_test_${suffix}`,
    id: `call_test_${suffix}`,
    signal: signal ?? undefined,
    async progress() {},
  };
}
