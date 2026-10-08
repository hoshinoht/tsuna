import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import permissions from "../config/permissions.json";
import { asToolCall, decidePermission, type PermissionPolicies } from "../lib/permissions";
import { frontmatter } from "../scripts/import-opencode";

const policies = permissions as PermissionPolicies;

test("the orchestrator can request the project-local fidelity agent", () => {
  const { meta } = frontmatter(readFileSync(new URL("../agent/agents/orchestrator.md", import.meta.url), "utf8"));
  expect(meta.spawns).toContain("fidelity");
  expect(decidePermission(policies, "orchestrator", asToolCall("task", { agent: "fidelity" })).effect).toBe("allow");
});

test("fidelity can measure, inspect captures and fix components in either permission mode", () => {
  for (const approvalMode of ["source", "auto"] as const) {
    const decide = (tool: string, input: unknown) => decidePermission(policies, "fidelity", asToolCall(tool, input), {
      cwd: "/private/tmp/project", approvalMode,
    }).effect;
    for (const path of ["web/e2e/.captures/fidelity/pair/report.md", "web/e2e/.captures/fidelity/pair/composite.png"]) {
      expect(decide("read", { path })).toBe("allow");
    }
    expect(decide("edit", { path: "web/apps/public/src/page.tsx" })).toBe("allow");
    expect(decide("write", { path: "docs/research/board.html", content: "fixture" })).toBe("allow");
    for (const command of ["KANADE_REAL_ART=1 bun run fidelity --keep", "bun run fidelity", "bun run check", "bun run lint", "bun run test", "bun run leak", "bunx playwright test e2e/public-portal.spec.ts"]) {
      expect(decide("bash", { command })).toBe("allow");
    }
    expect(decide("mcp__context7_query_docs", {})).toBe("allow");
    expect(decide("mcp__lsp_tools_lsp_diagnostics", {})).toBe("allow");
    expect(decide("task", { agent: "code-writer" })).toBe("deny");
    expect(decide("mcp__workplan_update", {})).toBe("deny");
    expect(decide("browser", {})).toBe("deny");
    expect(decide("read", { path: ".env" })).toBe("ask");
    // Auto mode waives the existing generic external-directory ask for all implementers.
    expect(decide("read", { path: "/private/tmp/other-project/report.md" })).toBe(approvalMode === "auto" ? "allow" : "ask");
    for (const command of ["git push origin main", "git reset --hard HEAD", "git clean -fd", "rm -rf dist"]) {
      expect(decide("bash", { command })).toBe("ask");
    }
  }
});
