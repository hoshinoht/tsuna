import { expect, test } from "bun:test";
import policies from "../config/permissions.json";
import { asToolCall, decidePermission, type PermissionPolicies } from "../lib/permissions";

test("auto mode runs routine work without weakening explicit denies or guarded asks", () => {
  const p = policies as PermissionPolicies;
  const decide = (role: string, name: string, args: unknown) => decidePermission(p, role, asToolCall(name, args), { cwd: "/private/tmp/project", approvalMode: "auto" });
  expect(decide("orchestrator", "bash", { command: "python3 - <<'PY'\nprint(1 + 1)\nPY" }).effect).toBe("allow");
  expect(decide("orchestrator", "bash", { command: "rg TODO src | head -20" }).effect).toBe("allow");
  expect(decide("orchestrator", "read", { path: "/private/tmp/another-project/README.md" }).effect).toBe("allow");
  expect(decide("orchestrator", "bash", { command: "bun test && git push origin main" }).effect).toBe("ask");
  expect(decide("orchestrator", "bash", { command: "bun test; git reset --hard HEAD" }).effect).toBe("ask");
  expect(decide("orchestrator", "read", { path: "/private/tmp/project/.env" }).effect).toBe("ask");
  expect(decide("code-checker", "write", { path: "src/index.ts", content: "x" }).effect).toBe("deny");
  expect(decidePermission(p, "orchestrator", asToolCall("bash", { command: "rg TODO src | head -20" })).effect).toBe("ask");
});
