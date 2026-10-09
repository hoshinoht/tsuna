import { join, relative } from "node:path";
import { homedir } from "node:os";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { root, runtimeAgentDir } from "./rust-cli";

process.env.PI_CONFIG_DIR = relative(homedir(), join(root, ".runtime/omp"));
process.env.PI_CODING_AGENT_DIR = runtimeAgentDir;
process.env.OMP_PROFILE = "";
process.env.TSUNA_ROOT = root;
process.env.TSUNA_AGENT = "orchestrator";
process.env.TSUNA_PROXY_KEY = "offline-smoke-placeholder";
// This assertion exercises source-mode approval; auto mode permits ordinary external reads.
process.env.TSUNA_PERMISSION_MODE = "source";
const { Settings, discoverAuthStorage, ModelRegistry, createAgentSession, SessionManager } = await import("@oh-my-pi/pi-coding-agent");
const { closeModelCache } = await import("@oh-my-pi/pi-catalog/model-cache");
const { AgentStorage } = await import("@oh-my-pi/pi-coding-agent/session/agent-storage");
const { postmortem } = await import("@oh-my-pi/pi-utils");
const workspace = await mkdtemp(join(tmpdir(), "tsuna-smoke-"));
const settings = await Settings.loadIsolated({ cwd: workspace, agentDir: runtimeAgentDir });
const authStorage = await discoverAuthStorage(runtimeAgentDir);
const modelRegistry = new ModelRegistry(authStorage, join(runtimeAgentDir, "models.yml"), { settings });
const model = modelRegistry.find("cliproxy-anthropic", "claude-opus-5-5");
if (!model) throw new Error("Gateway model did not load");
console.log(`Model loaded: ${model.provider}/${model.id}; context=${model.contextWindow}; thinking=${model.thinking?.mode}`);
let session: Awaited<ReturnType<typeof createAgentSession>>["session"] | undefined;
try {
  const result = await createAgentSession({
    cwd: workspace, agentDir: runtimeAgentDir, settings, authStorage, modelRegistry, model,
    enableMCP: false, cacheWarming: false, sessionManager: SessionManager.inMemory(workspace),
  });
  session = result.session;
  const names = session.agent.state.tools.map(t => t.name);
  for (const name of ["docs_draft", "cache_guard_status", "reasoning_router_status", "subagent_list", "subagent_stop", "compress", "task"]) {
    if (!names.includes(name)) throw new Error(`Missing registered tool ${name}`);
  }
  if (!session.extensionRunner?.getRegisteredCommands().some(c => c.name === "tsuna-usage")) throw new Error("Quota command did not load");
  console.log(`OMP session loaded ${names.length} tools and the configured extensions.`);
  const { discoverAgents } = await import("@oh-my-pi/pi-coding-agent/task/discovery");
  const agents = await discoverAgents(workspace);
  const imported = Object.keys((await import("../config/roles.json")).default);
  for (const name of imported) if (!agents.agents.some(a => a.name === name)) throw new Error(`Missing agent ${name}`);
  console.log(`Discovered all ${imported.length} imported agents.`);
  const read = session.agent.state.tools.find(t => t.name === "read")!;
  await writeFile(join(workspace, "fixture.txt"), "tsuna smoke fixture");
  const output = await read.execute("smoke-read", { path: join(workspace, "fixture.txt") }, new AbortController().signal);
  console.log(`Read tool executes through native wrapper: ${output.content.length} content block(s).`);
  let blocked = false;
  try {
    await read.execute("smoke-external", { path: join(root, "config/roles.json") }, new AbortController().signal);
  } catch (error) { blocked = String(error).includes("permission requires"); }
  if (!blocked) throw new Error("External-directory approval did not fail closed in headless session");
  console.log("External-directory permission enforced by the actual OMP tool wrapper.");
} finally {
  try {
    await session?.dispose();
    session = undefined;
  } finally {
    // The SDK only closes auth storage it created; this script supplies its own.
    authStorage.close();
    settings.cancelPendingSaves();
    // loadIsolated opens the process-wide settings database; this one-shot owns it.
    AgentStorage.close();
    // ModelRegistry uses the process-wide cache for this runtime agent directory.
    closeModelCache();
    await rm(workspace, { recursive: true, force: true });
  }
}
console.log("OMP smoke cleanup complete.");
// Match the SDK CLI lifecycle: process-global native helpers end after session disposal.
await postmortem.quit(0);
