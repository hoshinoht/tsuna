export interface UsageWindow {
  label: string;
  usedPercent: number;
  resetTime?: string;
}

export interface UsageData {
  provider: string;
  planType?: string;
  windows: UsageWindow[];
  extra?: Record<string, string>;
  error?: string;
}

const BAR_WIDTH = 16;
const CONTENT_WIDTH = 58;

function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, value));
}

export function progressBar(percent: number, width = BAR_WIDTH): string {
  const safeWidth = Math.max(1, Math.floor(width));
  const clamped = clampPercent(percent);
  const filled = Math.round((clamped / 100) * safeWidth);
  return `${"█".repeat(filled)}${"░".repeat(safeWidth - filled)} ${Math.round(clamped)
    .toString()
    .padStart(3)}%`;
}

export function getUsageIndicator(percent: number): string {
  if (percent >= 90) return "[critical]";
  if (percent >= 75) return "[high]";
  if (percent >= 50) return "[moderate]";
  return "";
}

function padLine(value: string): string {
  if (value.length >= CONTENT_WIDTH) return value.slice(0, CONTENT_WIDTH);
  return `${value}${" ".repeat(CONTENT_WIDTH - value.length)}`;
}

function line(value: string): string {
  return `│ ${padLine(value)} │`;
}

function providerLines(data: UsageData): string[] {
  const heading = data.planType ? `${data.provider} (${data.planType})` : data.provider;
  const lines = [line(heading)];

  if (data.error) {
    lines.push(line(`Error: ${data.error}`));
    return lines;
  }

  for (const window of data.windows) {
    const indicator = getUsageIndicator(window.usedPercent);
    lines.push(line(`${window.label.padEnd(10)} ${progressBar(window.usedPercent)} ${indicator}`.trim()));
    if (window.resetTime) lines.push(line(`  ${window.label} resets: ${window.resetTime}`));
  }

  for (const [key, value] of Object.entries(data.extra ?? {})) {
    lines.push(line(`${key}: ${value}`));
  }

  if (data.windows.length === 0 && Object.keys(data.extra ?? {}).length === 0) {
    lines.push(line("No usage windows reported."));
  }

  return lines;
}

export function formatUsageTable(providers: readonly UsageData[]): string {
  const border = `├${"─".repeat(CONTENT_WIDTH + 2)}┤`;
  const top = `╭${"─".repeat(CONTENT_WIDTH + 2)}╮`;
  const bottom = `╰${"─".repeat(CONTENT_WIDTH + 2)}╯`;
  const lines = [top, line("AI Provider Usage"), border];

  providers.forEach((provider, index) => {
    lines.push(...providerLines(provider));
    if (index < providers.length - 1) lines.push(border);
  });

  lines.push(bottom);
  return lines.join("\n");
}

export function formatRelativeTime(
  date: Date,
  options: { includeDate?: boolean; now?: Date } = {},
): string {
  if (!Number.isFinite(date.getTime())) return "unknown";
  const now = options.now ?? new Date();
  const diffMs = date.getTime() - now.getTime();
  const includeDate = options.includeDate ?? true;
  const day = String(date.getDate()).padStart(2, "0");
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const year = String(date.getFullYear()).slice(-2);
  const hours24 = date.getHours();
  const hours12 = hours24 % 12 || 12;
  const minutes = String(date.getMinutes()).padStart(2, "0");
  const period = hours24 >= 12 ? "PM" : "AM";
  const timestamp = `${includeDate ? `${day}/${month}/${year} ` : ""}${hours12}:${minutes} ${period}`;

  if (diffMs < 0) return `expired (${timestamp})`;
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHour = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHour / 24);

  let relative: string;
  if (diffDay > 0) {
    const remainingHours = diffHour % 24;
    relative = remainingHours > 0 ? `${diffDay}d ${remainingHours}h` : `${diffDay}d`;
  } else if (diffHour > 0) {
    const remainingMinutes = diffMin % 60;
    relative = remainingMinutes > 0 ? `${diffHour}h ${remainingMinutes}m` : `${diffHour}h`;
  } else if (diffMin > 0) {
    relative = `${diffMin}m`;
  } else {
    relative = `${diffSec}s`;
  }

  return `${relative} (${timestamp})`;
}

export function formatError(message: string): string {
  return formatUsageTable([{ provider: "Usage", windows: [], error: message }]);
}

export function formatNoProviders(): string {
  return "No providers configured. Authenticate with Copilot or OpenAI/Codex first.";
}

export function formatUsageResult(result: {
  kind: "ok" | "empty" | "error";
  provider: string;
  providers?: readonly UsageData[];
  message?: string;
}): string {
  if (result.kind === "ok") return formatUsageTable(result.providers ?? []);
  return result.message ?? (result.kind === "empty" ? formatNoProviders() : "Usage request failed.");
}
