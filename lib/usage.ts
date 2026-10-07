import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { fetchAnthropicUsage } from "../packages/usage-core/providers/anthropic";
import { fetchOpenAIUsage } from "../packages/usage-core/providers/openai";
import type { UsageData, UsageWindow } from "../packages/usage-core/format";

export type QuotaProvider = "openai" | "anthropic";

export interface GatewayAccount {
  provider: QuotaProvider;
  accessToken: string;
  accountId?: string;
}

export interface QuotaRow {
  provider: "OpenAI/Codex" | "Anthropic";
  account: string;
  windows: UsageWindow[];
  error?: string;
}

export interface LowQuotaWarning {
  account: string;
  label: string;
  remainingPercent: number;
}

export interface AuthDirectoryEntry {
  name: string;
  isFile(): boolean;
}

export interface UsageDependencies {
  readDir?: (path: string) => Promise<readonly AuthDirectoryEntry[]>;
  readFile?: (path: string, encoding: "utf8") => Promise<string>;
  fetch?: typeof fetch;
  now?: () => Date;
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function string(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function accountFromFile(value: unknown): GatewayAccount | undefined {
  const entry = record(value);
  const type = string(entry?.type)?.toLowerCase();
  const accessToken = string(entry?.access_token);
  if (!accessToken) return undefined;
  if (type === "codex") {
    return { provider: "openai", accessToken, ...(string(entry?.account_id) ? { accountId: string(entry?.account_id) } : {}) };
  }
  if (type === "claude") return { provider: "anthropic", accessToken };
  return undefined;
}

/** Reads only CLIProxyAPI's documented OAuth token shape and discards all other fields. */
export async function readGatewayAccounts(authDirectory: string, dependencies: UsageDependencies = {}): Promise<GatewayAccount[]> {
  const list = dependencies.readDir ?? (async (path: string) => readdir(path, { withFileTypes: true }));
  const load = dependencies.readFile ?? ((path: string, encoding: "utf8") => readFile(path, encoding));
  let entries: readonly AuthDirectoryEntry[];
  try {
    entries = await list(authDirectory);
  } catch {
    return [];
  }

  const accounts: GatewayAccount[] = [];
  for (const entry of [...entries].sort((left, right) => left.name.localeCompare(right.name))) {
    if (!entry.isFile() || !entry.name.endsWith(".json")) continue;
    try {
      const parsed = JSON.parse(await load(join(authDirectory, entry.name), "utf8"));
      const account = accountFromFile(parsed);
      if (account) accounts.push(account);
    } catch {
      // An in-progress login or malformed credential is unavailable, never a quota of zero.
    }
  }
  return accounts;
}

function providerName(provider: QuotaProvider): QuotaRow["provider"] {
  return provider === "openai" ? "OpenAI/Codex" : "Anthropic";
}

function accountLabel(provider: QuotaProvider, index: number): string {
  return `${providerName(provider)} account ${index}`;
}

function rowFromData(data: UsageData, account: string): QuotaRow {
  return {
    provider: data.provider === "Anthropic" ? "Anthropic" : "OpenAI/Codex",
    account,
    windows: data.windows,
    ...(data.error ? { error: safeQuotaError(data.error) } : {}),
  };
}

function safeQuotaError(value: string): string {
  if (/^HTTP \d{3}$/.test(value)) return value;
  if (["Token expired or invalid", "Access denied (account ID may be required)", "Request timed out", "Request cancelled", "Request failed", "Invalid usage response"].includes(value)) return value;
  return "Quota request failed";
}

/** Fetches only the official source-plugin quota endpoints with a short timeout. */
export async function fetchGatewayQuota(accounts: readonly GatewayAccount[], dependencies: UsageDependencies = {}): Promise<QuotaRow[]> {
  const counters: Record<QuotaProvider, number> = { openai: 0, anthropic: 0 };
  const options = { fetch: dependencies.fetch, now: dependencies.now, timeoutMs: 3_000 };
  return Promise.all(accounts.map(async (account) => {
    const label = accountLabel(account.provider, ++counters[account.provider]);
    const data = account.provider === "openai"
      ? await fetchOpenAIUsage(account.accessToken, account.accountId, options)
      : await fetchAnthropicUsage(account.accessToken, options);
    return rowFromData(data, label);
  }));
}

export function lowQuotaRows(rows: readonly QuotaRow[], remainingPercent = 10): QuotaRow[] {
  const usedThreshold = 100 - remainingPercent;
  return rows.filter((row) => row.windows.some((window) => Number.isFinite(window.usedPercent) && window.usedPercent >= usedThreshold));
}

export function quotaReminderThresholds(value: unknown): Partial<Record<QuotaProvider, number>> {
  const source = record(value);
  const thresholds = record(source?.quotaReminderRemainingPercent);
  const result: Partial<Record<QuotaProvider, number>> = {};
  for (const provider of ["openai", "anthropic"] as const) {
    const threshold = thresholds?.[provider];
    if (typeof threshold === "number" && Number.isFinite(threshold) && threshold >= 0 && threshold <= 100) result[provider] = threshold;
  }
  return result;
}

export function newLowQuotaWarnings(
  rows: readonly QuotaRow[],
  thresholds: Partial<Record<QuotaProvider, number>>,
  delivered: Set<string>,
): LowQuotaWarning[] {
  const warnings: LowQuotaWarning[] = [];
  for (const row of rows) {
    const provider: QuotaProvider = row.provider === "Anthropic" ? "anthropic" : "openai";
    const remainingPercent = thresholds[provider];
    if (remainingPercent === undefined || row.error) continue;
    for (const window of row.windows) {
      if (!Number.isFinite(window.usedPercent) || window.usedPercent < 100 - remainingPercent) continue;
      const key = `${row.account}\u0000${window.label}\u0000${window.resetTime ?? ""}`;
      if (delivered.has(key)) continue;
      delivered.add(key);
      warnings.push({ account: row.account, label: window.label, remainingPercent });
    }
  }
  return warnings;
}

export function formatQuotaRows(rows: readonly QuotaRow[]): string {
  if (!rows.length) return "CLIProxyAPI quota status is unavailable: no OAuth accounts are logged in.";
  const lines = rows.flatMap((row) => {
    if (row.error) return [`${row.account}: quota status unavailable (${row.error}).`];
    if (!row.windows.length) return [`${row.account}: no quota windows reported.`];
    return row.windows.map((window) => `${row.account} — ${window.label}: ${Math.round(window.usedPercent)}% used${window.resetTime ? `; resets ${window.resetTime}` : ""}.`);
  });
  lines.push("Quota status and runtime model capability are independent; an unavailable quota response does not establish model availability.");
  return lines.join("\n");
}
