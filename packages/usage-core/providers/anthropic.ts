import { formatRelativeTime, type UsageData } from "../format.ts";
import { requestJson, type ProviderRequestOptions, UsageRequestError } from "./request.ts";

export const ANTHROPIC_USAGE_ENDPOINT = "https://api.anthropic.com/api/oauth/usage";

/** Subscription windows only: extra-usage spending is not a model quota. */
export function parseAnthropicUsage(value: unknown, now = new Date()): UsageData {
  const result: UsageData = { provider: "Anthropic", windows: [] };
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { ...result, error: "Invalid usage response" };
  }
  const labels: Record<string, string> = {
    five_hour: "5h",
    seven_day: "Weekly",
    seven_day_opus: "Weekly Opus",
    seven_day_sonnet: "Weekly Sonnet",
  };
  for (const [key, label] of Object.entries(labels)) {
    const window = (value as Record<string, unknown>)[key];
    if (!window || typeof window !== "object") continue;
    const { utilization, resets_at } = window as Record<string, unknown>;
    if (typeof utilization !== "number" || !Number.isFinite(utilization) || utilization < 0 || utilization > 100) continue;
    const reset = typeof resets_at === "string" ? new Date(resets_at) : undefined;
    result.windows.push({
      label,
      usedPercent: utilization,
      ...(reset && Number.isFinite(reset.getTime()) ? { resetTime: formatRelativeTime(reset, { now }) } : {}),
    });
  }
  return result.windows.length ? result : { ...result, error: "Invalid usage response" };
}

export async function fetchAnthropicUsage(accessToken: string, options: ProviderRequestOptions = {}): Promise<UsageData> {
  try {
    const { response, data } = await requestJson(ANTHROPIC_USAGE_ENDPOINT, {
      method: "GET",
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json",
        "anthropic-beta": "oauth-2025-04-20",
      },
    }, options);
    if (!response.ok) return { provider: "Anthropic", windows: [], error: `HTTP ${response.status}` };
    return parseAnthropicUsage(data, options.now?.() ?? new Date());
  } catch (error) {
    return {
      provider: "Anthropic", windows: [],
      error: error instanceof UsageRequestError ? error.message : "Request failed",
    };
  }
}
