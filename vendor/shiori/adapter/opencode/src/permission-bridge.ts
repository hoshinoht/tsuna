import { createHmac, randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import { realpathSync } from "node:fs";
import { realpath } from "node:fs/promises";
import { basename, dirname, isAbsolute, relative, resolve, sep } from "node:path";

// Native host authorization for the Shiori adapter (spec 02 §4). Derived
// from the reference workplan-tools permission bridge (same owner, GPLv3):
// the same HMAC instance proof, event-stream readiness proof, filtered
// waiters and fail-closed outcomes, over a dependency-free public client.
// It never calls permission reply/rule APIs and never self-approves.
import type { Plugin } from "@opencode/plugin";

import {
  BRIDGE_RPC_ID,
  discoverService,
  makeHostClient,
  type Endpoint,
  type HostClient,
  type HostEvent as OpenCodeEvent,
  type PermissionCreateInput,
} from "./host-client";

const PROOF_TIMEOUT_MS = 5_000;
const REPLY_BUFFER_SIZE = 32;

// Rpc.define is an identity function in @opencode/plugin 2.0.20; the plain
// definition keeps this module free of runtime dependencies.
export const WorkplanPermissionRpc = {
  id: BRIDGE_RPC_ID,
  methods: {
    prove: {
      input: {
        type: "object",
        properties: { challenge: { type: "string", minLength: 64, maxLength: 64 } },
        required: ["challenge"],
        additionalProperties: false,
      },
      output: {
        type: "object",
        properties: {
          challenge: { type: "string" },
          proof: { type: "string", minLength: 64, maxLength: 64 },
        },
        required: ["challenge", "proof"],
        additionalProperties: false,
      },
    },
  },
  events: {
    proof: {
      schema: {
        type: "object",
        properties: {
          challenge: { type: "string" },
          proof: { type: "string", minLength: 64, maxLength: 64 },
        },
        required: ["challenge", "proof"],
        additionalProperties: false,
      },
    },
  },
} as const;

export interface NativePermissionBridgeContext {
  readonly location: { readonly project: { readonly directory: string } };
  readonly rpc: { register: (definition: any, handlers: any) => ReturnType<Plugin.Context["rpc"]["register"]> };
}

export interface NativePermissionIntent {
  readonly sessionID: string;
  readonly agent: string;
  readonly messageID: string;
  readonly toolCallID: string;
  readonly resources: readonly string[];
}

export interface NativePermissionInvocation {
  readonly signal: AbortSignal;
}

export interface NativePermissionReceipt {
  readonly decision: "allow";
  readonly via: "runtime-policy" | "user-reply";
  readonly authorizationID: string;
  readonly requestID: string;
  readonly sessionID: string;
  readonly agent: string;
  readonly source: { readonly type: "tool"; readonly messageID: string; readonly id: string };
  readonly resources: readonly string[];
}

export interface NativePermissionBridgeDiagnostics {
  readonly clientVersion: "2.0.20";
  readonly rpcRegistration: "available" | "unavailable";
  readonly serviceDiscovery: "unknown" | "available" | "unavailable";
  readonly runtimeVersion?: string;
  readonly hostBinding: "unknown" | "verified" | "mismatch";
  readonly eventStream: "unknown" | "ready" | "unavailable";
  readonly permissionDecision: "unknown" | "allow" | "ask" | "deny" | "rejected" | "cancelled";
  readonly lastFailure?: string;
}

export interface NativePermissionBridge {
  authorizeEdit(intent: NativePermissionIntent, invocation: NativePermissionInvocation): Promise<NativePermissionReceipt>;
  diagnostics(): NativePermissionBridgeDiagnostics;
  dispose(): Promise<void>;
}

export type NativePermissionBridgeClient = HostClient;

export interface NativePermissionBridgeOptions {
  readonly discover?: () => Promise<Endpoint | undefined>;
  readonly makeClient?: (endpoint: Endpoint) => NativePermissionBridgeClient;
  readonly realpath?: (path: string) => Promise<string>;
  readonly proofTimeoutMs?: number;
}

type BridgeFailureCode =
  | "missing-abort-signal"
  | "invocation-cancelled"
  | "bridge-disposed"
  | "invalid-intent"
  | "resource-outside-project"
  | "resource-not-canonical"
  | "rpc-unavailable"
  | "service-unavailable"
  | "client-unavailable"
  | "host-info-unavailable"
  | "host-mismatch"
  | "event-stream-unavailable"
  | "event-stream-disconnected"
  | "session-unavailable"
  | "session-location-mismatch"
  | "permission-request-failed"
  | "permission-denied"
  | "permission-ask-unverified"
  | "permission-rejected"
  | "permission-result-unknown";

class NativePermissionBridgeError extends Error {
  constructor(readonly code: BridgeFailureCode, message: string) {
    super(message);
    this.name = "NativePermissionBridgeError";
  }
}

interface BridgeState {
  rpcRegistration: "available" | "unavailable";
  serviceDiscovery: "unknown" | "available" | "unavailable";
  runtimeVersion?: string;
  hostBinding: "unknown" | "verified" | "mismatch";
  eventStream: "unknown" | "ready" | "unavailable";
  permissionDecision: "unknown" | "allow" | "ask" | "deny" | "rejected" | "cancelled";
  lastFailure?: string;
}

interface LinkedSignal {
  readonly signal: AbortSignal;
  readonly controller: AbortController;
  dispose(): void;
}

interface EventWait<T> {
  readonly promise: Promise<T>;
  cancel(): void;
}

interface EventWaiter<T> {
  readonly read: (event: OpenCodeEvent) => T | undefined;
  readonly resolve: (value: T) => void;
  readonly reject: (error: Error) => void;
}

interface ProofObservation {
  readonly challenge: string;
  readonly proof: string;
  readonly location?: string;
}

interface PermissionReplyObservation {
  readonly requestID: string;
  readonly sessionID: string;
  readonly reply: "once" | "always" | "reject";
  readonly location?: string;
}

function fail(code: BridgeFailureCode, message: string): NativePermissionBridgeError {
  return new NativePermissionBridgeError(code, message);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isUsableSignal(value: unknown): value is AbortSignal {
  if (!isRecord(value)) return false;
  return typeof value.aborted === "boolean" &&
    typeof value.addEventListener === "function" &&
    typeof value.removeEventListener === "function";
}

function requireSignal(value: unknown): asserts value is AbortSignal {
  if (!isUsableSignal(value)) {
    throw fail(
      "missing-abort-signal",
      "Native workplan writes require the OpenCode invocation AbortSignal; this host cannot safely authorize a write without it.",
    );
  }
  if (value.aborted) {
    throw fail("invocation-cancelled", "The workplan permission request was cancelled; no write authorization was granted.");
  }
}

function linkSignal(parent: AbortSignal): LinkedSignal {
  const controller = new AbortController();
  const forwardAbort = () => controller.abort();
  parent.addEventListener("abort", forwardAbort, { once: true });
  if (parent.aborted) controller.abort();
  return {
    signal: controller.signal,
    controller,
    dispose() {
      parent.removeEventListener("abort", forwardAbort);
      if (!controller.signal.aborted) controller.abort();
    },
  };
}

function activeError(parent: AbortSignal, linked: AbortSignal, streamFailure?: Error): Error | undefined {
  if (streamFailure) return streamFailure;
  if (parent.aborted) {
    return fail("invocation-cancelled", "The workplan permission request was cancelled; no write authorization was granted.");
  }
  if (linked.aborted) {
    return fail("bridge-disposed", "The workplan permission bridge stopped before authorization completed.");
  }
}

function checkActive(parent: AbortSignal, linked: AbortSignal, streamFailure?: Error): void {
  const error = activeError(parent, linked, streamFailure);
  if (error) throw error;
}

function runCancellable<T>(
  operation: () => Promise<T>,
  parent: AbortSignal,
  linked: AbortSignal,
  failure: BridgeFailureCode,
  message: string,
  getStreamFailure: () => Error | undefined,
): Promise<T> {
  checkActive(parent, linked, getStreamFailure());
  let request: Promise<T>;
  try {
    request = operation();
  } catch {
    return Promise.reject(fail(failure, message));
  }
  return new Promise<T>((resolveRequest, rejectRequest) => {
    let settled = false;
    const cleanup = () => linked.removeEventListener("abort", onAbort);
    const settle = (callback: () => void) => {
      if (settled) return;
      settled = true;
      cleanup();
      callback();
    };
    const onAbort = () => settle(() => rejectRequest(activeError(parent, linked, getStreamFailure()) ?? fail("invocation-cancelled", "The workplan permission request was cancelled.")));
    linked.addEventListener("abort", onAbort, { once: true });
    if (linked.aborted) {
      onAbort();
      return;
    }
    request.then(
      (result) => {
        const error = activeError(parent, linked, getStreamFailure());
        if (error) settle(() => rejectRequest(error));
        else settle(() => resolveRequest(result));
      },
      () => {
        const error = activeError(parent, linked, getStreamFailure());
        settle(() => rejectRequest(error ?? fail(failure, message)));
      },
    );
  });
}

function runWithTimeout<T>(
  operation: Promise<T>,
  timeoutMs: number,
  parent: AbortSignal,
  linked: AbortSignal,
  getStreamFailure: () => Error | undefined,
): Promise<T> {
  checkActive(parent, linked, getStreamFailure());
  return new Promise<T>((resolveOperation, rejectOperation) => {
    let settled = false;
    let timer: ReturnType<typeof setTimeout>;
    const cleanup = () => {
      clearTimeout(timer);
      linked.removeEventListener("abort", onAbort);
    };
    const settle = (callback: () => void) => {
      if (settled) return;
      settled = true;
      cleanup();
      callback();
    };
    const onAbort = () => settle(() => rejectOperation(activeError(parent, linked, getStreamFailure()) ?? fail("invocation-cancelled", "The workplan permission request was cancelled.")));
    timer = setTimeout(() => settle(() => rejectOperation(fail(
      "event-stream-unavailable",
      "The public event stream did not prove readiness for this plugin instance; no permission request was sent.",
    ))), timeoutMs);
    linked.addEventListener("abort", onAbort, { once: true });
    if (linked.aborted) {
      onAbort();
      return;
    }
    operation.then(
      (result) => {
        const error = activeError(parent, linked, getStreamFailure());
        if (error) settle(() => rejectOperation(error));
        else settle(() => resolveOperation(result));
      },
      (error: unknown) => settle(() => rejectOperation(error instanceof Error ? error : fail("host-mismatch", "The host proof could not be verified."))),
    );
  });
}

function errorCode(error: unknown): string | undefined {
  if (!isRecord(error)) return undefined;
  return typeof error.code === "string" ? error.code : undefined;
}

async function canonicalExistingPath(path: string, signal: AbortSignal, realpathFn: (path: string) => Promise<string>): Promise<string> {
  let cursor = path;
  const missing: string[] = [];
  while (true) {
    if (signal.aborted) throw fail("invocation-cancelled", "The workplan permission request was cancelled before path validation completed.");
    try {
      const existing = await realpathFn(cursor);
      if (signal.aborted) throw fail("invocation-cancelled", "The workplan permission request was cancelled before path validation completed.");
      return resolve(existing, ...missing);
    } catch (error) {
      if (error instanceof NativePermissionBridgeError) throw error;
      if (errorCode(error) !== "ENOENT" && errorCode(error) !== "ENOTDIR") {
        throw fail("resource-not-canonical", "A requested workplan resource could not be verified as a canonical project path.");
      }
      const parent = dirname(cursor);
      if (parent === cursor) {
        throw fail("resource-not-canonical", "A requested workplan resource has no verifiable canonical parent path.");
      }
      missing.unshift(basename(cursor));
      cursor = parent;
    }
  }
}

async function validateIntent(
  root: string,
  intent: NativePermissionIntent,
  signal: AbortSignal,
  realpathFn: (path: string) => Promise<string>,
): Promise<readonly string[]> {
  if (!intent.sessionID || !intent.agent || !intent.messageID || !intent.toolCallID || intent.resources.length === 0) {
    throw fail("invalid-intent", "A native edit authorization needs trusted session, agent, message, tool-call, and resource identities.");
  }
  const resources: string[] = [];
  for (const resource of intent.resources) {
    if (typeof resource !== "string" || !isAbsolute(resource) || resolve(resource) !== resource || resource.includes("\0") || /[*?\[\]]/.test(resource)) {
      throw fail("invalid-intent", "Native workplan permissions accept only exact canonical absolute file resources, never globs or directory-wide rules.");
    }
    const withinRoot = relative(root, resource);
    if (!withinRoot || withinRoot === ".." || withinRoot.startsWith(`..${sep}`) || isAbsolute(withinRoot)) {
      throw fail("resource-outside-project", "A requested workplan edit resource is outside the canonical active project.");
    }
    const canonical = await canonicalExistingPath(resource, signal, realpathFn);
    if (canonical !== resource) {
      throw fail("resource-not-canonical", "A requested workplan edit resource resolves through a non-canonical path; no permission was requested.");
    }
    resources.push(resource);
  }
  if (new Set(resources).size !== resources.length) {
    throw fail("invalid-intent", "A native workplan permission request cannot contain duplicate resources.");
  }
  return Object.freeze(resources.sort());
}

function withinProjectRoot(location: string | undefined, root: string): boolean {
  if (!location || !isAbsolute(location)) return false;
  try {
    return realpathSync(location) === root;
  } catch {
    return false;
  }
}

function proofMessage(instanceNonce: Buffer, root: string, challenge: string): string {
  return `${instanceNonce.toString("hex")}\0${root}\0${challenge}`;
}

function makeProof(secret: Buffer, instanceNonce: Buffer, root: string, challenge: string): string {
  return createHmac("sha256", secret).update(proofMessage(instanceNonce, root, challenge)).digest("hex");
}

function matchesProof(expected: string, actual: unknown): boolean {
  if (typeof actual !== "string" || !/^[0-9a-f]{64}$/.test(actual)) return false;
  const expectedBytes = Buffer.from(expected, "hex");
  const actualBytes = Buffer.from(actual, "hex");
  return expectedBytes.length === actualBytes.length && timingSafeEqual(expectedBytes, actualBytes);
}

function eventProof(event: OpenCodeEvent): ProofObservation | undefined {
  if (event.type !== `rpc.${BRIDGE_RPC_ID}.proof` || !isRecord(event.data)) return undefined;
  if (typeof event.data.challenge !== "string" || typeof event.data.proof !== "string") return undefined;
  return {
    challenge: event.data.challenge,
    proof: event.data.proof,
    location: event.location?.directory,
  };
}

function proofReply(value: unknown): { challenge: string; proof: string } | undefined {
  if (!isRecord(value) || typeof value.challenge !== "string" || typeof value.proof !== "string") return undefined;
  return { challenge: value.challenge, proof: value.proof };
}

function eventReply(event: OpenCodeEvent, sessionID: string, requestID?: string): PermissionReplyObservation | undefined {
  if (event.type !== "permission.replied" || event.data.sessionID !== sessionID) return undefined;
  if (requestID && event.data.requestID !== requestID) return undefined;
  return {
    requestID: event.data.requestID,
    sessionID: event.data.sessionID,
    reply: event.data.reply,
    location: event.location?.directory,
  };
}

class EventMonitor {
  readonly #waiters = new Set<EventWaiter<unknown>>();
  readonly #recentReplies: OpenCodeEvent[] = [];
  #iterator?: AsyncIterator<OpenCodeEvent>;
  #reader?: Promise<void>;
  #failure?: NativePermissionBridgeError;
  #closing = false;

  constructor(
    private readonly stream: AsyncIterable<OpenCodeEvent>,
    private readonly signal: AbortSignal,
    private readonly sessionID: string,
    private readonly onFailure: (error: NativePermissionBridgeError) => void,
  ) {}

  start(): void {
    this.#reader = this.#read();
  }

  waitFor<T>(read: (event: OpenCodeEvent) => T | undefined): EventWait<T> {
    if (this.#failure) throw this.#failure;
    if (this.signal.aborted) throw fail("invocation-cancelled", "The workplan permission request was cancelled while waiting for a runtime event.");
    const cached = this.#recentReplies.findIndex((event) => read(event) !== undefined);
    if (cached >= 0) {
      const [event] = this.#recentReplies.splice(cached, 1);
      return { promise: Promise.resolve(read(event)!), cancel() {} };
    }
    let waiter!: EventWaiter<T>;
    let finish!: (callback: () => void) => void;
    const promise = new Promise<T>((resolveEvent, rejectEvent) => {
      let settled = false;
      const cleanup = () => {
        this.#waiters.delete(waiter as EventWaiter<unknown>);
        this.signal.removeEventListener("abort", onAbort);
      };
      finish = (callback) => {
        if (settled) return;
        settled = true;
        cleanup();
        callback();
      };
      const onAbort = () => finish(() => rejectEvent(this.#failure ?? fail(
        "invocation-cancelled",
        "The workplan permission request was cancelled while waiting for a runtime event.",
      )));
      waiter = {
        read,
        resolve: (value) => finish(() => resolveEvent(value)),
        reject: (error) => finish(() => rejectEvent(error)),
      };
      this.#waiters.add(waiter as EventWaiter<unknown>);
      this.signal.addEventListener("abort", onAbort, { once: true });
      if (this.signal.aborted) onAbort();
    });
    return {
      promise,
      cancel() {
        finish(() => {});
      },
    };
  }

  assertHealthy(): void {
    if (this.#failure) throw this.#failure;
    if (this.signal.aborted) throw fail("invocation-cancelled", "The workplan permission request was cancelled; no write authorization was granted.");
  }

  async close(): Promise<void> {
    this.#closing = true;
    try {
      await this.#iterator?.return?.();
    } catch {
      // The linked invocation signal also closes the public stream.
    }
    await this.#reader?.catch(() => {});
    for (const waiter of this.#waiters) waiter.reject(fail("invocation-cancelled", "The workplan permission event listener was closed."));
    this.#waiters.clear();
  }

  get failure(): NativePermissionBridgeError | undefined {
    return this.#failure;
  }

  async #read(): Promise<void> {
    try {
      this.#iterator = this.stream[Symbol.asyncIterator]();
      while (!this.#closing && !this.signal.aborted) {
        const result = await this.#iterator.next();
        if (result.done) {
          if (!this.#closing && !this.signal.aborted) this.#fail("The authenticated OpenCode event stream disconnected during authorization.");
          return;
        }
        this.#dispatch(result.value);
      }
    } catch {
      if (!this.#closing && !this.signal.aborted) this.#fail("The authenticated OpenCode event stream disconnected during authorization.");
    }
  }

  #dispatch(event: OpenCodeEvent): void {
    let matched = false;
    for (const waiter of [...this.#waiters]) {
      const value = waiter.read(event);
      if (value !== undefined) {
        matched = true;
        waiter.resolve(value);
      }
    }
    if (!matched && event.type === "permission.replied" && event.data.sessionID === this.sessionID) {
      this.#recentReplies.push(event);
      if (this.#recentReplies.length > REPLY_BUFFER_SIZE) this.#recentReplies.shift();
    }
  }

  #fail(message: string): void {
    if (this.#failure || this.#closing) return;
    this.#failure = fail("event-stream-disconnected", message);
    this.onFailure(this.#failure);
    for (const waiter of this.#waiters) waiter.reject(this.#failure);
    this.#waiters.clear();
  }
}

function sameSource(
  actual: PermissionCreateInput["source"],
  intent: NativePermissionIntent,
): boolean {
  return actual?.type === "tool" && actual.messageID === intent.messageID && actual.id === intent.toolCallID;
}

function sameResources(actual: readonly string[], expected: readonly string[]): boolean {
  return actual.length === expected.length && actual.every((resource, index) => resource === expected[index]);
}

function markerMatches(value: unknown, authorizationID: string, agent: string): boolean {
  return isRecord(value) && value.version === 1 && value.authorizationID === authorizationID && value.agent === agent;
}

function makeClient(endpoint: Endpoint): NativePermissionBridgeClient {
  return makeHostClient(endpoint);
}

export async function registerNativePermissionBridge(
  context: NativePermissionBridgeContext,
  options: NativePermissionBridgeOptions = {},
): Promise<NativePermissionBridge> {
  const realpathFn = options.realpath ?? realpath;
  let canonicalRoot: string;
  try {
    canonicalRoot = await realpathFn(context.location.project.directory);
  } catch {
    throw fail("resource-not-canonical", "The native permission bridge could not resolve the active project to a canonical directory.");
  }

  const secret = randomBytes(32);
  const instanceNonce = randomBytes(32);
  const state: BridgeState = {
    rpcRegistration: "unavailable",
    serviceDiscovery: "unknown",
    hostBinding: "unknown",
    eventStream: "unknown",
    permissionDecision: "unknown",
  };
  const active = new Map<AbortController, Promise<void>>();
  let disposed = false;
  const instanceProof = (challenge: string) => makeProof(secret, instanceNonce, canonicalRoot, challenge);
  let emitProof: (event: { challenge: string; proof: string }) => Promise<void> = async () => {
    throw fail("rpc-unavailable", "The workplan host-proof RPC is not registered; native writes remain disabled.");
  };
  let rpcRegistered = false;
  let disposeRegistration: (() => Promise<void>) | undefined;
  try {
    const rpcRegistration = await context.rpc.register(WorkplanPermissionRpc, {
      async prove(input: unknown, rpcContext: { signal: AbortSignal }) {
        if (disposed) {
          throw fail("bridge-disposed", "The native workplan permission bridge has been disposed.");
        }
        if (rpcContext.signal.aborted) {
          throw fail("invocation-cancelled", "The public workplan host-proof request was cancelled.");
        }
        const challenge = isRecord(input) ? input.challenge : undefined;
        if (typeof challenge !== "string" || !/^[0-9a-f]{64}$/.test(challenge)) {
          throw fail("host-mismatch", "The workplan host-proof challenge was invalid.");
        }
        const proof = instanceProof(challenge);
        await emitProof({ challenge, proof });
        if (disposed || rpcContext.signal.aborted) {
          throw fail("invocation-cancelled", "The public workplan host-proof request was cancelled.");
        }
        return { challenge, proof };
      },
    });
    emitProof = (event) => rpcRegistration.events.emit("proof", event);
    disposeRegistration = () => rpcRegistration.dispose();
    rpcRegistered = true;
    state.rpcRegistration = "available";
  } catch {
    state.lastFailure = "rpc-unavailable";
  }

  const discover = options.discover ?? (() => discoverService());
  const createClient = options.makeClient ?? makeClient;
  const proofTimeoutMs = options.proofTimeoutMs ?? PROOF_TIMEOUT_MS;

  const bridge: NativePermissionBridge = {
    async authorizeEdit(intent, invocation) {
      requireSignal(invocation?.signal);
      if (disposed) throw fail("bridge-disposed", "The native workplan permission bridge has been disposed; no write authorization was granted.");
      if (!rpcRegistered) {
        state.lastFailure = "rpc-unavailable";
        throw fail("rpc-unavailable", "This OpenCode host cannot register the required public workplan RPC proof; native writes are disabled and no permission was requested.");
      }
      state.serviceDiscovery = "unknown";
      state.hostBinding = "unknown";
      state.eventStream = "unknown";
      state.permissionDecision = "unknown";
      state.runtimeVersion = undefined;
      state.lastFailure = undefined;

      const resources = await validateIntent(canonicalRoot, intent, invocation.signal, realpathFn);
      const linked = linkSignal(invocation.signal);
      let finishActive!: () => void;
      const completion = new Promise<void>((resolveCompletion) => { finishActive = resolveCompletion; });
      active.set(linked.controller, completion);
      let monitor: EventMonitor | undefined;
      let askWait: EventWait<{ requestID: string; sessionID: string; action: string; resources: readonly string[]; source?: PermissionCreateInput["source"]; metadata?: Record<string, unknown>; location?: string }> | undefined;
      try {
        checkActive(invocation.signal, linked.signal);
        let endpoint: Endpoint | undefined;
        try {
          endpoint = await runCancellable(discover, invocation.signal, linked.signal, "service-unavailable", "No authenticated local OpenCode service is discoverable; native writes remain disabled.", () => monitor?.failure);
        } catch (error) {
          state.serviceDiscovery = "unavailable";
          state.lastFailure = error instanceof NativePermissionBridgeError ? error.code : "service-unavailable";
          throw error;
        }
        if (!endpoint) {
          state.serviceDiscovery = "unavailable";
          state.lastFailure = "service-unavailable";
          throw fail("service-unavailable", "No authenticated local OpenCode service is discoverable; native writes remain disabled.");
        }
        state.serviceDiscovery = "available";

        let client: NativePermissionBridgeClient;
        try {
          client = createClient(endpoint);
        } catch {
          state.lastFailure = "client-unavailable";
          throw fail("client-unavailable", "The discovered OpenCode service could not create a public client; no permission was requested.");
        }

        let info: Awaited<ReturnType<HostClient["server"]["info"]>>;
        try {
          info = await runCancellable(() => client.server.info({ signal: linked.signal }), invocation.signal, linked.signal, "host-info-unavailable", "The discovered OpenCode service could not be authenticated or queried; native writes are disabled.", () => monitor?.failure);
        } catch (error) {
          state.lastFailure = error instanceof NativePermissionBridgeError ? error.code : "host-info-unavailable";
          throw error;
        }
        state.runtimeVersion = typeof info.version === "string" ? info.version : undefined;

        try {
          monitor = new EventMonitor(client.event.subscribe({ signal: linked.signal }), linked.signal, intent.sessionID, (error) => {
            state.eventStream = "unavailable";
            state.lastFailure = error.code;
            linked.controller.abort();
          });
          monitor.start();
        } catch {
          state.eventStream = "unavailable";
          state.lastFailure = "event-stream-unavailable";
          throw fail("event-stream-unavailable", "The public OpenCode event stream is unavailable; no permission request was sent.");
        }

        const challenge = randomBytes(32).toString("hex");
        const expectedProof = instanceProof(challenge);
        const proofWait = monitor.waitFor((event) => {
          const observed = eventProof(event);
          return observed?.challenge === challenge ? observed : undefined;
        });
        let rpcProof: { challenge: string; proof: string } | undefined;
        let proofEvent: ProofObservation;
        try {
          const [rpcProofValue, observedProof] = await runWithTimeout(
            Promise.all([
              runCancellable(
                () => client.rpc.prove({ challenge }, {
                  signal: linked.signal,
                  location: { directory: canonicalRoot },
                }),
                invocation.signal,
                linked.signal,
                "host-mismatch",
                "The discovered service did not prove the executing workplan plugin instance; no permission was requested.",
                () => monitor?.failure,
              ),
              proofWait.promise,
            ]),
            proofTimeoutMs,
            invocation.signal,
            linked.signal,
            () => monitor?.failure,
          );
          rpcProof = proofReply(rpcProofValue);
          proofEvent = observedProof;
        } catch (error) {
          proofWait.cancel();
          if (invocation.signal.aborted) {
            state.permissionDecision = "cancelled";
            state.lastFailure = "invocation-cancelled";
            throw fail("invocation-cancelled", "The workplan permission request was cancelled during host proof; no permission request was sent.");
          }
          if (monitor.failure) {
            state.eventStream = "unavailable";
            state.lastFailure = monitor.failure.code;
            throw monitor.failure;
          }
          if (error instanceof NativePermissionBridgeError && error.code === "event-stream-unavailable") {
            state.eventStream = "unavailable";
            state.lastFailure = error.code;
            throw error;
          }
          state.hostBinding = "mismatch";
          state.eventStream = "unavailable";
          state.lastFailure = error instanceof NativePermissionBridgeError ? error.code : "host-mismatch";
          throw fail("host-mismatch", "The discovered service did not prove this plugin instance and canonical project through its event stream; no permission was requested.");
        }
        monitor.assertHealthy();
        if (!rpcProof || rpcProof.challenge !== challenge || proofEvent.challenge !== challenge ||
          !matchesProof(expectedProof, rpcProof.proof) || !matchesProof(expectedProof, proofEvent.proof) ||
          proofEvent.location !== canonicalRoot) {
          state.hostBinding = "mismatch";
          state.eventStream = "unavailable";
          state.lastFailure = "host-mismatch";
          throw fail("host-mismatch", "The discovered service reached a different workplan plugin instance or project location; no permission was requested.");
        }
        state.hostBinding = "verified";
        state.eventStream = "ready";

        let session: Awaited<ReturnType<HostClient["session"]["get"]>>;
        try {
          session = await runCancellable(
            () => client.session.get({ sessionID: intent.sessionID }, { signal: linked.signal }),
            invocation.signal,
            linked.signal,
            "session-unavailable",
            "The active session could not be verified at the canonical workplan project; no permission was requested.",
            () => monitor?.failure,
          );
        } catch (error) {
          state.lastFailure = error instanceof NativePermissionBridgeError ? error.code : "session-unavailable";
          throw error;
        }
        if (session.id !== intent.sessionID || !withinProjectRoot(session.location?.directory, canonicalRoot)) {
          state.lastFailure = "session-location-mismatch";
          throw fail("session-location-mismatch", "The active session is not owned by this canonical project location; no permission was requested.");
        }

        const authorizationID = randomUUID();
        const source = { type: "tool" as const, messageID: intent.messageID, id: intent.toolCallID };
        const request: PermissionCreateInput = {
          sessionID: intent.sessionID,
          action: "edit",
          resources,
          agent: intent.agent,
          source,
          metadata: { workplanToolsBridge: { version: 1, authorizationID, agent: intent.agent } },
        };
        askWait = monitor.waitFor((event) => {
          if (event.type !== "permission.asked" || event.data.sessionID !== intent.sessionID) return undefined;
          if (event.data.action !== "edit" || !sameResources(event.data.resources, resources) ||
            !sameSource(event.data.source, intent) ||
            !markerMatches(event.data.metadata?.workplanToolsBridge, authorizationID, intent.agent) ||
            !withinProjectRoot(event.location?.directory, canonicalRoot)) return undefined;
          return {
            requestID: event.data.id,
            sessionID: event.data.sessionID,
            action: event.data.action,
            resources: event.data.resources,
            source: event.data.source,
            metadata: event.data.metadata,
            location: event.location?.directory,
          };
        });
        void askWait.promise.catch(() => {});

        let permissionResult: Awaited<ReturnType<HostClient["permission"]["create"]>>;
        try {
          permissionResult = await runCancellable(
            () => client.permission.create(request, { signal: linked.signal }),
            invocation.signal,
            linked.signal,
            "permission-request-failed",
            "OpenCode did not return a definitive permission result for this exact edit intent; no workplan files were changed.",
            () => monitor?.failure,
          );
        } catch (error) {
          askWait.cancel();
          state.permissionDecision = invocation.signal.aborted ? "cancelled" : "rejected";
          state.lastFailure = error instanceof NativePermissionBridgeError ? error.code : "permission-request-failed";
          throw error instanceof NativePermissionBridgeError ? error : fail(
            "permission-request-failed",
            "OpenCode did not return a definitive permission result for this exact edit intent; no workplan files were changed.",
          );
        }
        checkActive(invocation.signal, linked.signal, monitor.failure);
        monitor.assertHealthy();

        let via: NativePermissionReceipt["via"];
        if (permissionResult.effect === "allow") {
          askWait.cancel();
          via = "runtime-policy";
          state.permissionDecision = "allow";
        } else if (permissionResult.effect === "deny") {
          askWait.cancel();
          state.permissionDecision = "deny";
          state.lastFailure = "permission-denied";
          throw fail("permission-denied", "OpenCode denied this exact edit intent; no workplan files were changed.");
        } else if (permissionResult.effect === "ask" && permissionResult.id) {
          state.permissionDecision = "ask";
          let asked: Awaited<typeof askWait.promise>;
          try {
            asked = await runCancellable(
              () => askWait!.promise,
              invocation.signal,
              linked.signal,
              "permission-ask-unverified",
              "The pending permission could not be correlated to this exact edit intent; no workplan files were changed.",
              () => monitor?.failure,
            );
          } catch (error) {
            if (error instanceof NativePermissionBridgeError && error.code === "invocation-cancelled") throw error;
            if (invocation.signal.aborted) {
              state.permissionDecision = "cancelled";
              state.lastFailure = "invocation-cancelled";
              throw fail("invocation-cancelled", "The workplan permission request was cancelled; a later user reply cannot authorize this invocation.");
            }
            if (monitor.failure) throw monitor.failure;
            state.lastFailure = "permission-ask-unverified";
            throw fail("permission-ask-unverified", "The pending permission could not be correlated to this exact edit intent; no workplan files were changed.");
          }
          const marker = asked.metadata?.workplanToolsBridge;
          if (asked.requestID !== permissionResult.id || asked.sessionID !== intent.sessionID || asked.action !== "edit" ||
            !sameResources(asked.resources, resources) || !sameSource(asked.source, intent) ||
            !markerMatches(marker, authorizationID, intent.agent) || !withinProjectRoot(asked.location, canonicalRoot)) {
            state.permissionDecision = "rejected";
            state.lastFailure = "permission-ask-unverified";
            throw fail("permission-ask-unverified", "OpenCode's ask event did not match the session, source, agent, canonical location, and exact resources; no workplan files were changed.");
          }

          let replyWait: EventWait<PermissionReplyObservation>;
          try {
            replyWait = monitor.waitFor((event) => eventReply(event, intent.sessionID, permissionResult.id));
          } catch {
            throw fail("permission-ask-unverified", "The OpenCode event stream stopped before the real user permission reply; no workplan files were changed.");
          }
          let reply: Awaited<typeof replyWait.promise>;
          try {
            reply = await runCancellable(
              () => replyWait.promise,
              invocation.signal,
              linked.signal,
              "permission-ask-unverified",
              "The real user permission reply was not received; no workplan files were changed.",
              () => monitor?.failure,
            );
          } catch {
            state.permissionDecision = invocation.signal.aborted ? "cancelled" : "rejected";
            state.lastFailure = monitor.failure?.code ?? (invocation.signal.aborted ? "invocation-cancelled" : "permission-ask-unverified");
            throw fail(
              invocation.signal.aborted ? "invocation-cancelled" : "permission-ask-unverified",
              invocation.signal.aborted
                ? "The workplan permission request was cancelled; a later user reply cannot authorize this invocation."
                : "The real user permission reply was not received; no workplan files were changed.",
            );
          }
          if (!withinProjectRoot(reply.location, canonicalRoot) || reply.requestID !== permissionResult.id || reply.sessionID !== intent.sessionID) {
            state.permissionDecision = "rejected";
            state.lastFailure = "permission-ask-unverified";
            throw fail("permission-ask-unverified", "The permission reply did not match this request, session, and canonical project; no workplan files were changed.");
          }
          if (reply.reply === "reject") {
            state.permissionDecision = "rejected";
            state.lastFailure = "permission-rejected";
            throw fail("permission-rejected", "The user rejected this exact workplan edit request; no workplan files were changed.");
          }
          if (reply.reply !== "once" && reply.reply !== "always") {
            state.permissionDecision = "rejected";
            state.lastFailure = "permission-result-unknown";
            throw fail("permission-result-unknown", "OpenCode returned an unsupported permission reply; no workplan files were changed.");
          }
          state.permissionDecision = "allow";
          via = "user-reply";
        } else {
          askWait.cancel();
          state.permissionDecision = "rejected";
          state.lastFailure = "permission-result-unknown";
          throw fail("permission-result-unknown", "OpenCode returned an unsupported permission result; no workplan files were changed.");
        }

        checkActive(invocation.signal, linked.signal, monitor.failure);
        monitor.assertHealthy();
        return Object.freeze({
          decision: "allow",
          via,
          authorizationID,
          requestID: permissionResult.id,
          sessionID: intent.sessionID,
          agent: intent.agent,
          source: Object.freeze(source),
          resources,
        });
      } catch (error) {
        if (error instanceof NativePermissionBridgeError) {
          state.lastFailure = error.code;
          if (error.code === "invocation-cancelled") state.permissionDecision = "cancelled";
          throw error;
        }
        state.lastFailure = "permission-request-failed";
        throw fail("permission-request-failed", "The native workplan permission bridge failed closed; no workplan files were changed.");
      } finally {
        askWait?.cancel();
        linked.dispose();
        await monitor?.close();
        active.delete(linked.controller);
        finishActive();
      }
    },

    diagnostics() {
      return Object.freeze({ clientVersion: "2.0.20", ...state });
    },

    async dispose() {
      if (disposed) return;
      disposed = true;
      for (const controller of active.keys()) controller.abort();
      await Promise.all([...active.values()]);
      try {
        await disposeRegistration?.();
      } catch {
        // Unloading remains fail-closed even if host registration cleanup fails.
      }
      secret.fill(0);
      instanceNonce.fill(0);
    },
  };

  return bridge;
}
