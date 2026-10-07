import { cp, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { basename, join, resolve } from "node:path";
import { parse, stringify } from "yaml";

export function frontmatter(text: string): { meta: Record<string, any>; body: string } {
  const match = text.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/);
  if (!match) throw new Error("Missing Markdown frontmatter");
  return { meta: parse(match[1]), body: match[2] };
}

export function modelSelector(value: string, gateway = true): string {
  const [base, effort] = value.split("#");
  const slash = base.indexOf("/");
  const provider = base.slice(0, slash);
  const id = base.slice(slash + 1).replace(/-1m$/, "");
  const mapped = gateway
    ? ({ openai: "cliproxy-openai", anthropic: "cliproxy-anthropic" } as Record<string, string>)[provider] ?? provider
    : provider === "openai" ? "openai-codex" : provider;
  return `${mapped}/${id}${effort ? `:${effort}` : ""}`;
}

export function translateMcp(servers: Record<string, any>): Record<string, any> {
  return Object.fromEntries(Object.entries(servers).map(([name, entry]) => {
    const expand = (s: string) => s.replaceAll("{env:HOME}/.config/opencode", "${HOSHI_OMP_ROOT}")
      .replaceAll("$HOME/.config/opencode", "${HOSHI_OMP_ROOT}")
      .replace(/\{env:([^}]+)\}/g, "${$1}");
    const [command, ...args] = entry.command ?? [];
    const mapped: Record<string, any> = {
      type: entry.type === "local" ? "stdio" : "http",
      enabled: !entry.disabled,
      ...(command ? { command: expand(command), args: args.map(expand) } : { url: entry.url }),
      ...(entry.headers ? { headers: Object.fromEntries(Object.entries(entry.headers).map(([k, v]) => [k, expand(v as string)])) } : {}),
      ...(entry.environment ? { env: Object.fromEntries(Object.entries(entry.environment).map(([k, v]) => [k, expand(v as string)])) } : {}),
      ...(entry.timeout ? { timeout: typeof entry.timeout === "number" ? entry.timeout : entry.timeout.execution } : {}),
    };
    return [name, mapped];
  }));
}

function adaptProse(text: string): string {
  return text.replaceAll("~/.config/opencode", "~/.config/hoshi-omp")
    .replaceAll("OpenCode 2", "OMP (hoshi-omp)")
    .replaceAll("OpenCode loads", "OMP loads")
    .replaceAll("native subagent tool", "native task tool")
    .replaceAll("`subagent`", "`task`")
    .replaceAll("`shell`", "`bash`")
    .replaceAll("`question`", "`ask`")
    .replaceAll("webfetch", "read")
    .replaceAll("websearch", "web_search");
}

