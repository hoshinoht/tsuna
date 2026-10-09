import { afterEach, expect, test } from "bun:test";
import { chmod, copyFile, mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { parse } from "yaml";
import { runShellCommand } from "@oh-my-pi/pi-coding-agent/config/resolve-config-value";
import { nativeBinary, rustCli } from "../tests/rust-cli-helper";

const repo = resolve(import.meta.dir, "..");
const cleanup: string[] = [];
afterEach(async () => { for (const path of cleanup.splice(0)) await rm(path, { recursive: true, force: true }); });

async function fixture() {
  const base = await realpath(await mkdtemp(join(tmpdir(), "tsuna-rust-")));
  cleanup.push(base);
  const root = join(base, "repo with ' quotes");
  await mkdir(root);
  return root;
}

test("Rust setup reads the real OMP catalog and keeps command-backed credentials", async () => {
  const root = await fixture();
  for (const directory of ["config", "scripts", "bin", ".runtime/proxy", "vendor/shiori", "mcps/gofetch-mcp/bin", "mcps/researcher-mcp/bin"]) {
    await mkdir(join(root, directory), { recursive: true });
  }
  for (const name of ["omp.yml", "roles.json", "mcp.json"]) await copyFile(join(repo, "config", name), join(root, "config", name));
  await symlink(join(repo, "agent"), join(root, "agent"));
  await symlink(join(repo, "extensions"), join(root, "extensions"));
  for (const name of ["omp-catalog.ts", "omp-smoke.ts"]) await symlink(join(repo, "scripts", name), join(root, "scripts", name));
  for (const file of ["bin/tsuna", "vendor/shiori/shiori", "mcps/gofetch-mcp/bin/gofetch", "mcps/researcher-mcp/bin/researcher-mcp"]) await writeFile(join(root, file), "fixture");
  await writeFile(join(root, ".runtime/proxy/client-key"), "fixture-key-only");

  const result = rustCli(root, ["setup"]);
  expect(result.stderr).toBe("");
  expect(result.code).toBe(0);
  const generated = await readFile(join(root, ".runtime/omp/agent/models.yml"), "utf8");
  expect(generated).not.toContain("fixture-key-only");
  const models = parse(generated);
  expect(await runShellCommand(models.providers["cliproxy-openai"].apiKey.slice(1), 10_000, root)).toBe("fixture-key-only");
  const config = parse(await readFile(join(root, ".runtime/omp/agent/config.yml"), "utf8"));
  expect(config.extensions).toHaveLength(7);
  expect(config.modelRoles.default).toBe(config.modelRoles.orchestrator);
  expect(config.setupVersion).toBeGreaterThan(0);
  expect(models.providers["cliproxy-openai"].models.length).toBeGreaterThan(0);
  // Exercise the complete Rust -> Bun -> OMP SDK route against the isolated profile.
  const child = Bun.spawn([nativeBinary, "--root", root, "smoke"], {
    env: { ...process.env, HOME: root },
    stdin: "ignore", stdout: "pipe", stderr: "pipe", timeout: 20_000, killSignal: "SIGKILL",
  });
  const [code, stdout, stderr] = await Promise.all([
    child.exited, new Response(child.stdout).text(), new Response(child.stderr).text(),
  ]);
  const smoke = { code, stdout, stderr };
  if (smoke.code !== 0) throw new Error(`Rust/OMP smoke failed:\n${smoke.stdout}\n${smoke.stderr}`);
  expect(smoke.code).toBe(0);
  expect(smoke.stdout).toContain("External-directory permission enforced");
  expect(smoke.stdout).toContain("OMP smoke cleanup complete.");
}, 30_000);

test("workplan validation and old Bun entry point preserve arguments and exit status", async () => {
  const root = await fixture();
  await mkdir(join(root, "vendor/shiori"), { recursive: true });
  const script = join(root, "vendor/shiori/shiori");
  await writeFile(script, '#!/bin/sh\nprintf "%s\\n" "$@"\nexit 17\n');
  await chmod(script, 0o755);
  const result = rustCli(root, ["check-workplan", root, "plan with spaces"]);
  expect(result.code).toBe(17);
  expect(result.stdout.trim().split("\n")).toEqual(["validate", "plan with spaces", "--root", root, "--json"]);
  expect(rustCli(root, ["check-workplan"]).code).toBe(2);
  const shim = Bun.spawnSync([process.execPath, join(repo, "scripts/check-workplan.ts"), root, "plan with spaces"], {
    env: { ...process.env, TSUNA_ROOT: root, TSUNA_NATIVE_BIN: nativeBinary }, stdout: "pipe", stderr: "pipe",
  });
  expect(shim.exitCode).toBe(17);
  expect(shim.stdout.toString()).toBe(result.stdout);
});
