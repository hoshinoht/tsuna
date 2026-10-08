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

test("the multiline worktree cleanup prompt identifies recursive deletion, not ordinary Git inspection", () => {
  const inspection = "set -e\ngit update-ref refs/heads/feature 1111111 2222222\ngit reset -q\ngit status --short | head; git --no-pager log --oneline -1; git diff HEAD --stat | tail -1\ngit worktree remove .worktrees/inherit --force 2>/dev/null || true";
  const p = policies as PermissionPolicies;
  const cleanup = decidePermission(p, "orchestrator", asToolCall("bash", { command: `${inspection}; rm -rf /tmp/tmp.agent-owned` }), { approvalMode: "auto" });
  expect(cleanup.effect).toBe("ask");
  expect(cleanup.reason).toContain("Recursive deletion");
  expect(decidePermission(p, "orchestrator", asToolCall("bash", { command: "git reset -q; git status --short | head" }), { approvalMode: "auto" }).effect).toBe("allow");
});

test("auto mode permits verified file updates with process substitution and non-recursive temp cleanup", () => {
  const command = "set -e; grep -cE '^(<<<<<<<|\\|\\|\\|\\|\\|\\|\\||=======|>>>>>>>)' /tmp/cl.out2.md || true; diff <(grep -v 'new bullet' /tmp/cl.out2.md) /tmp/cl.user2.md && echo verified; cmp -s CHANGELOG.md /tmp/cl.user2.md && cp /tmp/cl.out2.md CHANGELOG.md && echo applied; rm -f /tmp/cl.*.md";
  const p = policies as PermissionPolicies;
  expect(decidePermission(p, "orchestrator", asToolCall("bash", { command }), { approvalMode: "auto" }).effect).toBe("allow");
  for (const command of ["echo checked; rm -rf /tmp/project", "echo checked; rm --recursive /tmp/project"]) {
    expect(decidePermission(p, "orchestrator", asToolCall("bash", { command }), { approvalMode: "auto" }).effect).toBe("ask");
  }
});
