#!/usr/bin/env bun
/**
 * Reproducible offline demonstration (no credentials, no network, fixture model).
 *
 *   bun scripts/demo.ts [--out DIR]
 *
 * Creates a throwaway git workspace and a throwaway Tsuna state directory,
 * runs session 1 and session 2 (a separate process resuming the same root)
 * through ./bin/tsuna, then verifies that owned resources were released.
 */
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const here = resolve(import.meta.dir, "..");
const outArg = process.argv.indexOf("--out");
const out = outArg > 0 ? resolve(process.argv[outArg + 1]!) : mkdtempSync(join(tmpdir(), "tsuna-demo-"));
mkdirSync(out, { recursive: true });
const workspace = join(out, "workspace");
const state = join(out, "state");
const mcpLog = join(out, "mcp-fixture.log");
mkdirSync(join(workspace, "src"), { recursive: true });
mkdirSync(join(workspace, "test"), { recursive: true });
mkdirSync(join(workspace, "docs"), { recursive: true });
writeFileSync(join(workspace, "src/main.ts"), "export function main() {\n  return greet('tsuna');\n}\nimport { greet } from './util.ts';\n");
writeFileSync(join(workspace, "src/util.ts"), "export function greet(name: string) {\n  return `hello ${name}`;\n}\n");
writeFileSync(join(workspace, "test/main.test.ts"), "import { main } from '../src/main.ts';\nif (main() !== 'hello tsuna') throw new Error('bad');\n");
writeFileSync(join(workspace, "docs/guide.md"), "# Guide\n\nRun `bun src/main.ts`.\n");
writeFileSync(join(workspace, "AGENTS.md"), "# Demo project\n\nKeep changes small.\n");
const git = (...args: string[]) => Bun.spawnSync(["git", "-c", "user.name=demo", "-c", "user.email=demo@example.invalid", ...args], { cwd: workspace });
git("init", "-q");
git("add", ".");
git("commit", "-q", "-m", "demo workspace");
writeFileSync(join(workspace, "src/util.ts"), readFileSync(join(workspace, "src/util.ts"), "utf8") + "// uncommitted edit\n");

const env = { ...process.env, TSUNA_DEMO_MCP_LOG: mcpLog, HOME: join(out, "home") };
delete (env as Record<string, string | undefined>).TSUNA_ROOT;
mkdirSync(env.HOME!, { recursive: true });

function session(n: number, extra: string[]) {
	const args = [join(here, "bin/tsuna"), "--config", join(here, "fixtures/demo/tsuna.config.json"), "--state", state, "--cwd", workspace, "--headless", "--script", join(here, `fixtures/demo/session${n}.tsuna`), ...extra];
	console.log(`\n$ ${args.map(a => a.replace(out, "$DEMO")).join(" ")}\n`);
	const proc = Bun.spawnSync(args, { env, stdout: "pipe", stderr: "pipe" });
	const text = proc.stdout.toString();
	const err = proc.stderr.toString();
	writeFileSync(join(out, `session${n}.log`), text + (err ? `\n[stderr]\n${err}` : ""));
	console.log(text.replaceAll(out, "$DEMO"));
	if (err.trim()) console.log(`[stderr]\n${err}`);
	return { code: proc.exitCode, text };
}

function alive(pid: number) {
	try {
		process.kill(pid, 0);
		return true;
	} catch {
		return false;
	}
}

const checks: [string, boolean][] = [];
const s1 = session(1, []);
checks.push(["session 1 exited 0", s1.code === 0]);
const root = /^root (\S+)/m.exec(s1.text)?.[1];
if (!root) throw new Error("could not find the root id in session 1 output");
checks.push(["session 1 steered the running explorer", /steer → explore-\S+: delivered/.test(s1.text)]);
checks.push(["structured results received (explore + tester)", (s1.text.match(/★ result (explore|tester)-\S+\/r1 success/g) ?? []).length === 2]);
checks.push(["orchestrator collected both results", /Survey complete: received 2 specialist results/.test(s1.text)]);
checks.push(["follow-up continued the explorer (r2)", /★ result explore-\S+\/r2 success/.test(s1.text)]);
checks.push(["explorer parked", /parked explore-/.test(s1.text)]);
const s2 = session(2, ["--resume", root]);
checks.push(["session 2 exited 0", s2.code === 0]);
checks.push(["revived after restart", /resume explore-\S+: revived/.test(s2.text)]);
checks.push(["revived child result r3 accepted", /★ result explore-\S+\/r3 success/.test(s2.text)]);
checks.push(["denied operation shown (external read)", /⛔ explore-\S+ read denied: This permission requires an interactive primary session/.test(s2.text)]);
checks.push(["out-of-catalog tool refused (bash)", /bash: Tool bash not found/.test(s2.text)]);
const lockPath = join(state, "agents", root, "lock");
checks.push(["root lock released", !existsSync(lockPath)]);
const pids = existsSync(mcpLog) ? readFileSync(mcpLog, "utf8").split("\n").filter(l => l.startsWith("start ")).map(l => Number(l.split(" pid ")[1])) : [];
checks.push([`MCP fixture processes started (${pids.length}) and all exited`, pids.length === 2 && pids.every(p => !alive(p))]);
const jobsDir = join(state, "jobs");
const jobStates = existsSync(jobsDir) ? readdirSync(jobsDir).map(d => JSON.parse(readFileSync(join(jobsDir, d, "status.json"), "utf8")).state as string) : [];
checks.push([`supervised jobs terminal (${jobStates.join(",") || "none"})`, jobStates.length > 0 && jobStates.every(s => ["completed", "timed_out", "startup_failed", "cancelled"].includes(s))]);
checks.push(["no writes to the demo HOME's ~/.omp or ~/.pi", !existsSync(join(env.HOME!, ".omp")) && !existsSync(join(env.HOME!, ".pi"))]);
const records = readdirSync(join(state, "agents", root, "records")).map(f => JSON.parse(readFileSync(join(state, "agents", root, "records", f), "utf8")));
checks.push(["all agents parked or idle after shutdown (no live state)", records.every(r => ["parked", "idle"].includes(r.status))]);

console.log("\nVerification:");
let ok = true;
for (const [name, pass] of checks) {
	console.log(`  ${pass ? "PASS" : "FAIL"}  ${name}`);
	ok &&= pass;
}
console.log(`\nArtifacts: ${out}`);
process.exit(ok ? 0 : 1);
