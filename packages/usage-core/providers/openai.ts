import { formatRelativeTime, type UsageData } from "../format.ts";
import { requestJson, type ProviderRequestOptions, UsageRequestError } from "./request.ts";

export const CODEX_USAGE_ENDPOINT = "https://chatgpt.com/backend-api/wham/usage";

interface RateLimitWindow {
  used_percent?: unknown;
  limit_window_seconds?: unknown;
  reset_after_seconds?: unknown;
  reset_at?: unknown;
}

interface CodexUsageResponse {
  plan_type?: unknown;
  rate_limit?: unknown;
  credits?: unknown;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  return value as Record<string, unknown>;
}

function finiteNumber(value: unknown): number | undefined {
  const number = typeof value === "number" ? value : typeof value === "string" ? Number(value) : NaN;
  return Number.isFinite(number) ? number : undefined;
}

export function getWindowLabel(key: string, window: RateLimitWindow): string {
  const seconds = finiteNumber(window.limit_window_seconds);
  if (seconds !== undefined && seconds > 0) {
    const hours = seconds / 3600;
    if (hours <= 24) return `${Math.round(hours)}h`;
    const days = hours / 24;
    if (days === 7) return "Weekly";
    return `${Math.round(days)}d`;
  }
  if (key === "primary_window") return "5h";
  if (key === "secondary_window") return "Weekly";
  return key.replace(/_/g, " ").replace(/window/i, "").trim() || "Usage";
}

function resetTime(window: RateLimitWindow, now: Date): string | undefined {
  const after = finiteNumber(window.reset_after_seconds);
  if (after !== undefined) return formatRelativeTime(new Date(now.getTime() + after * 1000), { now });

  const rawTimestamp = finiteNumber(window.reset_at);
  if (rawTimestamp === undefined) return undefined;
  const timestamp = rawTimestamp > 2_000_000_000_000 ? rawTimestamp : rawTimestamp * 1000;
  const date = new Date(timestamp);
  return Number.isFinite(date.getTime()) ? formatRelativeTime(date, { now }) : undefined;
}

function planType(value: unknown): string | undefined {
  if (typeof value !== "string" || value.length === 0) return undefined;
  return value.charAt(0).toUpperCase() + value.slice(1).toLowerCase();
}

/** Parse a ChatGPT/Codex usage response without network or filesystem access. */
export function parseOpenAIUsage(value: unknown, now = new Date()): UsageData {
  const data = asRecord(value) as CodexUsageResponse | undefined;
  const rateLimit = asRecord(data?.rate_limit);
  if (!data || !rateLimit) return { provider: "OpenAI/Codex", windows: [], error: "Invalid usage response" };

  const entries = Object.entries(rateLimit)
    .map(([key, raw]) => [key, asRecord(raw) as RateLimitWindow | undefined] as const)
    .filter((entry): entry is readonly [string, RateLimitWindow] => {
      const used = finiteNumber(entry[1]?.used_percent);
      return entry[1] !== undefined && used !== undefined;
    })
    .sort((a, b) => (finiteNumber(a[1].limit_window_seconds) ?? Infinity) - (finiteNumber(b[1].limit_window_seconds) ?? Infinity));

  const primary = entries.find(([key]) => key === "primary_window") ?? entries[0];
  const secondary = entries.find(([key]) => key === "secondary_window" && key !== primary?.[0]) ?? entries.find(([key]) => key !== primary?.[0]);
  const windows: UsageData["windows"] = [];

  for (const entry of [primary, secondary]) {
    if (!entry) continue;
    const used = finiteNumber(entry[1].used_percent);
    if (used === undefined) continue;
    windows.push({
      label: getWindowLabel(entry[0], entry[1]),
      usedPercent: Math.max(0, Math.min(100, used)),
      resetTime: resetTime(entry[1], now),
    });
  }

  const credits = asRecord(data.credits);
  const extra: Record<string, string> = {};
  if (credits?.unlimited === true) {
    extra.Credits = "Unlimited";
  } else {
    const balance = finiteNumber(credits?.balance);
    if (balance !== undefined) extra.Credits = `$${balance.toFixed(2)}`;
  }

  return {
    provider: "OpenAI/Codex",
    planType: planType(data.plan_type),
    windows,
    ...(Object.keys(extra).length > 0 ? { extra } : {}),
  };
}

export async function fetchOpenAIUsage(
  accessToken: string,
  accountId?: string,
  options: ProviderRequestOptions = {},
): Promise<UsageData> {
  try {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${accessToken}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    };
    if (accountId) headers["ChatGPT-Account-Id"] = accountId;

    const { response, data } = await requestJson(CODEX_USAGE_ENDPOINT, { method: "GET", headers }, options);
    if (!response.ok) {
      return {
        provider: "OpenAI/Codex",
        windows: [],
        error:
          response.status === 401
            ? "Token expired or invalid"
            : response.status === 403
              ? "Access denied (account ID may be required)"
              : `HTTP ${response.status}`,
      };
    }
    return parseOpenAIUsage(data, options.now?.() ?? new Date());
  } catch (error) {
    return {
      provider: "OpenAI/Codex",
      windows: [],
      error: error instanceof UsageRequestError ? error.message : "Request failed",
    };
  }
}
