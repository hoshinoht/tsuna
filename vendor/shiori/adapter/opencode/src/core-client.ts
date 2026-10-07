// The adapter side of `shiori serve --stdio` (spec 02 §2/§6, contracts
// §5.3/§5.4): one lazily started child per plugin instance, a versioned
// JSON-lines protocol with bounded frames, requestId multiplexing, cancel
// frames bridged from AbortSignal, and fail-closed transport loss. Nothing
// is replayed after a crash: the next call respawns and handshakes again,
// and prepared intents from the old child are gone.
import { spawn as nodeSpawn, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import { constants } from "node:fs";
import { access } from "node:fs/promises";
import { isAbsolute } from "node:path";

export const PROTOCOL_VERSION = 1;
export const CONTRACT_VERSION = "v1";
export const TOOL_OPERATIONS = [
  "workplan_create", "workplan_update", "workplan_patch", "workplan_reset",
  "workplan_read", "workplan_list", "workplan_inspect", "workplan_validate",
  "workplan_checkpoint", "workplan_resume", "workplan_compact", "workplan_doctor",
  "workplan_compact_preview",
] as const;
export const CONTROL_OPERATIONS = ["shiori.handshake", "shiori.commit", "shiori.discard"] as const;

const DEFAULT_MAX_FRAME_BYTES = 16 * 1024 * 1024;
const DEFAULT_MAX_RESPONSE_BYTES = 64 * 1024 * 1024;
const MAX_NESTING = 128;
const STDERR_TAIL_BYTES = 8 * 1024;

export interface HostContext {
  readonly mode: "native";
  readonly canonicalRoot: string;
  readonly sessionID: string;
  readonly agent: string;
  readonly messageID: string;
  readonly callID: string;
  readonly runtimeFacts?: unknown;
}

export interface Handshake {
  readonly coreVersion: string;
  readonly protocolVersion: number;
  readonly contractVersion: string;
  readonly operations: readonly string[];
  readonly platform: { readonly os: string; readonly arch: string; readonly supported: boolean };
  readonly durability: { readonly atomicRename: string; readonly fileSync: string; readonly directorySync: string };
  readonly writeSupported: boolean;
  readonly limits: { readonly maxFrameBytes: number; readonly maxResponseBytes?: number; readonly maxNesting: number };
}

export interface PreparedIntent {
  readonly intentId: string;
  readonly intentDigest: string;
  readonly capability: string;
  readonly operation: string;
  readonly tool: string;
  readonly workplanId: string;
  readonly canonicalRoot: string;
  readonly expectedStateHash: string | null;
  readonly resources: {
    readonly readPaths: readonly string[];
    readonly writePaths: readonly string[];
    readonly deletePaths: readonly string[];
    readonly lockPaths: readonly string[];
    readonly stagingPaths: readonly string[];
    readonly archivePaths: readonly string[];
  };
  readonly targets: readonly { path: string; kind?: string; beforeHash: string | null; afterHash: string | null }[];
}

export interface CoreResponse {
  readonly requestId: string;
  readonly result?: { readonly text?: string; readonly discarded?: boolean };
  readonly prepared?: PreparedIntent;
  readonly hashes?: { readonly planHash?: string; readonly stateHash?: string };
}

/** A structured core or transport failure (protocol error classes). */
export class ShioriError extends Error {
  constructor(
    readonly errorClass: string,
    message: string,
    readonly detail: { issues?: unknown; currentStateHash?: string; retrieval?: string; recoveryJournal?: string } = {},
  ) {
    super(message);
    this.name = "ShioriError";
  }
}

export interface CoreClientOptions {
  /** Absolute path of the `shiori` binary (plugin option `bin` or SHIORI_BIN). */
  readonly bin: string | undefined;
  readonly args?: readonly string[];
  readonly env?: NodeJS.ProcessEnv;
  readonly spawn?: typeof nodeSpawn;
  readonly clientName?: string;
  /** Unload grace periods (contracts §5.4: 2 s then SIGTERM, 2 s then SIGKILL). */
  readonly closeGraceMs?: number;
  readonly termGraceMs?: number;
  readonly onDiagnostic?: (line: string) => void;
}

interface Pending {
  readonly operation: string;
  readonly resolve: (response: CoreResponse) => void;
  readonly reject: (error: Error) => void;
}

interface Session {
  readonly child: ChildProcess;
  readonly handshake: Handshake;
  readonly pending: Map<string, Pending>;
  readonly exited: Promise<void>;
  dead: boolean;
}

export function redact(text: string): string {
  return text
    .replace(/\b(basic|bearer)\s+[A-Za-z0-9+/=._~-]+/gi, "$1 [redacted]")
    .replace(/\b(password|passwd|secret|token|capability|api[_-]?key)(["']?\s*[:=]\s*["']?)[^\s"',}]+/gi, "$1$2[redacted]")
    .replace(/\b[0-9a-f]{64}\b/g, (m) => `${m.slice(0, 8)}…`);
}

function depth(value: unknown, level = 1): number {
  if (value === null || typeof value !== "object") return level - 1;
  let max = level;
  for (const child of Array.isArray(value) ? value : Object.values(value)) {
    const d = depth(child, level + 1);
    if (d > max) max = d;
    if (max > MAX_NESTING) return max;
  }
  return max;
}

function validateHandshake(value: unknown): Handshake {
  const h = value as Handshake;
  const unsupported = (why: string) =>
    new ShioriError("unsupported_protocol", `The Shiori core at the configured binary is not supported by this adapter (${why}); install a matching shiori build and check SHIORI_BIN or the plugin option "bin".`);
  if (!h || typeof h !== "object") throw unsupported("malformed handshake");
  if (h.protocolVersion !== PROTOCOL_VERSION) throw unsupported(`protocolVersion ${String(h.protocolVersion)}`);
  if (h.contractVersion !== CONTRACT_VERSION) throw unsupported(`contractVersion ${String(h.contractVersion)}`);
  const ops = new Set(Array.isArray(h.operations) ? h.operations : []);
  const missing = [...TOOL_OPERATIONS, ...CONTROL_OPERATIONS].filter((op) => !ops.has(op));
  if (missing.length) throw unsupported(`missing operations ${missing.join(", ")}`);
  if (typeof h.writeSupported !== "boolean" || !h.durability || !h.limits || typeof h.limits.maxFrameBytes !== "number") {
    throw unsupported("incomplete capability facts");
  }
  return h;
}

/**
 * Owns at most one live `shiori serve --stdio` child. Every method fails
 * closed: an unusable binary, an unsupported handshake, an oversized or
 * too-deep frame, or a lost transport rejects the affected requests with a
 * structured, actionable error and never retries a mutation.
 */
export class CoreClient {
  #session?: Session;
  #starting?: Promise<Session>;
  #disposed = false;
  #counter = 0;
  #stderrTail = "";
  readonly #nonce = randomBytes(4).toString("hex");

  constructor(private readonly options: CoreClientOptions) {}

  /** The live child's PID (tests and diagnostics). */
  get pid(): number | undefined {
    return this.#session && !this.#session.dead ? this.#session.child.pid : undefined;
  }

  get handshake(): Handshake | undefined {
    return this.#session && !this.#session.dead ? this.#session.handshake : undefined;
  }

  stderrTail(): string {
    return redact(this.#stderrTail);
  }

  async ensure(): Promise<Session> {
    if (this.#disposed) throw new ShioriError("cancelled", "The Shiori adapter was unloaded; no request was sent.");
    if (this.#session && !this.#session.dead) return this.#session;
    if (!this.#starting) {
      this.#starting = this.#start().finally(() => {
        this.#starting = undefined;
      });
    }
    return this.#starting;
  }

  async #start(): Promise<Session> {
    const bin = this.options.bin;
    if (!bin) {
      throw new ShioriError("unsupported_capability", 'The Shiori core binary is not configured: set the plugin option "bin" or the SHIORI_BIN environment variable to the absolute path of a shiori build. The adapter never searches PATH.');
    }
    if (!isAbsolute(bin)) {
      throw new ShioriError("unsupported_capability", `The Shiori core binary must be an absolute path (got a relative path); the adapter never searches PATH.`);
    }
    try {
      await access(bin, constants.X_OK);
    } catch {
      throw new ShioriError("unsupported_capability", `The Shiori core binary is missing or not executable at ${bin}.`);
    }
    const spawn = this.options.spawn ?? nodeSpawn;
    const child = spawn(bin, [...(this.options.args ?? ["serve", "--stdio"])], {
      stdio: ["pipe", "pipe", "pipe"],
      env: this.options.env ?? process.env,
    });
    const pending = new Map<string, Pending>();
    let resolveExit!: () => void;
    const exited = new Promise<void>((resolve) => { resolveExit = resolve; });
    const session: Session = { child, handshake: undefined as unknown as Handshake, pending, exited, dead: false };
    const lose = (message: string) => {
      if (session.dead) return;
      session.dead = true;
      for (const [id, p] of pending) {
        pending.delete(id);
        p.reject(p.operation === "shiori.commit"
          ? new ShioriError("outcome_uncertain", `${message} while committing; the workplan outcome is uncertain. Run workplan_doctor and, if a transaction journal is reported, recover it explicitly with workplan_update {recovery}. Nothing is replayed.`)
          : new ShioriError("cancelled", `${message}; the request was not completed and nothing is replayed. The next call starts a new Shiori core.`));
      }
    };
    child.once("exit", (code, signal) => {
      lose(`The Shiori core exited (${signal ?? `code ${code}`})`);
      resolveExit();
    });
    child.once("error", (error) => {
      lose(`The Shiori core could not run (${redact(error.message)})`);
      resolveExit();
    });
    child.stdin?.on("error", () => lose("The Shiori core transport closed"));
    child.stderr?.setEncoding("utf8");
    child.stderr?.on("data", (chunk: string) => {
      this.#stderrTail = (this.#stderrTail + chunk).slice(-STDERR_TAIL_BYTES);
      for (const line of chunk.split("\n")) if (line.trim()) this.options.onDiagnostic?.(redact(line));
    });
    let buffer: Buffer = Buffer.alloc(0);
    const maxLine = () => (session.handshake?.limits?.maxResponseBytes ?? DEFAULT_MAX_RESPONSE_BYTES) + 1024;
    child.stdout?.on("data", (chunk: Buffer) => {
      buffer = buffer.length ? Buffer.concat([buffer, chunk]) : chunk;
      let newline: number;
      while ((newline = buffer.indexOf(0x0a)) >= 0) {
        const line = buffer.subarray(0, newline).toString("utf8");
        buffer = buffer.subarray(newline + 1);
        this.#dispatch(session, line, lose);
      }
      if (buffer.length > maxLine()) {
        lose("The Shiori core sent a frame above the response limit");
        this.#terminate(session);
      }
    });
    child.stdout?.on("end", () => lose("The Shiori core closed its output"));

    try {
      const response = await this.#send(session, "shiori.handshake", {
        client: this.options.clientName ?? "shiori-opencode",
        protocolVersions: [PROTOCOL_VERSION],
      }, undefined, undefined);
      (session as { handshake: Handshake }).handshake = validateHandshake(response.result);
    } catch (error) {
      this.#terminate(session);
      throw error;
    }
    this.#session = session;
    return session;
  }

  #dispatch(session: Session, line: string, lose: (message: string) => void): void {
    let frame: any;
    try {
      frame = JSON.parse(line);
    } catch {
      lose("The Shiori core sent a malformed frame");
      this.#terminate(session);
      return;
    }
    if (frame?.type !== "response" || frame.protocolVersion !== PROTOCOL_VERSION || typeof frame.requestId !== "string") {
      lose("The Shiori core sent an unexpected frame");
      this.#terminate(session);
      return;
    }
    if (frame.requestId === "_") {
      lose(`The Shiori core rejected the connection (${frame.error?.class ?? "invalid_frame"}: ${frame.error?.message ?? "no detail"})`);
      return;
    }
    const pending = session.pending.get(frame.requestId);
    if (!pending) return; // a request already answered locally (cancelled read)
    session.pending.delete(frame.requestId);
    if (frame.ok === true) {
      pending.resolve({ requestId: frame.requestId, result: frame.result, prepared: frame.prepared, hashes: frame.hashes });
    } else {
      const e = frame.error ?? {};
      pending.reject(new ShioriError(typeof e.class === "string" ? e.class : "internal", typeof e.message === "string" ? e.message : "The Shiori core failed without a message", {
        issues: e.issues, currentStateHash: e.currentStateHash, retrieval: e.retrieval, recoveryJournal: e.recoveryJournal,
      }));
    }
  }

  #nextId(): string {
    this.#counter += 1;
    return `a${this.#nonce}.${this.#counter}`;
  }

  #send(session: Session, operation: string, input: unknown, hostContext: HostContext | undefined, signal: AbortSignal | undefined, requestId = this.#nextId()): Promise<CoreResponse> {
    if (session.dead) return Promise.reject(new ShioriError("cancelled", "The Shiori core is not running; no request was sent."));
    const frame: Record<string, unknown> = { type: "request", protocolVersion: PROTOCOL_VERSION, requestId, operation, input };
    if (hostContext) frame.hostContext = hostContext;
    if (depth(frame) > MAX_NESTING) {
      return Promise.reject(new ShioriError("invalid_input", `Workplan input is nested deeper than the ${MAX_NESTING}-level protocol limit; no request was sent.`));
    }
    const text = JSON.stringify(frame);
    const limit = session.handshake?.limits?.maxFrameBytes ?? DEFAULT_MAX_FRAME_BYTES;
    if (Buffer.byteLength(text, "utf8") > limit) {
      return Promise.reject(new ShioriError("invalid_input", `Workplan input exceeds the ${limit}-byte protocol frame limit; no request was sent.`));
    }
    return new Promise<CoreResponse>((resolve, reject) => {
      const commit = operation === "shiori.commit";
      let onAbort: (() => void) | undefined;
      const cleanup = () => {
        if (onAbort) signal?.removeEventListener("abort", onAbort);
      };
      session.pending.set(requestId, {
        operation,
        resolve: (r) => { cleanup(); resolve(r); },
        reject: (e) => { cleanup(); reject(e); },
      });
      if (signal) {
        onAbort = () => {
          this.cancel(requestId);
          // Reads and prepares settle now; a commit waits for the core's
          // truthful outcome (cancelled before the journal, or uncertain).
          if (!commit) {
            const p = session.pending.get(requestId);
            session.pending.delete(requestId);
            p?.reject(new ShioriError("cancelled", "The workplan request was cancelled; nothing was changed."));
          }
        };
        if (signal.aborted) {
          session.pending.delete(requestId);
          reject(new ShioriError("cancelled", "The workplan request was cancelled before it was sent; nothing was changed."));
          return;
        }
        signal.addEventListener("abort", onAbort, { once: true });
      }
      session.child.stdin?.write(text + "\n", (error) => {
        if (error) {
          const p = session.pending.get(requestId);
          session.pending.delete(requestId);
          p?.reject(new ShioriError(commit ? "outcome_uncertain" : "cancelled", "The Shiori core transport failed while sending the request; nothing is replayed."));
        }
      });
    });
  }

  /** Sends one request on the live child (starting it lazily). */
  async request(operation: string, input: unknown, hostContext: HostContext, signal?: AbortSignal, requestId?: string): Promise<CoreResponse> {
    if (signal?.aborted) throw new ShioriError("cancelled", "The workplan request was cancelled before it was sent; nothing was changed.");
    const session = await this.ensure();
    return this.#send(session, operation, input, hostContext, signal, requestId);
  }

  newRequestId(): string {
    return this.#nextId();
  }

  /** Best-effort cancel frame (no response). */
  cancel(requestId: string): void {
    const session = this.#session;
    if (!session || session.dead) return;
    try {
      session.child.stdin?.write(JSON.stringify({ type: "cancel", protocolVersion: PROTOCOL_VERSION, requestId }) + "\n");
    } catch {
      // Transport loss already fails every pending request closed.
    }
  }

  #terminate(session: Session): void {
    session.dead = true;
    try {
      session.child.stdin?.end();
    } catch {}
    if (session.child.exitCode === null && session.child.signalCode === null) {
      try {
        session.child.kill("SIGTERM");
      } catch {}
    }
  }

  /**
   * Host unload (contracts §5.4): cancel in-flight requests, close stdin,
   * wait, SIGTERM the owned PID only, wait, SIGKILL. Durable journals stay.
   */
  async dispose(): Promise<void> {
    if (this.#disposed) return;
    this.#disposed = true;
    const session = this.#session ?? (await this.#starting?.catch(() => undefined));
    if (!session) return;
    for (const id of session.pending.keys()) this.cancel(id);
    try {
      session.child.stdin?.end();
    } catch {}
    const wait = (ms: number) => Promise.race([session.exited.then(() => true), new Promise<boolean>((r) => setTimeout(() => r(false), ms).unref?.())]);
    const running = () => session.child.exitCode === null && session.child.signalCode === null;
    if (running() && !(await wait(this.options.closeGraceMs ?? 2_000))) {
      if (running()) session.child.kill("SIGTERM");
      if (!(await wait(this.options.termGraceMs ?? 2_000)) && running()) session.child.kill("SIGKILL");
    }
    await wait(this.options.termGraceMs ?? 2_000);
  }
}