export async function importConfig(source: string, target: string) {
  const json = async (file: string) => Bun.JSONC.parse(await readFile(join(source, file), "utf8")) as any;
  const sourceConfig = await json("opencode.json");
  const permissions: Record<string, any[]> = {};
  const roles: Record<string, any> = {};
  const root = resolve(target);
  const save = async (path: string, data: string) => {
    await mkdir(resolve(root, path, ".."), { recursive: true });
    await writeFile(join(root, path), data);
  };
  const saveJson = (path: string, data: unknown) => save(path, `${JSON.stringify(data, null, 2)}\n`);
  const agentNames = (await readdir(join(source, "agents"))).filter(f => f.endsWith(".md")).sort();
  for (const file of agentNames) {
    const name = basename(file, ".md");
    const { meta, body } = frontmatter(await readFile(join(source, "agents", file), "utf8"));
    permissions[name] = meta.permissions;
    roles[name] = { description: meta.description, mode: meta.mode, model: modelSelector(meta.model), directModel: modelSelector(meta.model, false), sourceModel: meta.model, hidden: !!meta.hidden };
    // Tool filtering and resource permissions are enforced by the mandatory extension.
    // An explicit tool list prevents OMP from inheriting MCP servers, so leave it unset.
    const spawns = agentNames.map(f => basename(f, ".md")).filter(child => {
      let effect = "ask";
      for (const r of meta.permissions) {
        const matches = (s: string, value: string) => new RegExp(`^${s.replace(/[.+^${}()|[\]\\]/g, "\\$&").replaceAll("*", ".*").replaceAll("?", ".")}$`).test(value);
        if (matches(r.action, "subagent") && matches(r.resource, child)) effect = r.effect;
      }
      return effect === "allow";
    });
    const fm = { name, description: meta.description, model: `@${name}`, spawns: spawns.length ? spawns : false };
    await save(`agent/agents/${file}`, `---\n${stringify(fm)}---\n\n${adaptProse(body)}`);
    await save(`agent/prompts/${file}`, adaptProse(body));
  }
  await saveJson("config/roles.json", roles);
  await saveJson("config/permissions.json", permissions);
  const options = Object.fromEntries(sourceConfig.plugins.filter((p: any) => typeof p === "object").map((p: any) => [basename(p.package), p.options ?? {}]));
  await saveJson("config/plugin-options.json", options);
  await saveJson("config/mcp.json", { mcpServers: translateMcp(sourceConfig.mcp.servers) });
  const commands: Record<string, any> = {};
  for (const file of (await readdir(join(source, "commands"))).filter(f => f.endsWith(".md"))) {
    const { meta, body } = frontmatter(await readFile(join(source, "commands", file), "utf8"));
    commands[basename(file, ".md")] = { ...meta, body: adaptProse(body) };
    await save(`commands/${file}`, `---\n${stringify(meta)}---\n${adaptProse(body)}`);
  }
  await saveJson("config/commands.json", commands);
  await save("agent/AGENTS.md", `${adaptProse(await readFile(join(source, "AGENTS.md"), "utf8"))}\n\n## OMP integration\n\n- Use OMP's task tool with complete briefs; it replaces OpenCode subagent calls. Agent Hub and agent:// paths provide worker inspection and steering.\n- MCP tools have OMP names (mcp__server_tool); inspect the available catalog for the exact spelling.\n- The permission extension preserves the imported action/resource rules and Shiori role checks. A denied tool remains unavailable.\n- Durable plans stay in .opencode/workplan so existing Shiori artifacts remain compatible.\n- User-selected roles are managed by /hoshi-agent; only the user may change the primary role.\n`);
  await cp(join(source, "skills"), join(root, "agent/skills"), { recursive: true, filter: p => !p.includes("__pycache__") && !p.endsWith(".pyc") });
  async function rewriteMarkdown(dir: string) {
    for (const ent of await readdir(dir, { withFileTypes: true })) {
      const path = join(dir, ent.name);
      if (ent.isDirectory()) await rewriteMarkdown(path);
      else if (ent.name.endsWith(".md")) await writeFile(path, adaptProse(await readFile(path, "utf8")));
    }
  }
  await rewriteMarkdown(join(root, "agent/skills"));
  for (const path of ["LICENSE", "NOTICE", "scripts/check-workplan.ts", "scripts/agent-permissions.yaml", "dcp.jsonc", "cli.json"]) {
    await save(path === "LICENSE" || path === "NOTICE" ? path : `source-config/${path}`, await readFile(join(source, path), "utf8"));
  }
  await saveJson("config/import-manifest.json", {
    importedAt: "2026-10-07", sourceCommit: "f1586302b214c1ae84b81c7ca508cd1f20004428",
    includesWorkingTree: true, agents: agentNames.length, commands: Object.keys(commands).length,
    skills: (await readdir(join(root, "agent/skills"))).filter(name => existsSync(join(root, "agent/skills", name, "SKILL.md"))).length,
    mcp: Object.keys(sourceConfig.mcp.servers), plugins: sourceConfig.plugins,
    permissionsSource: "Actual agent frontmatter (active configuration); original generator YAML retained under source-config."
  });
}

if (import.meta.main) {
  const [source, target] = process.argv.slice(2);
  if (!source || !target) throw new Error("Usage: bun scripts/import-opencode.ts SOURCE TARGET");
  await importConfig(resolve(source), resolve(target));
}
