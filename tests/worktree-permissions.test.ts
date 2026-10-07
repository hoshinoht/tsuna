import { expect, test } from "bun:test";
import { mkdtemp, mkdir, writeFile, rm, symlink } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { decidePermission, asToolCall, type PermissionPolicies } from "../lib/permissions";

test("repository files and registered sibling worktrees are internal even when cwd is nested", async () => {
  const root = await mkdtemp(join(tmpdir(), "hoshi-worktree-permissions-"));
  const repo = join(root, "repo");
  const tree = join(root, "feature");
  const other = join(root, "other");
  const gitDir = join(repo, ".git/worktrees/feature");
  const rules: PermissionPolicies = { worker: [
    { action: "*", resource: "*", effect: "deny" },
    { action: "read", resource: "*", effect: "allow" },
    { action: "edit", resource: "*", effect: "allow" },
    { action: "external_directory", resource: "*", effect: "ask" },
  ] };
  try {
    for (const dir of [join(repo, "src"), gitDir, tree, other]) await mkdir(dir, { recursive: true });
    await writeFile(join(tree, ".git"), `gitdir: ${gitDir}\n`);
    await writeFile(join(gitDir, "commondir"), "../..\n");
    await writeFile(join(gitDir, "gitdir"), `${join(tree, ".git")}\n`);
    await writeFile(join(repo, "README.md"), "repo");
    await writeFile(join(tree, "README.md"), "tree");
    await writeFile(join(other, "README.md"), "outside");
    const decide = (name: string, path: string) => decidePermission(rules, "worker", asToolCall(name, { path }), { cwd: join(repo, "src") });
    expect(decide("read", join(repo, "README.md")).effect).toBe("allow");
    expect(decide("read", join(tree, "README.md")).effect).toBe("allow");
    expect(decide("write", join(tree, "new.ts")).effect).toBe("allow");
    expect(decide("read", join(other, "README.md")).effect).toBe("ask");
    await symlink(other, join(tree, "outside"));
    expect(decide("read", join(tree, "outside/README.md")).effect).toBe("ask");
    await writeFile(join(other, ".git"), `gitdir: ${gitDir}\n`);
    expect(decide("read", join(other, "README.md")).effect).toBe("ask");
  } finally { await rm(root, { recursive: true, force: true }); }
});
