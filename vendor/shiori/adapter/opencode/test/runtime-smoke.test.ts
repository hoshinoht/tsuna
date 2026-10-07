// Opt-in runtime smoke and parity run on PRIVATE OpenCode servers.
//
//   SHIORI_RUNTIME_SMOKE=1 SHIORI_SMOKE_FIXTURE=/path/to/project-copy-source \
//   [SHIORI_SMOKE_PLAN=<workplan id in the fixture>] [SHIORI_BIN=/abs/shiori] \
//   [SHIORI_REFERENCE_PLUGIN=/path/to/reference/workplan-tools] \
//   [SHIORI_SMOKE_SCRATCH=/private/tmp/...] [SHIORI_SMOKE_EVIDENCE=out.json] \
//   bun test test/runtime-smoke.test.ts
//
// Each server gets its own HOME/XDG_CONFIG_HOME/XDG_DATA_HOME/XDG_STATE_HOME/
// XDG_CACHE_HOME/TMPDIR under the scratch directory, its own loopback port
// and a project that is a `cp -Rp` copy of the fixture (the fixture itself is
// never written; its fingerprint is checked). No model is called: a
// test-only harness plugin relays tool calls to the effective native catalog,
// and the test answers real `permission.asked` events through the public API
// as the user would. Only the processes this test starts are stopped.
import { expect, it } from "bun:test";
import { chmodSync, cpSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { isAbsolute, join, relative, resolve } from "node:path";
import { createHash, randomUUID } from "node:crypto";

import { makeHostClient, type Endpoint, type HostEvent } from "../src/host-client";
import { shioriBin } from "./helpers";

const RUN = process.env.SHIORI_RUNTIME_SMOKE === "1";
const OPENCODE = process.env.OPENCODE_BIN ?? "/opt/homebrew/bin/opencode";
const FIXTURE = process.env.SHIORI_SMOKE_FIXTURE;
const REFERENCE = process.env.SHIORI_REFERENCE_PLUGIN;
const HARNESS_RPC = "shiori-runtime-harness";

type Kind = "shi" | "ref";

function pause(ms: number) {
  return new Promise((r) => setTimeout(r, ms));
}

async function reservePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((ok, fail) => {
    server.once("error", fail);
    server.listen(0, "127.0.0.1", ok);
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("no port");
  await new Promise<void>((ok) => server.close(() => ok()));
  return address.port;
}

/** sha256 over every file's relative path, mode and bytes (read-only walk). */
function treeHash(root: string): string {
  const h = createHash("sha256");
  const walk = (dir: string) => {
    for (const name of readdirSync(dir).sort()) {
      const p = join(dir, name);
      const st = lstatSync(p);
      h.update(`${relative(root, p)}\0${st.mode}\0`);
      if (st.isDirectory()) walk(p);
      else if (st.isFile()) h.update(readFileSync(p));
    }
  };
  walk(root);
  return h.digest("hex");
}

function fileMap(root: string): Map<string, string> {
  const out = new Map<string, string>();
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const p = join(dir, name);
      const st = lstatSync(p);
      if (st.isDirectory()) walk(p);
      else out.set(relative(root, p), `${st.mode & 0o777}:${st.mtimeMs}:${createHash("sha256").update(readFileSync(p)).digest("hex")}`);
    }
  };
  walk(root);
  return out;
}

function changedFiles(a: Map<string, string>, b: Map<string, string>): string[] {
  const keys = new Set([...a.keys(), ...b.keys()]);
  return [...keys].filter((k) => a.get(k) !== b.get(k)).sort();
}

function makeWritable(path: string) {
  const st = lstatSync(path);
  if (st.isSymbolicLink()) return;
  chmodSync(path, (st.mode & 0o777) | 0o200 | (st.isDirectory() ? 0o100 : 0));
  if (st.isDirectory()) for (const name of readdirSync(path)) makeWritable(join(path, name));
}

/** Copies the reference plugin package with absolute dependency links (read-only source). */
function copyReference(source: string, dest: string) {
  mkdirSync(dest, { recursive: true });
  for (const name of ["index.ts", "package.json"]) cpSync(join(source, name), join(dest, name));
  cpSync(join(source, "src"), join(dest, "src"), { recursive: true });
  const modules = join(dest, "node_modules");
  mkdirSync(join(modules, "@opencode"), { recursive: true });
  for (const dep of ["@opencode/client", "@opencode/plugin", "zod"]) {
    symlinkSync(realpathSync(join(source, "node_modules", dep)), join(modules, dep), "dir");
  }
}

