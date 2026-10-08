import { afterEach, expect, test } from "bun:test";
import { mkdtemp, mkdir, readFile, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { createAgentSession, discoverAuthStorage, ModelRegistry, SessionManager, Settings } from "@oh-my-pi/pi-coding-agent";
import { getBundledModel } from "@oh-my-pi/pi-catalog";
import { cfgSkillsCustomDirectories } from "@oh-my-pi/pi-coding-agent/extensibility/settings";
import { initializeExtensions } from "@oh-my-pi/pi-coding-agent/modes/runtime-init";
import { loadProjectContext } from "../lib/project-context";

const cleanup: string[] = [];
afterEach(async () => { for (const path of cleanup.splice(0)) await rm(path, { recursive: true, force: true }); });

async function fixture() {
  const base = await realpath(await mkdtemp(join(tmpdir(), "hoshi-project-context-")));
  cleanup.push(base);
  const root = join(base, "repo");
  await mkdir(join(root, ".git"), { recursive: true });
  async function file(path: string, content: string) {
    await mkdir(resolve(path, ".."), { recursive: true });
    await writeFile(path, content);
  }
  return { base, root, file };
}

test("project discovery preserves ancestor order and excludes outside configuration and symlinks", async () => {
  const { base, root, file } = await fixture();
  const cwd = join(root, "web");
  await file(join(root, ".opencode/AGENTS.md"), "root instructions");
  await file(join(cwd, ".claude/CLAUDE.md"), "web instructions");
  await file(join(root, ".codex/skills/example/SKILL.md"), "fixture skill");
  await file(join(base, ".opencode/AGENTS.md"), "outside ancestor");
  await file(join(base, "outside.md"), "outside symlink");
  await mkdir(join(root, ".gemini"));
  await symlink(join(base, "outside.md"), join(root, ".gemini/GEMINI.md"));
  const context = await loadProjectContext(cwd);
  expect(context.instructions.map(item => item.content)).toEqual(["root instructions", "web instructions"]);
  expect(context.skillPaths).toEqual([join(root, ".codex/skills")]);
});

test("outside Git only the current directory supplies project instructions", async () => {
  const { base, file } = await fixture();
  const cwd = join(base, "plain");
  await file(join(base, ".claude/CLAUDE.md"), "ancestor");
  await file(join(cwd, ".codex/AGENTS.md"), "local");
  expect((await loadProjectContext(cwd)).instructions.map(item => item.content)).toEqual(["local"]);
});

test("the OMP session loads local skills, reads them by skill URL and refreshes instruction content", async () => {
  const { base, root, file } = await fixture();
  const agentDir = join(base, "profile");
  const configPath = join(agentDir, "config.yml");
  const config = `tools:\n  approvalMode: yolo\nmemory:\n  backend: off\ndisabledProviders: [opencode, codex, claude, agents, gemini]\nskills:\n  enableAgentsUser: false\n  enableClaudeUser: false\n  enableCodexUser: false\n  ignoredSkills: [ignored-fixture]\nextensions:\n  - ${resolve(import.meta.dir, "../extensions/permissions.ts")}\n  - ${resolve(import.meta.dir, "../extensions/project-context.ts")}\n`;
  await file(configPath, config);
  const instructionsPath = join(root, ".opencode/AGENTS.md");
  await file(instructionsPath, "project rule one");
  for (const [folder, name] of [[".opencode", "opencode-fixture"], [".codex", "codex-fixture"], [".claude", "claude-fixture"], [".agents", "agents-fixture"], [".gemini", "gemini-fixture"], [".codex", "ignored-fixture"]]) {
    await file(join(root, folder, "skills", name, "SKILL.md"), `---\nname: ${name}\ndescription: Fixture project skill\n---\n${name} body\n`);
  }
  // Agent definitions and hook scripts are outside the requested discovery scope.
  await file(join(root, ".claude/hooks/never-run.ts"), "throw new Error('foreign hook executed')");
  const settings = await Settings.loadIsolated({ cwd: root, agentDir });
  const authStorage = await discoverAuthStorage(agentDir);
  const modelRegistry = new ModelRegistry(authStorage, join(agentDir, "models.yml"), { settings });
  const { session } = await createAgentSession({ cwd: root, agentDir, settings, authStorage, modelRegistry,
    model: getBundledModel("anthropic", "claude-opus-5-5"), sessionManager: SessionManager.inMemory(root),
    enableMCP: false, cacheWarming: false, bindProcessState: false });
  const errors: unknown[] = [];
  session.extensionRunner!.onError(error => { errors.push(error); });
  try {
    await initializeExtensions(session, {
      reportSendError: (_action, error) => { errors.push(error); },
      reportRuntimeError: error => { errors.push(error); },
    });
    expect(errors).toEqual([]);
    const names = session.skills.map(skill => skill.name);
    for (const name of ["opencode-fixture", "codex-fixture", "claude-fixture", "agents-fixture", "gemini-fixture"]) expect(names).toContain(name);
    expect(names).not.toContain("ignored-fixture");
    const read = session.agent.state.tools.find(tool => tool.name === "read")!;
    const result = await read.execute("project-skill", { path: "skill://codex-fixture/SKILL.md" }, new AbortController().signal);
    expect(JSON.stringify(result.content)).toContain("codex-fixture body");
    const runner = session.extensionRunner!;
    const first = await runner.emitBeforeAgentStart("fixture", undefined, ["base policy"]);
    expect(first?.systemPrompt?.join("\n")).toContain("project rule one");
    await writeFile(instructionsPath, "project rule two");
    const second = await runner.emitBeforeAgentStart("fixture", undefined, ["base policy"]);
    expect(second?.systemPrompt?.join("\n")).toContain("project rule two");
    expect(second?.systemPrompt?.join("\n")).not.toContain("project rule one");
    await runner.emit({ type: "session_start" });
    expect(new Set(cfgSkillsCustomDirectories.get(settings)).size).toBe(cfgSkillsCustomDirectories.get(settings).length);
    expect(await readFile(configPath, "utf8")).toBe(config);
    expect(errors).toEqual([]);
  } finally { await session.dispose(); }
});
