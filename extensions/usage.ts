import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import options from "../config/plugin-options.json";
import { fetchGatewayQuota, formatQuotaRows, newLowQuotaWarnings, quotaReminderThresholds, readGatewayAccounts } from "../lib/usage";
import { resolve } from "node:path";

const REMINDER_CACHE_MS = 5 * 60_000;

export default function usageExtension(pi: ExtensionAPI): void {
  let cache: { expiresAt: number; rows: Awaited<ReturnType<typeof fetchGatewayQuota>> } | undefined;
  const thresholds = quotaReminderThresholds(options["usage-tracker"]);
  const deliveredWarnings = new Set<string>();

  async function quotaRows(refresh = false) {
    if (!refresh && cache && Date.now() < cache.expiresAt) return cache.rows;
    const accounts = await readGatewayAccounts(resolve(import.meta.dir, "../.runtime/proxy/auth"));
    const rows = await fetchGatewayQuota(accounts);
    cache = { expiresAt: Date.now() + REMINDER_CACHE_MS, rows };
    return rows;
  }

  pi.registerCommand("tsuna-usage", {
    description: "Show read-only CLIProxyAPI OAuth quota windows.",
    handler: async (_args, ctx) => {
      ctx.ui.notify(formatQuotaRows(await quotaRows(true)), "info");
    },
  });

  pi.on("message_end", async (event, ctx) => {
    if (ctx.agent.kind !== "main" || event.message.role !== "assistant") return;
    try {
      const warnings = newLowQuotaWarnings(await quotaRows(), thresholds, deliveredWarnings);
      if (warnings.length) ctx.ui.notify(`Quota reminder: ${warnings.map((warning) => `${warning.account} ${warning.label} has ${warning.remainingPercent}% or less remaining`).join(", ")}.`, "warning");
    } catch {
      // Quota availability must never block a completed message.
    }
  });
}