interface Server {
  kind: Kind;
  root: string;
  base: string;
  endpoint: Endpoint;
  proc: ReturnType<typeof Bun.spawn>;
  scratch: string;
  pluginDir: string;
}

async function startServer(kind: Kind, scratchBase: string, fixture: string, planId: string): Promise<Server> {
  const scratch = join(scratchBase, kind);
  const home = join(scratch, "home");
  const env = {
    HOME: home,
    XDG_CONFIG_HOME: join(home, ".config"),
    XDG_DATA_HOME: join(home, ".local", "share"),
    XDG_STATE_HOME: join(home, ".local", "state"),
    XDG_CACHE_HOME: join(home, ".cache"),
    TMPDIR: join(scratch, "tmp"),
    PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
    LANG: "en_US.UTF-8",
    NO_COLOR: "1",
  };
  for (const d of [env.XDG_CONFIG_HOME + "/opencode", env.XDG_DATA_HOME, env.XDG_STATE_HOME, env.XDG_CACHE_HOME, env.TMPDIR]) mkdirSync(d, { recursive: true });
  const project = join(scratch, "project");
  cpSync(fixture, project, { recursive: true, preserveTimestamps: true });
  makeWritable(project);
  const root = realpathSync(project);

  const harness = join(scratch, "harness");
  cpSync(join(import.meta.dir, "runtime", "harness"), harness, { recursive: true });
  let pluginEntry: Record<string, unknown>;
  let pluginDir: string;
  if (kind === "shi") {
    pluginDir = join(scratch, "shiori-adapter");
    mkdirSync(pluginDir, { recursive: true });
    for (const name of ["package.json", "index.ts"]) cpSync(join(import.meta.dir, "..", name), join(pluginDir, name));
    cpSync(join(import.meta.dir, "..", "src"), join(pluginDir, "src"), { recursive: true });
    pluginEntry = { package: pluginDir, options: { bin: shioriBin() } };
  } else {
    pluginDir = join(scratch, "reference-plugin");
    copyReference(REFERENCE!, pluginDir);
    pluginEntry = { package: pluginDir };
  }
  const wpDir = join(root, ".opencode", "workplan");
  const deny = join(wpDir, "deny-target.json");
  // Exact absolute rules: the real plan and the scratch plan's artifacts
  // are "ask" (answered by the test as the user); one target is "deny".
  const rules = [
    ...[`${planId}.json`, "parity-demo.json", "parity-demo.md", "parity-demo.checkpoint.json",
      "parity-demo.evidence.json", "parity-demo.lanes.json", "parity-demo.links.json",
    ].map((n) => ({ action: "edit", resource: join(wpDir, n), effect: "ask" })),
    { action: "edit", resource: deny, effect: "deny" },
  ];
  writeFileSync(join(env.XDG_CONFIG_HOME, "opencode", "opencode.json"), JSON.stringify({
    plugins: ["-opencode.plan", pluginEntry, { package: harness }],
    agents: {
      orchestrator: { description: "Isolated Shiori runtime smoke", mode: "primary", system: "Runtime smoke only.", permissions: rules },
      plan: { description: "Isolated Shiori runtime smoke planner", mode: "primary", system: "Runtime smoke only.", permissions: rules },
    },
  }, null, 2));
  const port = await reservePort();
  const base = `http://127.0.0.1:${port}`;
  const proc = Bun.spawn([OPENCODE, "serve", "--service", "--hostname", "127.0.0.1", "--port", String(port)], {
    cwd: root, env, stdin: "ignore", stdout: "ignore", stderr: "pipe",
  });
  for (let i = 0; i < 200; i++) {
    if (proc.exitCode !== null) throw new Error(`private server (${kind}) exited during startup: ${await new Response(proc.stderr).text()}`);
    try {
      const r = await fetch(`${base}/api/info`);
      if (r.status < 500) break;
    } catch {}
    await pause(100);
  }
  let info: any;
  for (let i = 0; i < 100; i++) {
    try {
      info = JSON.parse(readFileSync(join(env.XDG_STATE_HOME, "opencode", "service.json"), "utf8"));
      if (info?.url) break;
    } catch {}
    await pause(100);
  }
  const endpoint: Endpoint = typeof info?.password === "string"
    ? { url: base, auth: { type: "basic", username: "opencode", password: info.password } }
    : { url: base };
  return { kind, root, base, endpoint, proc, scratch, pluginDir };
}

