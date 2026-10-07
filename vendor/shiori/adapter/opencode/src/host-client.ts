// Minimal public OpenCode client used by the permission bridge: service
// discovery (read-only; never Service.ensure), server info, the public
// event stream, session lookup, permission creation and the bridge's own
// RPC proof call. It speaks the same HTTP routes as @opencode/client 2.0.20
// so the adapter stays dependency-free (node: built-ins and fetch only).
import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";

export interface Endpoint {
  readonly url: string;
  readonly auth?: { readonly type: "basic"; readonly username: string; readonly password: string };
}

export interface HostEvent {
  readonly type: string;
  readonly data?: any;
  readonly location?: { readonly directory?: string };
}

export interface RequestOptions {
  readonly signal?: AbortSignal;
}

export interface PermissionCreateInput {
  readonly sessionID: string;
  readonly action: string;
  readonly resources: readonly string[];
  readonly agent?: string;
  readonly source?: { readonly type: "tool"; readonly messageID: string; readonly id: string };
  readonly metadata?: Record<string, unknown>;
}

export interface HostClient {
  readonly server: { info(options?: RequestOptions): Promise<{ version?: unknown; pid?: unknown }> };
  readonly event: { subscribe(options?: RequestOptions): AsyncIterable<HostEvent> };
  readonly session: { get(input: { sessionID: string }, options?: RequestOptions): Promise<any> };
  readonly permission: { create(input: PermissionCreateInput, options?: RequestOptions): Promise<{ id: string; effect: string }> };
  readonly rpc: {
    prove(input: { challenge: string }, options: RequestOptions & { location: { directory: string } }): Promise<unknown>;
  };
}

export class HostClientError extends Error {
  constructor(readonly reason: string, message?: string) {
    super(message ? `${reason}: ${message}` : reason);
    this.name = "HostClientError";
  }
}

const MAX_SSE_EVENT_BYTES = 16 * 1024 * 1024;

/** Registration file of the local service (XDG state directory). */
export function serviceFile(env: NodeJS.ProcessEnv = process.env): string {
  return join(env.XDG_STATE_HOME ?? join(homedir(), ".local", "state"), "opencode", "service.json");
}

export function authHeaders(endpoint: Endpoint): Record<string, string> {
  if (!endpoint.auth) return {};
  return { authorization: "Basic " + Buffer.from(`${endpoint.auth.username}:${endpoint.auth.password}`).toString("base64") };
}

/**
 * Discovers a healthy, registered local service without starting one. The
 * result is only a candidate: the bridge still proves that it reached this
 * plugin instance before creating any permission request.
 */
export async function discoverService(options: { file?: string; fetch?: typeof fetch; timeoutMs?: number } = {}): Promise<Endpoint | undefined> {
  const fetchImpl = options.fetch ?? fetch;
  let info: { url?: unknown; pid?: unknown; version?: unknown; password?: unknown };
  try {
    info = JSON.parse(await readFile(options.file ?? serviceFile(), "utf8"));
  } catch {
    return undefined;
  }
  if (typeof info?.url !== "string" || typeof info.pid !== "number") return undefined;
  const endpoint: Endpoint = typeof info.password === "string"
    ? { url: info.url, auth: { type: "basic", username: "opencode", password: info.password } }
    : { url: info.url };
  try {
    const response = await fetchImpl(new URL("/api/info", info.url), {
      headers: authHeaders(endpoint),
      signal: AbortSignal.timeout(options.timeoutMs ?? 2_000),
    });
    if (!response.ok) return undefined;
    const body = await response.json() as { version?: unknown; pid?: unknown };
    if (typeof body?.version !== "string" || body.pid !== info.pid) return undefined;
    if (typeof info.version === "string" && info.version !== body.version) return undefined;
    return endpoint;
  } catch {
    return undefined;
  }
}

function isJson(response: Response): boolean {
  const type = response.headers.get("content-type")?.split(";", 1)[0]?.trim().toLowerCase() ?? "";
  return type === "application/json" || type.includes("+json");
}

function locationQuery(url: URL, location?: { directory: string }): void {
  if (location) url.searchParams.append("location[directory]", location.directory);
}

