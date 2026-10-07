import { cp, mkdir, readFile, symlink, lstat, writeFile, chmod } from "node:fs/promises";
import { join, resolve } from "node:path";
import { parse, stringify } from "yaml";
import { getBundledModel } from "@oh-my-pi/pi-catalog";
import { CURRENT_SETUP_VERSION } from "@oh-my-pi/pi-tui/setup/setup-version";
import roles from "../config/roles.json";

export const root = resolve(import.meta.dir, "..");
export const runtimeAgentDir = join(root, ".runtime/omp/agent");

export async function prepare() {
  // Generated binaries are intentionally excluded from Git; source pins travel with the repository.
  for (const [directory, output, entry] of [
    ["vendor/shiori", "shiori", "./cmd/shiori"],
    ["mcps/gofetch-mcp", "bin/gofetch", "./cmd/gofetch"],
    ["mcps/researcher-mcp", "bin/researcher-mcp", "./cmd/google-scholar-mcp"],
  ]) {
    if (await lstat(join(root, directory, output)).catch(() => null)) continue;
    console.log(`Building ${directory}…`);
    const build = Bun.spawn(["go", "build", "-trimpath", "-o", output, entry], {
      cwd: join(root, directory), env: { ...process.env, CGO_ENABLED: "0" },
      stdin: "ignore", stdout: "inherit", stderr: "inherit",
    });
    if (await build.exited !== 0) throw new Error(`Failed to build ${directory}`);
  }
  await mkdir(runtimeAgentDir, { recursive: true });
  for (const entry of ["agents", "skills", "prompts", "AGENTS.md"]) {
    const target = join(runtimeAgentDir, entry);
    if (!await lstat(target).catch(() => null)) await symlink(join(root, "agent", entry), target);
  }
  const config = parse(await readFile(join(root, "config/omp.yml"), "utf8"));
  // This profile is already configured; provider login belongs to CLIProxyAPI.
  config.setupVersion = CURRENT_SETUP_VERSION;
  config.extensions = ["permissions", "docs", "runtime", "hoshi", "usage"].map(name => join(root, "extensions", `${name}.ts`));
  config.modelRoles = Object.fromEntries(Object.entries(roles).map(([name, info]) => [name, info.model]));
  config.modelRoles.default = roles.build.model;
  config.modelRoles.plan = roles.plan.model;
  config.modelRoles.smol = roles.explore.model;
  config.modelRoles.slow = roles.oracle.model;
  config.modelRoles.tiny = "cliproxy-openai/gpt-6-luna:low";
  await writeFile(join(runtimeAgentDir, "config.yml"), stringify(config));
  const mcpText = (await readFile(join(root, "config/mcp.json"), "utf8"))
    .replaceAll("${HOSHI_OMP_ROOT}", root);
  await writeFile(join(runtimeAgentDir, "mcp.json"), mcpText);
  const groups: Record<string, any> = {};
  const sources = new Set(Object.values(roles).map(r => r.sourceModel.split("#")[0]));
  for (const source of sources) {
    const [provider, rawId] = source.split("/");
    const id = rawId.replace(/-1m$/, "");
    if (provider !== "openai" && provider !== "anthropic") continue;
    const family = provider === "openai" ? "openai-codex" : "anthropic";
    const model = getBundledModel(family, id);
    if (!model) throw new Error(`OMP catalog has no ${family}/${id}; refusing guessed limits`);
    const proxy = `cliproxy-${provider}`;
    groups[proxy] ??= {
      baseUrl: provider === "openai" ? "http://127.0.0.1:18317/v1" : "http://127.0.0.1:18317",
      api: provider === "openai" ? "openai-responses" : "anthropic-messages",
      apiKey: "HOSHI_PROXY_KEY",
      models: [],
    };
    if (groups[proxy].models.some((m: any) => m.id === id)) continue;
    groups[proxy].models.push({
      id, name: `${model.name} (CLIProxyAPI)`, reasoning: true, input: model.input,
      cost: model.cost, contextWindow: rawId.endsWith("-1m") ? 1_000_000 : model.contextWindow,
      maxTokens: model.maxTokens,
    });
  }
  await writeFile(join(runtimeAgentDir, "models.yml"), stringify({ providers: groups }));
  await chmod(join(root, "bin/hoshi-omp"), 0o755);
  console.log(`Prepared ${Object.keys(roles).length} agents, MCP definitions and pinned OMP profile.`);
}

if (import.meta.main) await prepare();
