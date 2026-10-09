import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { loadExtensions } from "@oh-my-pi/pi-coding-agent";
import { fetchGatewayQuota, formatQuotaRows, lowQuotaRows, newLowQuotaWarnings, quotaReminderThresholds, readGatewayAccounts } from "../lib/usage";

let cleanup: string[] = [];
afterEach(() => {
  for (const directory of cleanup) rmSync(directory, { recursive: true, force: true });
  cleanup = [];
});

function authFile(directory: string, name: string, value: unknown): void {
  writeFileSync(join(directory, name), JSON.stringify(value));
}

describe("CLIProxyAPI usage", () => {
  test("reads only supported local OAuth accounts and never retains refresh tokens", async () => {
    const directory = mkdtempSync(join(tmpdir(), "tsuna-usage-"));
    cleanup.push(directory);
    authFile(directory, "codex.json", { type: "codex", access_token: "openai-access", refresh_token: "openai-refresh", account_id: "account-1" });
    authFile(directory, "claude.json", { type: "claude", access_token: "anthropic-access", refresh_token: "anthropic-refresh" });
    authFile(directory, "other.json", { type: "gemini", access_token: "ignored" });
    authFile(directory, "invalid.json", { type: "codex" });

    const accounts = await readGatewayAccounts(directory);
    expect(accounts).toEqual([
      { provider: "anthropic", accessToken: "anthropic-access" },
      { provider: "openai", accessToken: "openai-access", accountId: "account-1" },
    ]);
    expect(JSON.stringify(accounts)).not.toContain("refresh");
  });

  test("uses only source-plugin provider endpoints and returns generic multi-account windows", async () => {
    const requests: Array<{ url: string; authorization: string | null }> = [];
    const mockFetch = (async (input: URL | RequestInfo, init?: RequestInit) => {
      const url = String(input);
      requests.push({ url, authorization: new Headers(init?.headers).get("Authorization") });
      if (url.includes("chatgpt.com")) {
        return new Response(JSON.stringify({ rate_limit: { primary_window: { used_percent: 92, limit_window_seconds: 18_000, reset_after_seconds: 60 } } }), { status: 200 });
      }
      return new Response(JSON.stringify({ five_hour: { utilization: 25, resets_at: "2030-01-01T00:00:00Z" } }), { status: 200 });
    }) as typeof fetch;
    const rows = await fetchGatewayQuota([
      { provider: "openai", accessToken: "openai-secret", accountId: "account-id" },
      { provider: "anthropic", accessToken: "anthropic-secret" },
    ], { fetch: mockFetch, now: () => new Date("2029-12-31T00:00:00Z") });

    expect(requests.map((request) => request.url)).toEqual([
      "https://chatgpt.com/backend-api/wham/usage",
      "https://api.anthropic.com/api/oauth/usage",
    ]);
    expect(rows).toMatchObject([
      { provider: "OpenAI/Codex", account: "OpenAI/Codex account 1", windows: [{ label: "5h", usedPercent: 92 }] },
      { provider: "Anthropic", account: "Anthropic account 1", windows: [{ label: "5h", usedPercent: 25 }] },
    ]);
    const rendered = formatQuotaRows(rows);
    expect(rendered).not.toContain("secret");
    expect(rendered).not.toContain("account-id");
    expect(lowQuotaRows(rows)).toHaveLength(1);
  });

  test("uses imported thresholds and only warns once per account window reset", () => {
    const thresholds = quotaReminderThresholds({ quotaReminderRemainingPercent: { openai: 10, anthropic: 5 } });
    expect(thresholds).toEqual({ openai: 10, anthropic: 5 });
    expect(quotaReminderThresholds({ quotaReminderRemainingPercent: { openai: -1, anthropic: "5" } })).toEqual({});
    const delivered = new Set<string>();
    const row = { provider: "OpenAI/Codex" as const, account: "OpenAI/Codex account 1", windows: [{ label: "5h", usedPercent: 95, resetTime: "in 1h" }] };
    expect(newLowQuotaWarnings([row], thresholds, delivered)).toEqual([{ account: row.account, label: "5h", remainingPercent: 10 }]);
    expect(newLowQuotaWarnings([row], thresholds, delivered)).toEqual([]);
    expect(newLowQuotaWarnings([{ ...row, windows: [{ ...row.windows[0]!, resetTime: "in 2h" }] }], thresholds, delivered)).toHaveLength(1);
  });

  test("does not surface thrown request text", async () => {
    const rows = await fetchGatewayQuota([{ provider: "openai", accessToken: "never-print" }], {
      fetch: (async () => { throw new Error("never-print"); }) as unknown as typeof fetch,
    });
    expect(formatQuotaRows(rows)).not.toContain("never-print");
    expect(rows[0]).toMatchObject({ error: "Request failed" });
  });

  test("reports a missing login as unavailable rather than zero usage", async () => {
    const directory = join(mkdtempSync(join(tmpdir(), "tsuna-usage-")), "missing");
    cleanup.push(resolve(directory, ".."));
    expect(await readGatewayAccounts(directory)).toEqual([]);
    expect(formatQuotaRows([])).toContain("unavailable");
    expect(formatQuotaRows([])).not.toContain("0%");
  });

  test("loads the OMP slash command without replacing model providers", async () => {
    const workspace = mkdtempSync(join(tmpdir(), "tsuna-usage-extension-"));
    cleanup.push(workspace);
    const extension = resolve(import.meta.dir, "../extensions/usage.ts");
    const loaded = await loadExtensions([extension], workspace);
    expect(loaded.errors).toEqual([]);
    expect([...loaded.extensions[0]!.commands.keys()]).toEqual(["tsuna-usage"]);
  });
});