export function makeHostClient(endpoint: Endpoint, fetchImpl: typeof fetch = fetch): HostClient {
  const base = new URL(endpoint.url);
  if (!base.pathname.endsWith("/")) base.pathname += "/";
  const url = (path: string) => new URL(path.replace(/^\//, ""), base);
  const headers = (json: boolean) => ({ ...authHeaders(endpoint), ...(json ? { "content-type": "application/json" } : {}) });
  const request = async (method: string, target: URL, body: unknown, success: number, options?: RequestOptions): Promise<any> => {
    let response: Response;
    try {
      response = await fetchImpl(target, {
        method,
        headers: headers(body !== undefined),
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: options?.signal,
      });
    } catch (cause) {
      throw new HostClientError("Transport", cause instanceof Error ? cause.message : undefined);
    }
    if (response.status !== success) {
      try {
        await response.body?.cancel();
      } catch {}
      throw new HostClientError("UnexpectedStatus", String(response.status));
    }
    if (!isJson(response)) {
      try {
        await response.body?.cancel();
      } catch {}
      throw new HostClientError("UnsupportedContentType", response.headers.get("content-type") ?? "none");
    }
    return await response.json();
  };
  return {
    server: {
      info: (options) => request("GET", url("/api/info"), undefined, 200, options),
    },
    session: {
      get: async (input, options) => (await request("GET", url(`/api/session/${encodeURIComponent(input.sessionID)}`), undefined, 200, options)).data,
    },
    permission: {
      create: async (input, options) => (await request("POST", url(`/api/session/${encodeURIComponent(input.sessionID)}/permission`), {
        action: input.action,
        resources: input.resources,
        metadata: input.metadata,
        source: input.source,
        agent: input.agent,
      }, 200, options)).data,
    },
    rpc: {
      prove: async (input, options) => {
        const target = url(`/api/rpc/${encodeURIComponent(BRIDGE_RPC_ID)}/prove`);
        locationQuery(target, options.location);
        return (await request("POST", target, { input }, 200, options)).output;
      },
    },
    event: {
      subscribe: (options) => sse(fetchImpl, url("/api/event"), headers(false), options?.signal),
    },
  };
}

/** The bridge's RPC namespace (distinct from the reference plugin's). */
export const BRIDGE_RPC_ID = "shiori-workplan-permission-bridge";

async function* sse(fetchImpl: typeof fetch, target: URL, headers: Record<string, string>, signal?: AbortSignal): AsyncGenerator<HostEvent> {
  let response: Response;
  try {
    response = await fetchImpl(target, { headers: { ...headers, accept: "text/event-stream" }, signal });
  } catch (cause) {
    throw new HostClientError("Transport", cause instanceof Error ? cause.message : undefined);
  }
  if (response.status !== 200 || response.body === null) throw new HostClientError("UnexpectedStatus", String(response.status));
  if ((response.headers.get("content-type") ?? "").split(";", 1)[0]?.trim().toLowerCase() !== "text/event-stream") {
    try {
      await response.body.cancel();
    } catch {}
    throw new HostClientError("UnsupportedContentType");
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      let next: Awaited<ReturnType<typeof reader.read>>;
      try {
        next = await reader.read();
      } catch (cause) {
        throw new HostClientError("Transport", cause instanceof Error ? cause.message : undefined);
      }
      buffer += decoder.decode(next.value, { stream: !next.done });
      if (buffer.length > MAX_SSE_EVENT_BYTES) throw new HostClientError("SseEventTooLarge");
      buffer = buffer.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
      if (next.done && buffer !== "") buffer += "\n\n";
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        const block = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        const data = block.split("\n").flatMap((line) => line.startsWith("data:") ? [line.slice(5).trimStart()] : []).join("\n");
        if (data !== "") {
          try {
            yield JSON.parse(data) as HostEvent;
          } catch {
            throw new HostClientError("MalformedResponse");
          }
        }
        boundary = buffer.indexOf("\n\n");
      }
      if (next.done) return;
    }
  } finally {
    try {
      await reader.cancel();
    } catch {}
    try {
      reader.releaseLock();
    } catch {}
  }
}
