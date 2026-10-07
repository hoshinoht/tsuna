// Ported from the reference workplan-tools bridge tests (same owner, GPLv3)
// against the adapter's dependency-free bridge (P02, P04, P05, P06).
import { describe, expect, it } from "bun:test";
import { mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

import {
  registerNativePermissionBridge,
  type NativePermissionBridge,
  type NativePermissionBridgeClient,
  type NativePermissionIntent,
  type NativePermissionBridgeOptions,
} from "../src/permission-bridge";
import { BRIDGE_RPC_ID } from "../src/host-client";

const SESSION_ID = "ses_workplan_bridge_test";
const MESSAGE_ID = "msg_workplan_bridge_test";
const TOOL_CALL_ID = "call_workplan_bridge_test";
const REQUEST_ID = "per_workplan_bridge_test";
const AGENT = "orchestrator";

type EventStream = ReturnType<NativePermissionBridgeClient["event"]["subscribe"]>;
type OpenCodeEvent = EventStream extends AsyncIterable<infer Event> ? Event : never;
type PermissionCreateInput = Parameters<NativePermissionBridgeClient["permission"]["create"]>[0];
type PermissionEffect = Awaited<ReturnType<NativePermissionBridgeClient["permission"]["create"]>>["effect"];
type Endpoint = NonNullable<Awaited<ReturnType<NonNullable<NativePermissionBridgeOptions["discover"]>>>>;
type PermissionResult = { id: string; effect: PermissionEffect };
type ProofHandler = (input: unknown, context: { signal: AbortSignal }) => Promise<unknown>;
type AskedEventOverrides = {
  readonly id?: string;
  readonly sessionID?: string;
  readonly action?: string;
  readonly resources?: readonly string[];
  readonly source?: PermissionCreateInput["source"];
  readonly metadata?: PermissionCreateInput["metadata"];
  readonly location?: string;
};

interface HarnessOptions {
  readonly effect?: PermissionEffect;
  readonly dropProofEvent?: boolean;
  readonly unrelatedProofBeforeEcho?: boolean;
  readonly closeEventStream?: boolean;
  readonly proofEventLocation?: string;
  readonly alterRpcProof?: boolean;
  readonly unrelatedAskedEvents?: readonly AskedEventOverrides[];
  readonly askedEvent?: AskedEventOverrides;
  readonly sessionDirectory?: string;
  readonly nestedSession?: boolean;
  readonly projectDirectoryAlias?: boolean;
  readonly holdProof?: boolean;
  readonly holdDiscovery?: boolean;
  readonly serviceUnavailable?: boolean;
  readonly rpcRegistrationFails?: boolean;
  readonly permissionHandler?: (input: PermissionCreateInput, signal: AbortSignal) => Promise<PermissionResult>;
  readonly proofTimeoutMs?: number;
}

class EventChannel {
  #events: OpenCodeEvent[] = [];
  #waiters = new Set<(event: OpenCodeEvent) => void>();
  #closed = false;
  activeReaders = 0;

  emit(event: OpenCodeEvent): void {
    const waiter = this.#waiters.values().next().value as ((event: OpenCodeEvent) => void) | undefined;
    if (waiter) waiter(event);
    else this.#events.push(event);
  }

  close(): void {
    this.#closed = true;
    for (const waiter of [...this.#waiters]) waiter({
      id: "unused",
      type: "server.connected",
      data: {},
    });
  }

  subscribe(signal?: AbortSignal): AsyncIterable<OpenCodeEvent> {
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

  async #next(signal?: AbortSignal): Promise<OpenCodeEvent | undefined> {
    if (signal?.aborted || this.#closed) return undefined;
    const buffered = this.#events.shift();
    if (buffered) return buffered;
    return new Promise((resolveEvent) => {
      const onEvent = (event: OpenCodeEvent) => {
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

interface BridgeHarness {
  readonly bridge: NativePermissionBridge;
  readonly root: string;
  readonly channel: EventChannel;
  readonly requests: PermissionCreateInput[];
  readonly requestCreated: Promise<PermissionCreateInput>;
  readonly reply: (reply: "once" | "always" | "reject", requestID?: string) => void;
  readonly releaseProof: () => void;
  readonly releaseDiscovery: () => void;
}

function proofEvent(proof: { challenge: string; proof: string }, location: string): OpenCodeEvent {
  return {
    id: "event-workplan-bridge-proof",
    created: Date.now(),
    type: `rpc.${BRIDGE_RPC_ID}.proof`,
    location: { directory: location },
    data: proof,
  };
}

function askedEvent(input: PermissionCreateInput, location: string, overrides: AskedEventOverrides = {}): OpenCodeEvent {
  return {
    id: "event-workplan-bridge-asked",
    created: Date.now(),
    type: "permission.asked",
    location: { directory: overrides.location ?? location },
    data: {
      id: overrides.id ?? REQUEST_ID,
      sessionID: overrides.sessionID ?? input.sessionID,
      action: overrides.action ?? input.action,
      resources: [...(overrides.resources ?? input.resources)],
      source: overrides.source ?? input.source,
      metadata: overrides.metadata ?? input.metadata,
    },
  };
}

function repliedEvent(reply: "once" | "always" | "reject", location: string, requestID = REQUEST_ID): OpenCodeEvent {
  return {
    id: "event-workplan-bridge-replied",
    created: Date.now(),
    type: "permission.replied",
    location: { directory: location },
    data: { sessionID: SESSION_ID, requestID, reply },
  };
}

async function createHarness(options: HarnessOptions = {}): Promise<BridgeHarness> {
  const root = realpathSync(mkdtempSync(join(tmpdir(), "native-permission-bridge-")));
  if (options.nestedSession) mkdirSync(join(root, "nested"));
  const projectDirectory = options.projectDirectoryAlias ? join(root, "project-alias") : root;
  if (options.projectDirectoryAlias) symlinkSync(root, projectDirectory, "dir");
  const channel = new EventChannel();
  if (options.closeEventStream) channel.close();
  const requests: PermissionCreateInput[] = [];
  let prove: ProofHandler | undefined;
  let resolveRequest!: (input: PermissionCreateInput) => void;
  const requestCreated = new Promise<PermissionCreateInput>((resolveEvent) => { resolveRequest = resolveEvent; });
  let releaseProof!: () => void;
  const proofGate = new Promise<void>((resolveProof) => { releaseProof = resolveProof; });
  let releaseDiscovery!: (endpoint: Endpoint | undefined) => void;
  const discoveryGate = new Promise<Endpoint | undefined>((resolveEndpoint) => { releaseDiscovery = resolveEndpoint; });

  const context = {
    location: { project: { directory: projectDirectory } },
    rpc: {
      async register(_definition: unknown, handlers: unknown) {
        if (options.rpcRegistrationFails) throw new Error("unsupported RPC registration");
        prove = (handlers as { prove: ProofHandler }).prove;
        return {
          events: {
            async emit(_name: "proof", proof: { challenge: string; proof: string }) {
              if (!options.dropProofEvent) {
                if (options.unrelatedProofBeforeEcho) {
                  channel.emit(proofEvent({ challenge: "f".repeat(64), proof: "0".repeat(64) }, root));
                }
                channel.emit(proofEvent(proof, options.proofEventLocation ?? root));
              }
            },
          },
          async dispose() {},
        };
      },
    },
  };

  const endpoint: Endpoint = {
    url: "https://bridge-user:bridge-url-secret@host.invalid",
    auth: { type: "basic", username: "bridge-user", password: "bridge-auth-secret" },
  };
  const client = {
    server: {
      async info() {
        return { version: "2.0.19", pid: 42, urls: [], paths: { tmp: "/private/tmp" } };
      },
    },
    event: {
      subscribe(requestOptions?: { signal?: AbortSignal }) {
        return channel.subscribe(requestOptions?.signal);
      },
    },
    session: {
      async get({ sessionID }: { sessionID: string }) {
        return {
          id: sessionID,
          projectID: "project_workplan_bridge_test",
          cost: 0,
          tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } },
          time: { created: 0, updated: 0 },
          location: { directory: options.nestedSession ? join(root, "nested") : options.sessionDirectory ?? root },
        };
      },
    },
    permission: {
      async create(input: PermissionCreateInput, requestOptions?: { signal?: AbortSignal }) {
        requests.push(input);
        resolveRequest(input);
        if (options.permissionHandler) return options.permissionHandler(input, requestOptions?.signal ?? new AbortController().signal);
        if (options.effect === "ask") {
          for (const unrelated of options.unrelatedAskedEvents ?? []) channel.emit(askedEvent(input, root, unrelated));
          channel.emit(askedEvent(input, root, options.askedEvent));
          return { id: REQUEST_ID, effect: "ask" as const };
        }
        if (options.effect === "deny") return { id: REQUEST_ID, effect: "deny" as const };
        return { id: REQUEST_ID, effect: "allow" as const };
      },
    },
    rpc: {
      async prove(input: { challenge: string }, requestOptions?: { signal?: AbortSignal }) {
        if (options.holdProof) await proofGate;
        if (!prove) throw new Error("test RPC is not registered");
        const result = await prove(input, { signal: requestOptions?.signal ?? new AbortController().signal }) as { challenge: string; proof: string };
        return options.alterRpcProof ? { ...result, proof: "0".repeat(64) } : result;
      },
    },
  };

  const bridge = await registerNativePermissionBridge(context as never, {
    discover: async () => {
      if (options.holdDiscovery) return await discoveryGate;
      return options.serviceUnavailable ? undefined : endpoint;
    },
    makeClient: () => client as unknown as NativePermissionBridgeClient,
    proofTimeoutMs: options.proofTimeoutMs ?? 30,
  });

  return {
    bridge,
    root,
    channel,
    requests,
    requestCreated,
    reply(reply, requestID) {
      channel.emit(repliedEvent(reply, root, requestID));
    },
    releaseProof,
    releaseDiscovery() {
      releaseDiscovery(endpoint);
    },
  };
}

const intent = (root: string, resources = [resolve(root, "plan.json"), resolve(root, "plan.md")]): NativePermissionIntent => ({
  sessionID: SESSION_ID,
  agent: AGENT,
  messageID: MESSAGE_ID,
  toolCallID: TOOL_CALL_ID,
  resources,
});

async function withHarness(options: HarnessOptions, run: (harness: BridgeHarness) => Promise<void>): Promise<void> {
  const harness = await createHarness(options);
  try {
    await run(harness);
  } finally {
    await harness.bridge.dispose();
    rmSync(harness.root, { recursive: true, force: true });
  }
}

describe("native public workplan permission bridge", () => {
  it("binds the exact plugin instance and project location before an exact-source edit allow", async () => {
    await withHarness({}, async (harness) => {
      const controller = new AbortController();
      const receipt = await harness.bridge.authorizeEdit(intent(harness.root), { signal: controller.signal });

      expect(receipt).toMatchObject({
        decision: "allow",
        via: "runtime-policy",
        sessionID: SESSION_ID,
        agent: AGENT,
        source: { type: "tool", messageID: MESSAGE_ID, id: TOOL_CALL_ID },
      });
      expect(receipt.resources).toEqual([resolve(harness.root, "plan.json"), resolve(harness.root, "plan.md")]);
      expect(harness.requests).toHaveLength(1);
      expect(harness.requests[0]).toMatchObject({
        sessionID: SESSION_ID,
        action: "edit",
        agent: AGENT,
        source: { type: "tool", messageID: MESSAGE_ID, id: TOOL_CALL_ID },
      });
      expect(harness.requests[0].resources).toEqual(receipt.resources);
      expect(harness.requests[0].resources).not.toContain("*");
      expect(harness.requests[0].save).toBeUndefined();
      expect(harness.bridge.diagnostics()).toMatchObject({
        clientVersion: "2.0.20",
        rpcRegistration: "available",
        serviceDiscovery: "available",
        runtimeVersion: "2.0.19",
        hostBinding: "verified",
        eventStream: "ready",
        permissionDecision: "allow",
      });
      expect(JSON.stringify(harness.bridge.diagnostics())).not.toContain("bridge-auth-secret");
      expect(JSON.stringify(harness.bridge.diagnostics())).not.toContain("bridge-url-secret");
      expect(harness.channel.activeReaders).toBe(0);
    });
  });

  it("rejects cross-instance and cross-location proofs before requesting permission", async () => {
    for (const options of [
      { alterRpcProof: true },
      { proofEventLocation: resolve(tmpdir(), "other-workplan-project") },
    ]) {
      await withHarness(options, async (harness) => {
        await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal }))
          .rejects.toThrow(/did not prove|different workplan plugin instance/i);
        expect(harness.requests).toHaveLength(0);
        expect(harness.bridge.diagnostics().permissionDecision).toBe("unknown");
        expect(harness.bridge.diagnostics().hostBinding).toBe("mismatch");
      });
    }
  });

  it("ignores an unrelated proof event before the current challenge echo", async () => {
    await withHarness({ unrelatedProofBeforeEcho: true }, async (harness) => {
      const receipt = await harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal });
      expect(receipt).toMatchObject({ decision: "allow", via: "runtime-policy" });
      expect(harness.requests).toHaveLength(1);
    });
  });

  it("fails closed when the event stream disconnects or does not echo the nonce", async () => {
    await withHarness({ closeEventStream: true }, async (harness) => {
      await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal })).rejects.toThrow();
      expect(harness.requests).toHaveLength(0);
    });
    await withHarness({ dropProofEvent: true, proofTimeoutMs: 5 }, async (harness) => {
      await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal }))
        .rejects.toThrow(/did not prove/i);
      expect(harness.requests).toHaveLength(0);
      expect(harness.bridge.diagnostics().eventStream).toBe("unavailable");
    });
  });

  it("fails closed on missing public capabilities and does not continue after late discovery", async () => {
    await withHarness({ rpcRegistrationFails: true }, async (harness) => {
      await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal }))
        .rejects.toThrow(/cannot register/i);
      expect(harness.bridge.diagnostics().rpcRegistration).toBe("unavailable");
      expect(harness.bridge.diagnostics().serviceDiscovery).toBe("unknown");
      expect(harness.requests).toHaveLength(0);
    });
    await withHarness({ serviceUnavailable: true }, async (harness) => {
      await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal }))
        .rejects.toThrow(/No authenticated local OpenCode service/i);
      expect(harness.bridge.diagnostics().serviceDiscovery).toBe("unavailable");
      expect(harness.requests).toHaveLength(0);
    });
    await withHarness({ holdDiscovery: true }, async (harness) => {
      const controller = new AbortController();
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: controller.signal });
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 0));
      controller.abort();
      await expect(operation).rejects.toThrow(/cancelled/i);
      harness.releaseDiscovery();
      await Promise.resolve();
      expect(harness.requests).toHaveLength(0);
    });
  });

  it("waits for a correlated real grant and rejects a real rejection", async () => {
    await withHarness({ effect: "ask" }, async (harness) => {
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal });
      const request = await harness.requestCreated;
      let settled = false;
      void operation.finally(() => { settled = true; }).catch(() => {});
      await Promise.resolve();
      expect(settled).toBe(false);
      expect(request.action).toBe("edit");
      harness.reply("once");
      await expect(operation).resolves.toMatchObject({ decision: "allow", via: "user-reply", requestID: REQUEST_ID });
    });
    await withHarness({ effect: "ask" }, async (harness) => {
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal });
      await harness.requestCreated;
      harness.reply("reject");
      await expect(operation).rejects.toThrow(/rejected/i);
      expect(harness.bridge.diagnostics().permissionDecision).toBe("rejected");
    });
  });

  it("fails closed on deny, missing signals, broad paths, and session-location mismatch", async () => {
    await withHarness({ effect: "deny" }, async (harness) => {
      await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal })).rejects.toThrow(/denied/i);
      expect(harness.bridge.diagnostics().permissionDecision).toBe("deny");
    });
    await withHarness({}, async (harness) => {
      await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: undefined as never })).rejects.toThrow(/AbortSignal/i);
      await expect(harness.bridge.authorizeEdit(intent(harness.root, [resolve(harness.root, "*")]), { signal: new AbortController().signal }))
        .rejects.toThrow(/exact canonical absolute/i);
      expect(harness.requests).toHaveLength(0);
      expect(harness.bridge.diagnostics().serviceDiscovery).toBe("unknown");
    });
    const other = mkdtempSync(join(tmpdir(), "native-permission-bridge-other-"));
    try {
      await withHarness({ sessionDirectory: other }, async (harness) => {
        await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal }))
          .rejects.toThrow(/not owned by this canonical project/i);
        expect(harness.requests).toHaveLength(0);
      });
    } finally {
      rmSync(other, { recursive: true, force: true });
    }
  });

  it("canonicalizes an aliased project root and rejects aliased resources and nested sessions", async () => {
    await withHarness({ projectDirectoryAlias: true }, async (harness) => {
      const receipt = await harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal });
      expect(receipt.decision).toBe("allow");
      expect(receipt.resources).toEqual([resolve(harness.root, "plan.json"), resolve(harness.root, "plan.md")]);
    });

    await withHarness({}, async (harness) => {
      mkdirSync(join(harness.root, "actual"));
      symlinkSync(join(harness.root, "actual"), join(harness.root, "alias"), "dir");
      await expect(harness.bridge.authorizeEdit(intent(harness.root, [join(harness.root, "alias", "workplan.json")]), {
        signal: new AbortController().signal,
      })).rejects.toThrow(/non-canonical path/i);
      expect(harness.requests).toHaveLength(0);
    });

    await withHarness({ nestedSession: true }, async (harness) => {
      await expect(harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal }))
        .rejects.toThrow(/not owned by this canonical project/i);
      expect(harness.requests).toHaveLength(0);
    });
  });

  it("ignores unrelated same-session asks before the matching ask and accepts its grant", async () => {
    await withHarness({
      effect: "ask",
      unrelatedAskedEvents: [
        { source: { type: "tool", messageID: "msg_other", id: "call_other" } },
        { metadata: { workplanToolsBridge: { version: 1, authorizationID: "foreign-authorization", agent: AGENT } } },
        { resources: [resolve(tmpdir(), "broader-target")] },
        { action: "read" },
        { location: resolve(tmpdir(), "other-workplan-project") },
      ],
    }, async (harness) => {
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal });
      await harness.requestCreated;
      harness.reply("once");
      await expect(operation).resolves.toMatchObject({ decision: "allow", via: "user-reply", requestID: REQUEST_ID });
    });
  });

  it("waits for the correlated ask and fails closed on cancellation when only unrelated asks arrive", async () => {
    const unrelatedAskedEvents: AskedEventOverrides[] = [
      { source: { type: "tool", messageID: "msg_other", id: "call_other" } },
      { metadata: { workplanToolsBridge: { version: 1, authorizationID: "foreign-authorization", agent: AGENT } } },
      { resources: [resolve(tmpdir(), "broader-target")] },
      { action: "read" },
      { location: resolve(tmpdir(), "other-workplan-project") },
    ];
    for (const askedEvent of unrelatedAskedEvents) {
      await withHarness({ effect: "ask", askedEvent }, async (harness) => {
        const controller = new AbortController();
        let settled = false;
        const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: controller.signal })
          .finally(() => { settled = true; });
        await harness.requestCreated;
        await new Promise((resolveDelay) => setTimeout(resolveDelay, 0));
        expect(settled).toBe(false);
        controller.abort();
        await expect(operation).rejects.toThrow(/cancelled/i);
        expect(harness.bridge.diagnostics().permissionDecision).toBe("cancelled");
      });
    }
  });

  it("rejects a correlated ask whose request ID differs from permission.create", async () => {
    await withHarness({ effect: "ask", askedEvent: { id: "per_replayed" } }, async (harness) => {
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: new AbortController().signal });
      await harness.requestCreated;
      await expect(operation).rejects.toThrow(/ask event did not match/i);
      expect(harness.bridge.diagnostics().permissionDecision).toBe("rejected");
      expect(harness.bridge.diagnostics().lastFailure).toBe("permission-ask-unverified");
    });
  });

  it("does not continue after cancellation, including late network results and late user grants", async () => {
    await withHarness({ effect: "ask" }, async (harness) => {
      const controller = new AbortController();
      let continued = false;
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: controller.signal })
        .then((receipt) => { continued = true; return receipt; });
      await harness.requestCreated;
      controller.abort();
      await expect(operation).rejects.toThrow(/cancelled/i);
      harness.reply("once");
      await Promise.resolve();
      expect(continued).toBe(false);
      expect(harness.channel.activeReaders).toBe(0);
    });
    let resolveLate!: (result: PermissionResult) => void;
    const lateNetworkResult = new Promise<PermissionResult>((resolveResult) => { resolveLate = resolveResult; });
    await withHarness({ permissionHandler: async () => lateNetworkResult }, async (harness) => {
      const controller = new AbortController();
      let continued = false;
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: controller.signal })
        .then((receipt) => { continued = true; return receipt; });
      await harness.requestCreated;
      controller.abort();
      await expect(operation).rejects.toThrow(/cancelled/i);
      resolveLate({ id: REQUEST_ID, effect: "allow" });
      await Promise.resolve();
      expect(continued).toBe(false);
      expect(harness.bridge.diagnostics().permissionDecision).toBe("cancelled");
    });
    await withHarness({ holdProof: true }, async (harness) => {
      const controller = new AbortController();
      const operation = harness.bridge.authorizeEdit(intent(harness.root), { signal: controller.signal });
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 0));
      controller.abort();
      await expect(operation).rejects.toThrow(/cancelled/i);
      harness.releaseProof();
      expect(harness.requests).toHaveLength(0);
    });
  });
});
