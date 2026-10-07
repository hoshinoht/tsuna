// Verifies an OpenCode release as a Shiori write host (contracts §19) and,
// when every gate passes, adds it to SUPPORTED_HOST_VERSIONS.
//
//   bun run verify-host [version] [--dry-run]
//
// Gates, in order:
//   1. contract: the @opencode/client and @opencode/plugin surface the adapter
//      depends on is unchanged from the newest verified version;
//   2. the adapter test suite;
//   3. the live runtime smoke test against an installed binary of exactly
//      that version (OPENCODE_BIN, Homebrew or the desktop app's CLI).
//
// Without a version, the newest installed binary's version is verified. Nothing is
// committed; --dry-run runs the gates without editing the list.
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

import { SUPPORTED_HOST_VERSIONS } from "../src/plugin";

const ADAPTER = resolve(import.meta.dir, "..");
const PLUGIN_SRC = join(ADAPTER, "src", "plugin.ts");
const FIXTURE = process.env.SHIORI_SMOKE_FIXTURE ?? resolve(ADAPTER, "../../testdata/fixtures/full-valid");

/** Routes the host client calls (src/host-client.ts), as the generated client declares them. */
const ROUTES = [
  "GET /api/info",
  "GET /api/session/${encodeURIComponent(input.sessionID)}",
  "POST /api/session/${encodeURIComponent(input.sessionID)}/permission",
  "POST /api/rpc/${encodeURIComponent(input.rpcID)}/${encodeURIComponent(input.method)}",
  "GET /api/event",
];

/** Generated types behind those routes and the permission events; compared with every type they reference. */
const TYPE_ROOTS = [
  "ServerInfo", "SessionGetOutput", "PermissionCreateInput", "PermissionCreateOutput",
  "PermissionAsked", "PermissionReplied", "RpcCallInput", "RpcCallOutput",
];

/** Service discovery (service.json) and its version check. */
const DISCOVERY_FILES = ["dist/promise/service.js", "dist/service-version.js"];

const binCandidates = [
  process.env.OPENCODE_BIN,
  "/opt/homebrew/bin/opencode",
  "/Applications/OpenCode.app/Contents/Resources/opencode-cli",
].filter((b): b is string => Boolean(b));

function run(cmd: string, args: string[], options: { cwd?: string; env?: NodeJS.ProcessEnv; quiet?: boolean } = {}) {
  const r = spawnSync(cmd, args, { cwd: options.cwd, env: options.env ?? process.env, encoding: "utf8", stdio: options.quiet ? "pipe" : "inherit" });
  return { ok: r.status === 0, out: `${r.stdout ?? ""}${r.stderr ?? ""}` };
}

function binVersion(bin: string): string | undefined {
  if (!existsSync(bin)) return undefined;
  const r = run(bin, ["--version"], { quiet: true });
  return r.ok ? /(\d+\.\d+\.\d+\S*)/.exec(r.out)?.[1] : undefined;
}

function fail(message: string): never {
  console.error(`\n✗ ${message}`);
  process.exit(1);
}

function pack(name: string, version: string, dir: string): string {
  const dest = join(dir, `${name.replace(/\W+/g, "-")}-${version}`);
  run("mkdir", ["-p", dest], { quiet: true });
  const r = run("npm", ["pack", `${name}@${version}`, "--pack-destination", dest, "--silent"], { quiet: true });
  if (!r.ok) fail(`npm pack ${name}@${version} failed:\n${r.out.trim()}`);
  const tgz = readdirSync(dest).find((f) => f.endsWith(".tgz"));
  if (!tgz || !run("tar", ["xzf", join(dest, tgz), "-C", dest], { quiet: true }).ok) fail(`could not unpack ${name}@${version}`);
  return join(dest, "package");
}

function walk(dir: string, rel = ""): string[] {
  return readdirSync(join(dir, rel), { withFileTypes: true }).flatMap((e) =>
    e.isDirectory() ? walk(dir, join(rel, e.name)) : [join(rel, e.name)]);
}

/** Bundler chunk names carry content hashes and change between releases. */
const unchunk = (text: string) => text.replace(/[\w-]+-[a-z0-9]{8}\.js/g, "CHUNK.js");

