import { expect, test } from "bun:test";
import policies from "../config/permissions.json";
import { asToolCall, decidePermission, type PermissionPolicies } from "../lib/permissions";

test("goal mode can inspect and finish the activated objective while preserving other approvals", () => {
  const rules = policies as PermissionPolicies;
  for (const op of ["get", "complete"]) {
    expect(decidePermission(rules, "orchestrator", asToolCall("goal", { op })).effect).toBe("allow");
  }
  expect(decidePermission(rules, "orchestrator", asToolCall("goal", { op: "create", objective: "different objective" })).effect).toBe("ask");
  expect(decidePermission(rules, "orchestrator", asToolCall("bash", { command: "git push origin main" })).effect).toBe("ask");
});
