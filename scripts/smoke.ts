import { join, relative } from "node:path";
import { homedir } from "node:os";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { root, runtimeAgentDir, prepare } from "./setup";

await prepare();
process.env.PI_CONFIG_DIR = relative(homedir(), join(root, ".runtime/omp"));
process.env.PI_CODING_AGENT_DIR = runtimeAgentDir;
process.env.OMP_PROFILE = "";
process.env.HOSHI_OMP_ROOT = root;
process.env.HOSHI_AGENT = "orchestrator";
process.env.HOSHI_PROXY_KEY = "offline-smoke-placeholder";
const { Settings, discoverAuthStorage, ModelRegistry, createAgentSession, SessionManager } = await import("@oh-my-pi/pi-coding-agent");
const workspace = await mkdtemp(join(tmpdir(), "hoshi-omp-smoke-"));
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
  if (!session.extensionRunner?.getRegisteredCommands().some(c => c.name === "hoshi-usage")) throw new Error("Quota command did not load");
  console.log(`OMP session loaded ${names.length} tools and all five extensions.`);
  const { discoverAgents } = await import("@oh-my-pi/pi-coding-agent/task/discovery");
  const agents = await discoverAgents(workspace);
  const imported = Object.keys((await import("../config/roles.json")).default);
  for (const name of imported) if (!agents.agents.some(a => a.name === name)) throw new Error(`Missing agent ${name}`);
  console.log(`Discovered all ${imported.length} imported agents.`);
  const read = session.agent.state.tools.find(t => t.name === "read")!;
  await writeFile(join(workspace, "fixture.txt"), "hoshi smoke fixture");
  const output = await read.execute("smoke-read", { path: join(workspace, "fixture.txt") }, new AbortController().signal);
  console.log(`Read tool executes through native wrapper: ${output.content.length} content block(s).`);
  let blocked = false;
  try {
    await read.execute("smoke-external", { path: join(root, "config/roles.json") }, new AbortController().signal);
  } catch (error) { blocked = String(error).includes("permission requires"); }
  if (!blocked) throw new Error("External-directory approval did not fail closed in headless session");
  console.log("External-directory permission enforced by the actual OMP tool wrapper.");
} finally {
  await session?.dispose();
  await rm(workspace, { recursive: true, force: true });
}