/** Every `request({...})` / `sse({...})` declaration in the client bundle, keyed by method and path. */
function routeTable(pkg: string): Map<string, string[]> {
  const table = new Map<string, string[]>();
  for (const file of walk(pkg, "dist").filter((f) => f.endsWith(".js"))) {
    const text = readFileSync(join(pkg, file), "utf8");
    for (const m of text.matchAll(/\b(?:request|sse)\(\{/g)) {
      let depth = 0;
      let end = m.index! + m[0].length - 1;
      for (; end < text.length; end++) {
        if (text[end] === "{") depth++;
        else if (text[end] === "}" && --depth === 0) break;
      }
      const block = text.slice(m.index!, end + 1).replace(/\s+/g, " ");
      const method = /method: "([A-Z]+)"/.exec(block)?.[1];
      const path = /path: `([^`]*)`/.exec(block)?.[1];
      if (!method || path === undefined) continue;
      const key = `${method} ${path}`;
      const list = table.get(key) ?? [];
      if (!list.includes(block)) list.push(block);
      table.set(key, list);
    }
  }
  return table;
}

/** Top-level `export type` declarations of the generated promise client. */
function typeTable(pkg: string): Map<string, string> {
  const text = readFileSync(join(pkg, "dist/promise/generated/types.d.ts"), "utf8");
  const table = new Map<string, string>();
  for (const decl of text.split(/^(?=export type )/m)) {
    const name = /^export type (\w+)/.exec(decl)?.[1];
    if (name) table.set(name, decl.trim());
  }
  return table;
}

function typeClosure(types: Map<string, string>, roots: string[]): Set<string> {
  const seen = new Set<string>();
  const queue = roots.filter((r) => types.has(r));
  while (queue.length > 0) {
    const name = queue.pop()!;
    if (seen.has(name)) continue;
    seen.add(name);
    for (const id of types.get(name)!.slice(`export type ${name}`.length).match(/\b[A-Z]\w*\b/g) ?? []) {
      if (types.has(id) && !seen.has(id)) queue.push(id);
    }
  }
  return seen;
}

function contractGate(baseline: string, candidate: string, work: string): string[] {
  const problems: string[] = [];
  const oldClient = pack("@opencode/client", baseline, work);
  const newClient = pack("@opencode/client", candidate, work);

  const oldRoutes = routeTable(oldClient);
  const newRoutes = routeTable(newClient);
  for (const route of ROUTES) {
    const before = oldRoutes.get(route);
    const after = newRoutes.get(route);
    if (!before) problems.push(`route ${route} is missing from the ${baseline} baseline (update ROUTES)`);
    else if (!after) problems.push(`route ${route} is gone in ${candidate}`);
    else if (JSON.stringify(before) !== JSON.stringify(after)) problems.push(`route ${route} changed:\n    ${baseline}: ${before.join(" | ")}\n    ${candidate}: ${after.join(" | ")}`);
  }

  const oldTypes = typeTable(oldClient);
  const newTypes = typeTable(newClient);
  for (const root of TYPE_ROOTS) if (!oldTypes.has(root)) problems.push(`type ${root} is missing from the ${baseline} baseline (update TYPE_ROOTS)`);
  const closure = typeClosure(oldTypes, TYPE_ROOTS);
  for (const name of [...closure].sort()) {
    if (!newTypes.has(name)) problems.push(`type ${name} is gone in ${candidate}`);
    else if (newTypes.get(name) !== oldTypes.get(name)) problems.push(`type ${name} changed`);
  }

  for (const file of DISCOVERY_FILES) {
    const a = join(oldClient, file);
    const b = join(newClient, file);
    if (!existsSync(a) || !existsSync(b)) problems.push(`${file} is missing in ${existsSync(a) ? candidate : baseline}`);
    else if (unchunk(readFileSync(a, "utf8")) !== unchunk(readFileSync(b, "utf8"))) problems.push(`service discovery ${file} changed`);
  }

  const oldPlugin = pack("@opencode/plugin", baseline, work);
  const newPlugin = pack("@opencode/plugin", candidate, work);
  const decls = (pkg: string) => walk(pkg).filter((f) => f.endsWith(".d.ts")).sort();
  const pluginFiles = [...new Set([...decls(oldPlugin), ...decls(newPlugin)])];
  for (const f of pluginFiles) {
    const a = join(oldPlugin, f);
    const b = join(newPlugin, f);
    if (!existsSync(a) || !existsSync(b)) problems.push(`@opencode/plugin ${f} only in ${existsSync(a) ? baseline : candidate}`);
    else if (unchunk(readFileSync(a, "utf8")) !== unchunk(readFileSync(b, "utf8"))) problems.push(`@opencode/plugin ${f} changed`);
  }

  console.log(`  ${ROUTES.length} routes, ${closure.size} types, ${DISCOVERY_FILES.length} discovery files, ${pluginFiles.length} plugin declarations compared`);
  return problems;
}

function addVersion(version: string): void {
  const text = readFileSync(PLUGIN_SRC, "utf8");
  const decl = /export const SUPPORTED_HOST_VERSIONS = \[([^\]]*)\] as const;/;
  if (!decl.test(text)) fail(`could not find SUPPORTED_HOST_VERSIONS in ${PLUGIN_SRC}`);
  const key = (v: string) => v.split(".").map(Number);
  const versions = [...new Set([...SUPPORTED_HOST_VERSIONS, version])].sort((a, b) => {
    const [x, y] = [key(a), key(b)];
    return x[0] - y[0] || x[1] - y[1] || x[2] - y[2];
  });
  writeFileSync(PLUGIN_SRC, text.replace(decl, `export const SUPPORTED_HOST_VERSIONS = [${versions.map((v) => JSON.stringify(v)).join(", ")}] as const;`));
}

const args = process.argv.slice(2);
const dryRun = args.includes("--dry-run");
const unknown = args.filter((a) => a.startsWith("-") && a !== "--dry-run");
if (unknown.length > 0) fail(`unknown option ${unknown.join(" ")}; usage: bun run verify-host [version] [--dry-run]`);
const requested = args.find((a) => !a.startsWith("-"));

const installed = binCandidates.map((bin) => ({ bin, version: binVersion(bin) }));
const newest = installed
  .filter((i) => i.version && /^\d+\.\d+\.\d+$/.test(i.version))
  .map((i) => i.version!.split(".").map(Number))
  .sort((a, b) => b[0] - a[0] || b[1] - a[1] || b[2] - a[2])[0];
const target = requested ?? newest?.join(".");
if (!target) fail(`no version given and no OpenCode binary found (tried ${binCandidates.join(", ")})`);
if (!/^\d+\.\d+\.\d+$/.test(target)) fail(`only stable X.Y.Z releases can be verified, got ${target}`);
const host = installed.find((i) => i.version === target);
if (!host) {
  fail(`no installed OpenCode ${target} for the live smoke test. Found: ${installed.map((i) => `${i.bin} (${i.version ?? "absent"})`).join(", ")}. ` +
    "Set OPENCODE_BIN to a binary of that version.");
}
if ((SUPPORTED_HOST_VERSIONS as readonly string[]).includes(target)) {
  console.log(`OpenCode ${target} is already verified (${SUPPORTED_HOST_VERSIONS.join(", ")}).`);
  process.exit(0);
}
const baseline = SUPPORTED_HOST_VERSIONS[SUPPORTED_HOST_VERSIONS.length - 1];
if (!existsSync(join(FIXTURE, ".opencode", "workplan"))) fail(`smoke fixture ${FIXTURE} has no .opencode/workplan (set SHIORI_SMOKE_FIXTURE)`);

console.log(`Verifying OpenCode ${target} against ${baseline} (binary ${host.bin})\n`);
const work = mkdtempSync(join(process.env.SHIORI_VERIFY_TMP ?? tmpdir(), "shiori-verify-host-"));
try {
  console.log("1/3 contract");
  const problems = contractGate(baseline, target, work);
  if (problems.length > 0) {
    fail(`contract changed between ${baseline} and ${target}; review these before verifying by hand:\n  - ${problems.join("\n  - ")}`);
  }
  console.log("  unchanged\n\n2/3 adapter tests");
  const env = { ...process.env };
  delete env.SHIORI_RUNTIME_SMOKE;
  if (!run("bun", ["test"], { cwd: ADAPTER, env }).ok) fail("the adapter test suite failed");

  console.log(`\n3/3 live smoke test (fixture ${FIXTURE})`);
  const smoke = run("bun", ["test", "test/runtime-smoke.test.ts"], {
    cwd: ADAPTER,
    env: { ...env, SHIORI_RUNTIME_SMOKE: "1", OPENCODE_BIN: host.bin, SHIORI_SMOKE_FIXTURE: FIXTURE, SHIORI_SMOKE_SCRATCH: work },
  });
  if (!smoke.ok) fail(`the live smoke test failed on OpenCode ${target}`);
} finally {
  rmSync(work, { recursive: true, force: true });
}

if (dryRun) {
  console.log(`\n✓ OpenCode ${target} passed every gate (dry run: SUPPORTED_HOST_VERSIONS unchanged).`);
} else {
  addVersion(target);
  console.log(`\n✓ OpenCode ${target} passed every gate and was added to SUPPORTED_HOST_VERSIONS in src/plugin.ts. Review and commit it.`);
}
