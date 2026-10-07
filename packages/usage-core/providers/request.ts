export interface ProviderRequestOptions {
  fetch?: typeof fetch;
  signal?: AbortSignal;
  timeoutMs?: number;
  now?: () => Date;
}

export class UsageRequestError extends Error {
  constructor(public readonly reason: "timed out" | "cancelled" | "failed") {
    super(reason === "timed out" ? "Request timed out" : reason === "cancelled" ? "Request cancelled" : "Request failed");
    this.name = "UsageRequestError";
  }
}

export async function requestJson(
  url: string,
  init: RequestInit,
  options: ProviderRequestOptions = {},
): Promise<{ response: Response; data: unknown }> {
  const fetcher = options.fetch ?? globalThis.fetch;
  if (typeof fetcher !== "function") throw new UsageRequestError("failed");

  const controller = new AbortController();
  let timedOut = false;
  const timeoutMs = Math.max(1, Math.floor(options.timeoutMs ?? 10_000));
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  const abortParent = () => controller.abort();

  if (options.signal?.aborted) {
    clearTimeout(timer);
    throw new UsageRequestError("cancelled");
  }
  options.signal?.addEventListener("abort", abortParent, { once: true });

  try {
    const response = await fetcher(url, { ...init, signal: controller.signal });
    let data: unknown;
    if (response.ok) {
      try {
        data = await response.json();
      } catch {
        throw new UsageRequestError("failed");
      }
    }
    return { response, data };
  } catch (error) {
    if (error instanceof UsageRequestError) throw error;
    if (timedOut) throw new UsageRequestError("timed out");
    if (options.signal?.aborted) throw new UsageRequestError("cancelled");
    throw new UsageRequestError("failed");
  } finally {
    clearTimeout(timer);
    options.signal?.removeEventListener("abort", abortParent);
  }
}