async function stopServer(s: Server) {
  if (s.proc.exitCode !== null) return;
  s.proc.kill("SIGTERM");
  const exited = await Promise.race([s.proc.exited.then(() => true), pause(5_000).then(() => false)]);
  if (!exited) {
    s.proc.kill("SIGKILL");
    await s.proc.exited;
  }
}

function authHeaders(s: Server): Record<string, string> {
  return s.endpoint.auth ? { authorization: "Basic " + Buffer.from(`opencode:${s.endpoint.auth.password}`).toString("base64") } : {};
}

async function api(s: Server, method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<any> {
  const r = await fetch(`${s.base}${path}`, {
    method,
    headers: { ...authHeaders(s), ...(body === undefined ? {} : { "content-type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  const text = await r.text();
  if (r.status >= 300) throw new Error(`${method} ${path} -> ${r.status}: ${text.slice(0, 300)}`);
  return text ? JSON.parse(text) : undefined;
}

async function rpc(s: Server, method: string, input: unknown, signal?: AbortSignal): Promise<any> {
  const q = new URLSearchParams({ "location[directory]": s.root });
  return (await api(s, "POST", `/api/rpc/${HARNESS_RPC}/${method}?${q}`, { input }, signal)).output;
}

async function createSession(s: Server, agent: string, title: string, permissions: unknown[] = []) {
  return (await api(s, "POST", "/api/session", { title, agent, location: { directory: s.root }, permissions })).data as { id: string };
}

function watchAsks(s: Server, sessionID: string) {
  const controller = new AbortController();
  const events: HostEvent[] = [];
  const waiters: Array<(e: HostEvent) => void> = [];
  const client = makeHostClient(s.endpoint);
  const done = (async () => {
    try {
      for await (const e of client.event.subscribe({ signal: controller.signal })) {
        if (e.type !== "permission.asked" || e.data?.sessionID !== sessionID) continue;
        const w = waiters.shift();
        if (w) w(e);
        else events.push(e);
      }
    } catch {}
  })();
  return {
    next(timeoutMs: number): Promise<HostEvent | undefined> {
      const e = events.shift();
      if (e) return Promise.resolve(e);
      return new Promise((r) => {
        const t = setTimeout(() => r(undefined), timeoutMs);
        waiters.push((ev) => { clearTimeout(t); r(ev); });
      });
    },
    async close() {
      controller.abort();
      await done;
    },
  };
}

interface StepResult {
  label: string;
  tool: string;
  ok: boolean;
  content?: string;
  error?: string;
  asks: number;
  askResources?: string[];
  decision?: string;
}

type Decision = "none" | "once" | "reject" | "cancel-late";

async function runStep(s: Server, session: { id: string; agent: string }, label: string, tool: string, args: Record<string, unknown>, decision: Decision = "none"): Promise<StepResult> {
  const suffix = randomUUID().replaceAll("-", "").slice(0, 12);
  const caller = { sessionID: session.id, agent: session.agent, messageID: `msg_smoke_${suffix}`, toolCallID: `call_smoke_${suffix}` };
  const asks = watchAsks(s, session.id);
  await pause(50); // let the watcher subscribe
  const controller = new AbortController();
  const result: StepResult = { label, tool, ok: false, asks: 0, decision };
  try {
    const run = rpc(s, "run", { toolName: tool, args, ...caller }, controller.signal).then((o) => ({ o }), (e) => ({ e }));
    for (;;) {
      const first = await Promise.race([run.then((r) => ({ kind: "done" as const, r })), asks.next(decision === "none" ? 60_000 : 30_000).then((ev) => ({ kind: "ask" as const, ev }))]);
      if (first.kind === "done") {
        const r = first.r as { o?: any; e?: any };
        if (r.o) {
          result.ok = r.o.ok;
          if (r.o.ok) result.content = r.o.content;
          else result.error = r.o.error;
        } else result.error = `rpc: ${String(r.e?.message ?? r.e)}`;
        break;
      }
      const ev = first.ev;
      if (!ev) continue;
      result.asks++;
      const d = ev.data;
      // The request must carry the trusted source and exact canonical resources.
      if (d.action !== "edit" || d.source?.messageID !== caller.messageID || d.source?.id !== caller.toolCallID) throw new Error(`${label}: foreign ask`);
      for (const r of d.resources) {
        if (!isAbsolute(r) || resolve(r) !== r || /[*?[\]]/.test(r) || relative(s.root, r).startsWith("..")) throw new Error(`${label}: non-exact resource ${r}`);
      }
      result.askResources = d.resources.map((r: string) => relative(s.root, r)).sort();
      if (decision === "none") {
        await api(s, "POST", `/api/session/${session.id}/permission/${d.id}/reply`, { decision: "reject" });
        throw new Error(`${label}: unexpected permission request`);
      }
      if (decision === "cancel-late") {
        controller.abort();
        await run;
        await api(s, "POST", `/api/session/${session.id}/permission/${d.id}/reply`, { decision: "once" });
        await pause(300);
        result.error = "cancelled before the user reply";
        break;
      }
      await api(s, "POST", `/api/session/${session.id}/permission/${d.id}/reply`, { decision });
    }
  } finally {
    await asks.close();
  }
  return result;
}

const parse = (r: StepResult) => JSON.parse(r.content!);

async function scenario(s: Server, planId: string, steps: StepResult[], facts: Record<string, unknown>): Promise<void> {
  facts.runtimeVersion = (await api(s, "GET", "/api/info")).version;
  const status = await rpc(s, "status", {});
  facts.nativeTools = status.nativeTools;
  const planS = { ...(await createSession(s, "plan", "smoke plan")), agent: "plan" };
  const orchS = { ...(await createSession(s, "orchestrator", "smoke orchestrator")), agent: "orchestrator" };
  const step = async (...a: Parameters<typeof runStep>) => {
    const r = await runStep(...a);
    steps.push(r);
    return r;
  };
  const before = fileMap(s.root);
  await step(s, planS, "list", "workplan_list", {});
  await step(s, planS, "read", "workplan_read", { id: planId });
  const r3 = await step(s, planS, "read-no-md", "workplan_read", { id: planId, includeMarkdown: false });
  await step(s, planS, "inspect", "workplan_inspect", { id: planId });
  await step(s, planS, "inspect-limit3", "workplan_inspect", { id: planId, limit: 3 });
  await step(s, planS, "validate", "workplan_validate", { id: planId });
  await step(s, planS, "resume-4096", "workplan_resume", { id: planId, maxChars: 4096 });
  await step(s, planS, "resume-12000", "workplan_resume", { id: planId });
  await step(s, planS, "resume-64000", "workplan_resume", { id: planId, maxChars: 64000 });
  await step(s, planS, "doctor", "workplan_doctor", {});
  await step(s, planS, "doctor-id", "workplan_doctor", { id: planId, limit: 10 });
  await step(s, planS, "compact-preview", "workplan_compact_preview", { id: planId, archiveReason: "parity preview" });
  await step(s, planS, "compact-mode-preview", "workplan_compact", { id: planId, archiveReason: "parity preview" });
  facts.readsChangedFiles = changedFiles(before, fileMap(s.root));

  await step(s, planS, "err-root-override", "workplan_read", { id: planId, workspaceRoot: "/" });
  await step(s, planS, "err-missing-hash", "workplan_update", { id: planId, appendNotes: ["x"] });
  await step(s, planS, "err-role-checkpoint", "workplan_checkpoint", { id: planId, summary: "s", nextAction: "n", expectedHash: parse(r3).stateHash });
  await step(s, planS, "err-missing-plan", "workplan_read", { id: "does-not-exist" });
  await step(s, planS, "err-stale", "workplan_update", { id: planId, expectedHash: "0".repeat(64), title: "x" });
  await step(s, planS, "err-preview-apply-injection", "workplan_compact_preview", { id: planId, archiveReason: "x", mode: "apply" });

  // One authorized mutation of the real plan through the host permission path.
  const preMutation = fileMap(s.root);
  const m1 = await step(s, planS, "update-real-plan", "workplan_update", { id: planId, expectedHash: parse(r3).stateHash, appendNotes: ["Runtime smoke note (isolated copy)"] }, "once");
  facts.realPlanMutationChangedFiles = changedFiles(preMutation, fileMap(s.root));
  facts.realPlanMutationAskResources = m1.askResources;

  // Lifecycle on a scratch plan.
  const created = await step(s, planS, "create", "workplan_create", {
    id: "parity-demo", kind: "software-engineering", title: "Parity demo", goal: "Parity goal", status: "in_progress",
    notes: ["archive this note"], reviewFindings: [{ severity: "major", title: "Resolved item", status: "resolved" }],
    phases: [
      { id: "done-phase", title: "Done", status: "completed", steps: [{ id: "done-step", title: "Setup", action: "Set up", validation: "Ready", status: "completed" }] },
      { id: "active-phase", title: "Active", status: "in_progress", steps: [{ id: "active-step", title: "Continue", action: "Continue", validation: "Test", status: "in_progress" }] },
    ],
  }, "once");
  const upd = await step(s, planS, "update", "workplan_update", { id: "parity-demo", expectedHash: parse(created).stateHash, appendNotes: ["second note"], updateSteps: [{ phaseId: "active-phase", stepId: "active-step", status: "review" }] }, "once");
  const patchText = ["*** Begin Patch", "*** Update File: .opencode/workplan/parity-demo.md", "@@", "-Parity goal", "+Parity goal after patch", "*** End Patch"].join("\n");
  const patched = await step(s, planS, "patch", "workplan_patch", { id: "parity-demo", expectedHash: parse(upd).stateHash, patchText }, "once");
  const cp = await step(s, orchS, "checkpoint", "workplan_checkpoint", { id: "parity-demo", expectedHash: parse(patched).metadata.stateHash, summary: "Ready to compact", nextAction: "Compact history", phaseId: "active-phase", stepId: "active-step" }, "once");
  await step(s, planS, "resume-after-checkpoint", "workplan_resume", { id: "parity-demo" });
  const prev = await step(s, planS, "compact-preview-demo", "workplan_compact_preview", { id: "parity-demo", archiveReason: "Archive completed history", completedPhaseIds: ["done-phase"], noteIndexes: [0], resolvedFindingIndexes: [0] });
  void cp;
  await step(s, orchS, "compact-apply", "workplan_compact", {
    id: "parity-demo", mode: "apply", archiveReason: "Archive completed history", completedPhaseIds: ["done-phase"], noteIndexes: [0], resolvedFindingIndexes: [0],
    confirmation: "ARCHIVE_SELECTED_HISTORY", previewToken: parse(prev).previewToken, expectedHash: parse(prev).stateHash,
  }, "once");
  const rr = await step(s, planS, "read-demo", "workplan_read", { id: "parity-demo", includeMarkdown: false });
  const reset = await step(s, planS, "reset", "workplan_reset", { id: "parity-demo", expectedHash: parse(rr).stateHash, mode: "draft", replaceMarkdown: true }, "once");
  if (s.kind === "shi") {
    const noNotes = await step(s, planS, "read-no-notes", "workplan_read", { id: "parity-demo", includeMarkdown: false, includeNotes: false });
    if (!noNotes.ok || parse(noNotes).workplan.notes !== undefined) throw new Error("includeNotes=false did not omit notes");
    const preExtensions = fileMap(s.root);
    const extensions = await step(s, planS, "update-extensions", "workplan_update", {
      id: "parity-demo", expectedHash: parse(reset).stateHash,
      recordEvidence: [{ phaseId: "active-phase", stepId: "active-step", command: "bun test", exitCode: 0 }],
      lanes: [{ op: "propose", laneId: "smoke", steps: [{ phaseId: "active-phase", stepId: "active-step" }], claims: ["src"] }],
      planLinks: [{ planId, relation: "related" }],
    }, "once");
    if (!extensions.ok || parse(extensions).stateHash !== parse(reset).stateHash) throw new Error("sidecar update failed or changed the plan state");
    facts.extensionsChangedFiles = changedFiles(preExtensions, fileMap(s.root));
    facts.extensionsAskResources = extensions.askResources;
  }
  const preReject = fileMap(s.root);
  await step(s, planS, "update-rejected", "workplan_update", { id: "parity-demo", expectedHash: parse(reset).stateHash, title: "must not land" }, "reject");
  facts.rejectChangedFiles = changedFiles(preReject, fileMap(s.root));
  const preDeny = fileMap(s.root);
  await step(s, planS, "create-denied", "workplan_create", { id: "deny-target", goal: "Denied by an absolute agent rule" });
  facts.denyChangedFiles = changedFiles(preDeny, fileMap(s.root));
  // P03: session rules follow host whole-path matching. An absolute session
  // deny blocks; a relative one does not match the adapter's absolute
  // resources (the host's default policy then applies). Disclosed, not
  // translated by the adapter.
  const absS = { ...(await createSession(s, "plan", "absolute session deny", [{ action: "edit", resource: join(s.root, ".opencode", "workplan", "abs-deny.json"), effect: "deny" }])), agent: "plan" };
  const absDeny = await step(s, absS, "create-session-absolute-deny", "workplan_create", { id: "abs-deny", goal: "Absolute session deny" }, "reject");
  facts.sessionAbsoluteDeny = absDeny.ok ? "allowed" : absDeny.asks ? "ask-rejected" : "denied";
  const relS = { ...(await createSession(s, "plan", "relative session deny", [{ action: "edit", resource: ".opencode/workplan/rel-deny.json", effect: "deny" }])), agent: "plan" };
  const relDeny = await step(s, relS, "create-session-relative-deny", "workplan_create", { id: "rel-deny", goal: "Relative session deny" }, "reject");
  facts.sessionRelativeDeny = relDeny.ok ? "not-matched-default-allow" : relDeny.asks ? "not-matched-ask-rejected" : "denied";
  const preCancel = fileMap(s.root);
  const rc = await step(s, planS, "read-before-cancel", "workplan_read", { id: planId, includeMarkdown: false });
  const statusBefore = (await rpc(s, "status", {})).completed;
  await step(s, planS, "update-cancel-late-reply", "workplan_update", { id: planId, expectedHash: parse(rc).stateHash, title: "Late approval must not commit" }, "cancel-late");
  facts.cancelChangedFiles = changedFiles(preCancel, fileMap(s.root));
  facts.cancelContinued = (await rpc(s, "status", {})).completed !== statusBefore;
  await step(s, planS, "doctor-final", "workplan_doctor", { limit: 20 });
}

function normalize(text: string | undefined, root: string): string {
  return (text ?? "").split(root).join("$ROOT");
}

function deepNormalize(text: string): string {
  return text
    .replace(/\d{4}-\d\d-\d\dT\d\d:\d\d(:\d\d(\.\d+)?)?Z/g, "$TIME")
    .replace(/v1-[0-9a-f]{64}/g, "$TOKEN")
    .replace(/[0-9a-f]{64}/g, "$HASH")
    .replace(/state-[0-9a-f]{12}-[0-9a-f]{12}/g, "state-$ARCHIVE");
}

/** JSON paths whose values differ; string arrays compare as sets (tx ids normalized). */
function jsonDiff(a: unknown, b: unknown, path = "$", out: string[] = []): string[] {
  const tx = (v: unknown) => typeof v === "string" ? v.replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|\b[0-9a-f]{16}\b/g, "$TX") : v;
  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.every((x) => typeof x === "string") && b.every((x) => typeof x === "string")) {
      const sa = new Set(a.map(tx));
      const sb = new Set(b.map(tx));
      for (const x of sa) if (!sb.has(x)) out.push(`${path}: only shiori has ${String(x)}`);
      for (const x of sb) if (!sa.has(x)) out.push(`${path}: only reference has ${String(x)}`);
      if (!out.some((o) => o.startsWith(path)) && JSON.stringify(a.map(tx)) !== JSON.stringify(b.map(tx))) out.push(`${path}: same members, different order`);
      return out;
    }
    if (a.length !== b.length) out.push(`${path}: length ${a.length} vs ${b.length}`);
    for (let i = 0; i < Math.min(a.length, b.length); i++) jsonDiff(a[i], b[i], `${path}.${i}`, out);
    return out;
  }
  if (a && b && typeof a === "object" && typeof b === "object") {
    const ka = Object.keys(a as object);
    const kb = Object.keys(b as object);
    for (const k of new Set([...ka, ...kb])) {
      if (!(k in (a as object))) out.push(`${path}.${k}: only in reference`);
      else if (!(k in (b as object))) out.push(`${path}.${k}: only in shiori`);
      else jsonDiff((a as any)[k], (b as any)[k], `${path}.${k}`, out);
    }
    if (ka.join() !== kb.join() && ka.length === kb.length) out.push(`${path}: key order differs`);
    return out;
  }
  if (tx(a) !== tx(b)) out.push(`${path}: ${JSON.stringify(a)?.slice(0, 700)} vs ${JSON.stringify(b)?.slice(0, 700)}`);
  return out;
}

const smokeIt = RUN && FIXTURE ? it : it.skip;

smokeIt("runs the adapter on a private OpenCode server (and the reference for parity)", async () => {
  const fixture = realpathSync(FIXTURE!);
  const fixtureBefore = treeHash(fixture);
  const scratchBase = mkdtempSync(join(process.env.SHIORI_SMOKE_SCRATCH ?? tmpdir(), "shiori-smoke-"));
  const planId = process.env.SHIORI_SMOKE_PLAN ??
    readdirSync(join(fixture, ".opencode", "workplan")).filter((n) => n.endsWith(".json") && n.split(".").length === 2).sort()[0]!.replace(/\.json$/, "");
  const evidence: Record<string, unknown> = { opencodeBin: OPENCODE, planIdLength: planId.length };
  const runs: Partial<Record<Kind, { steps: StepResult[]; facts: Record<string, unknown>; root: string }>> = {};
  const servers: Server[] = [];
  try {
    for (const kind of (REFERENCE ? ["shi", "ref"] : ["shi"]) as Kind[]) {
      const server = await startServer(kind, scratchBase, fixture, planId);
      servers.push(server);
      const run = { steps: [] as StepResult[], facts: {} as Record<string, unknown>, root: server.root };
      runs[kind] = run;
      try {
        await scenario(server, planId, run.steps, run.facts);
      } catch (error) {
        evidence[`${kind}Failure`] = normalize(String(error), server.root);
        evidence[`${kind}Steps`] = run.steps.map((x) => ({ ...x, content: x.content?.slice(0, 600), error: x.error?.slice(0, 600) }));
        throw error;
      } finally {
        await stopServer(server);
      }
      evidence[`${kind}PluginDirEntries`] = readdirSync(server.pluginDir).sort();
    }
    const shi = runs.shi!;
    evidence.shiori = {
      facts: shi.facts,
      steps: shi.steps.map((s) => ({ label: s.label, ok: s.ok, asks: s.asks, decision: s.decision, error: s.error ? normalize(s.error, shi.root).slice(0, 240) : undefined })),
    };
    // Smoke assertions (adapter).
    expect(shi.facts.nativeTools).toHaveLength(13);
    expect(shi.facts.readsChangedFiles).toEqual([]);
    const by = (label: string) => shi.steps.find((s) => s.label === label)!;
    for (const l of ["list", "read", "read-no-md", "inspect", "validate", "resume-4096", "resume-12000", "resume-64000", "doctor", "doctor-id", "compact-preview", "compact-mode-preview"]) {
      expect(by(l).ok).toBe(true);
      expect(by(l).asks).toBe(0);
    }
    expect(by("update-real-plan").ok).toBe(true);
    expect(by("update-real-plan").asks).toBe(1);
    expect((shi.facts.realPlanMutationChangedFiles as string[]).filter((f) => !f.endsWith(".md"))).toEqual([
      `.opencode/workplan/${planId}.history.jsonl`, `.opencode/workplan/${planId}.json`,
    ]);
    expect(shi.facts.realPlanMutationAskResources).toContain(`.opencode/workplan/${planId}.history.jsonl`);
    expect(by("update-extensions").ok).toBe(true);
    expect(by("update-extensions").asks).toBe(1);
    expect(shi.facts.extensionsChangedFiles).toEqual([
      ".opencode/workplan/parity-demo.evidence.json", ".opencode/workplan/parity-demo.history.jsonl",
      ".opencode/workplan/parity-demo.lanes.json", ".opencode/workplan/parity-demo.links.json",
    ]);
    for (const suffix of ["evidence.json", "history.jsonl", "lanes.json", "links.json"]) {
      expect(shi.facts.extensionsAskResources).toContain(`.opencode/workplan/parity-demo.${suffix}`);
    }
    for (const l of ["create", "update", "patch", "checkpoint", "compact-apply", "reset"]) expect(by(l).ok).toBe(true);
    expect(by("update-rejected").ok).toBe(false);
    expect(shi.facts.rejectChangedFiles).toEqual([]);
    expect(by("create-denied").ok).toBe(false);
    expect(by("create-denied").asks).toBe(0);
    expect(shi.facts.denyChangedFiles).toEqual([]);
    expect(shi.facts.cancelChangedFiles).toEqual([]);
    expect(shi.facts.cancelContinued).toBe(false);
    expect(shi.facts.sessionAbsoluteDeny).toBe("denied");
    expect(by("err-root-override").ok).toBe(false);
    const doctor = JSON.parse(by("doctor-final").content!);
    expect(doctor.runtimeFacts.permission.detail).toContain("host verified");
    expect(doctor.runtimeFacts.permission.detail).toContain("event stream ready");
    expect(doctor.runtimeFacts.host.writes).toBe("enabled");
    expect(doctor.runtimeFacts.host.opencodeVersion).toBe(shi.facts.runtimeVersion);
    expect(doctor.runtimeFacts.plugin.effective).toBe(true);

    if (runs.ref) {
      const ref = runs.ref;
      const diffs: Array<{ label: string; exact: boolean; normalizedEqual: boolean; ok: [boolean, boolean]; paths?: string[]; shi?: string; ref?: string }> = [];
      for (const s of shi.steps) {
        if (s.label === "read-no-notes" || s.label === "update-extensions") continue; // Shiori extensions have no reference equivalent.
        const r = ref.steps.find((x) => x.label === s.label)!;
        const a = normalize(s.ok ? s.content : s.error, shi.root);
        const b = normalize(r.ok ? r.content : r.error, ref.root);
        const exact = a === b && s.ok === r.ok;
        const normalizedEqual = deepNormalize(a) === deepNormalize(b) && s.ok === r.ok;
        let paths: string[] | undefined;
        if (!normalizedEqual && s.ok && r.ok) {
          try {
            paths = jsonDiff(JSON.parse(deepNormalize(a)), JSON.parse(deepNormalize(b)));
          } catch {}
        }
        diffs.push({ label: s.label, exact, normalizedEqual, ok: [s.ok, r.ok], ...(normalizedEqual ? {} : { paths, shi: deepNormalize(a).slice(0, 1500), ref: deepNormalize(b).slice(0, 1500) }) });
      }
      evidence.parity = {
        steps: diffs.length,
        exact: diffs.filter((d) => d.exact).map((d) => d.label),
        normalizedOnly: diffs.filter((d) => !d.exact && d.normalizedEqual).map((d) => d.label),
        different: diffs.filter((d) => !d.normalizedEqual),
        refFacts: ref.facts,
        refSteps: ref.steps.map((s) => ({ label: s.label, ok: s.ok, asks: s.asks, askResources: s.askResources })),
        shiAskResources: shi.steps.filter((s) => s.askResources).map((s) => ({ label: s.label, askResources: s.askResources })),
      };
      // Resulting artifacts after identical sequences (timestamps/hashes normalized).
      const files = (root: string) => {
        const m = new Map<string, string>();
        const walk = (dir: string) => {
          for (const name of readdirSync(dir)) {
            const p = join(dir, name);
            if (statSync(p).isDirectory()) walk(p);
            else m.set(relative(root, p).replace(/state-[0-9a-f]{12}-[0-9a-f]{12}/, "state-$ARCHIVE"), deepNormalize(normalize(readFileSync(p, "utf8"), root)));
          }
        };
        walk(join(root, ".opencode"));
        return m;
      };
      const fs = files(shi.root);
      const fr = files(ref.root);
      evidence.artifactParity = [...new Set([...fs.keys(), ...fr.keys()])].sort().map((k) => ({ path: k, equal: fs.get(k) === fr.get(k), shi: fs.has(k), ref: fr.has(k) }));
    }
  } finally {
    for (const s of servers) await stopServer(s);
    evidence.fixtureUnchanged = treeHash(fixture) === fixtureBefore;
    if (process.env.SHIORI_SMOKE_EVIDENCE) writeFileSync(process.env.SHIORI_SMOKE_EVIDENCE, `${JSON.stringify(evidence, null, 2)}\n`);
    if (process.env.SHIORI_SMOKE_KEEP !== "1") rmSync(scratchBase, { recursive: true, force: true });
  }
  expect(evidence.fixtureUnchanged).toBe(true);
}, 600_000);
