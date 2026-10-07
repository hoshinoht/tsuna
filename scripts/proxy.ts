import { chmod, mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { randomBytes } from "node:crypto";
import { join, resolve } from "node:path";

export const root = resolve(import.meta.dir, "..");
const gatewayPort = 18_317;

export interface ProxyPaths {
  directory: string;
  authDirectory: string;
  clientKey: string;
  config: string;
}

export function proxyPaths(repositoryRoot = root): ProxyPaths {
  const directory = join(repositoryRoot, ".runtime", "proxy");
  return {
    directory,
    authDirectory: join(directory, "auth"),
    clientKey: join(directory, "client-key"),
    config: join(directory, "config.yaml"),
  };
}

export function localhostPortMapping(port: number): string {
  if (!Number.isInteger(port) || port < 1 || port > 65_535) throw new Error(`Invalid localhost port: ${port}`);
  return `127.0.0.1:${port}:${port}`;
}

export function composeArgs(repositoryRoot: string, args: string[]): string[] {
  return ["compose", "-f", join(repositoryRoot, "compose.yml"), ...args];
}

export function loginArgs(repositoryRoot: string, provider: "claude" | "codex"): string[] {
  const callbackPort = provider === "claude" ? 54_545 : 1_455;
  return composeArgs(repositoryRoot, [
    "run", "--rm", "--no-deps", "-p", localhostPortMapping(callbackPort), "gateway", "./CLIProxyAPI", `--${provider}-login`, "--no-browser",
  ]);
}

export function configYaml(clientKey: string): string {
  return [
    "config-version: 8",
    "server:",
    '  host: "0.0.0.0"',
    "  port: 8317",
    "management:",
    "  allow-remote: false",
    '  secret-key: ""',
    "  disable-control-panel: true",
    "access:",
    "  api-keys:",
    `    - "${clientKey}"`,
    "routing:",
    '  strategy: "round-robin"',
    "  session-affinity: true",
    '  session-affinity-ttl: "1h"',
    "oauth:",
    '  auth-dir: "/root/.cli-proxy-api"',
    "observability:",
    "  logs:",
    "    debug: false",
    "    logging-to-file: false",
    "    request-log: false",
    "  usage:",
    "    usage-statistics-enabled: false",
    "",
  ].join("\n");
}

async function createPrivateFile(path: string, content: string): Promise<void> {
  try {
    await writeFile(path, content, { encoding: "utf8", mode: 0o600, flag: "wx" });
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "EEXIST") return;
    throw error;
  }
}

async function privateRegularFile(path: string): Promise<void> {
  const info = await stat(path);
  if (!info.isFile()) throw new Error(`Expected a file at ${path}`);
  await chmod(path, 0o600);
}

async function readClientKey(paths: ProxyPaths): Promise<string> {
  const key = (await readFile(paths.clientKey, "utf8")).trim();
  if (!key) throw new Error(`Client key at ${paths.clientKey} is empty; remove it and run 'proxy init'.`);
  return key;
}

export async function initializeProxy(repositoryRoot = root): Promise<ProxyPaths> {
  const paths = proxyPaths(repositoryRoot);
  await mkdir(paths.authDirectory, { recursive: true, mode: 0o700 });
  await chmod(paths.directory, 0o700);
  await chmod(paths.authDirectory, 0o700);

  await createPrivateFile(paths.clientKey, `${randomBytes(32).toString("base64url")}\n`);
  await privateRegularFile(paths.clientKey);
  const clientKey = await readClientKey(paths);
  await createPrivateFile(paths.config, configYaml(clientKey));
  await privateRegularFile(paths.config);

  return paths;
}

async function run(command: string, args: string[], inherit = false): Promise<void> {
  const process = Bun.spawn([command, ...args], {
    stdin: inherit ? "inherit" : "ignore",
    stdout: inherit ? "inherit" : "pipe",
    stderr: inherit ? "inherit" : "pipe",
  });
  const exitCode = await process.exited;
  if (exitCode === 0) return;
  if (inherit) throw new Error(`${command} exited with status ${exitCode}`);
  const stderr = await new Response(process.stderr as ReadableStream).text();
  throw new Error(`${command} exited with status ${exitCode}${stderr.trim() ? `: ${stderr.trim()}` : ""}`);
}

export async function fetchModels(repositoryRoot: string, signal = AbortSignal.timeout(5_000)): Promise<unknown> {
  const clientKey = await readClientKey(proxyPaths(repositoryRoot));
  const response = await fetch(`http://127.0.0.1:${gatewayPort}/v1/models`, {
    headers: { Authorization: `Bearer ${clientKey}` },
    signal,
  });
  if (!response.ok) throw new Error(`Gateway model listing failed: HTTP ${response.status}`);
  return response.json();
}

export async function waitForGateway(repositoryRoot: string, timeoutMs = 30_000): Promise<unknown> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    const remainingMs = deadline - Date.now();
    try {
      return await fetchModels(repositoryRoot, AbortSignal.timeout(Math.max(1, Math.min(5_000, remainingMs))));
    } catch (error) {
      lastError = error;
      await Bun.sleep(Math.min(250, Math.max(1, deadline - Date.now())));
    }
  }
  const message = lastError instanceof Error ? `: ${lastError.message}` : "";
  throw new Error(`CLIProxyAPI gateway did not become healthy within ${timeoutMs}ms${message}`);
}

function usage(): string {
  return "Usage: hoshi-omp proxy <init|start|stop|status|login claude|codex|models>";
}

async function main(args: string[]): Promise<void> {
  const [command, provider] = args;
  switch (command) {
    case "init":
      await initializeProxy();
      console.log("CLIProxyAPI gateway initialized.");
      return;
    case "start":
      await initializeProxy();
      await run("docker", composeArgs(root, ["up", "-d", "gateway"]), true);
      await waitForGateway(root);
      console.log("CLIProxyAPI gateway is reachable.");
      return;
    case "stop":
      await run("docker", composeArgs(root, ["stop", "gateway"]), true);
      return;
    case "status": {
      const models = await fetchModels(root);
      const count = Array.isArray((models as { data?: unknown }).data) ? (models as { data: unknown[] }).data.length : 0;
      console.log(`CLIProxyAPI gateway is reachable (${count} models).`);
      return;
    }
    case "models":
      console.log(JSON.stringify(await fetchModels(root), null, 2));
      return;
    case "login": {
      if (provider !== "claude" && provider !== "codex") throw new Error(usage());
      await initializeProxy();
      await run("docker", loginArgs(root, provider), true);
      return;
    }
    default:
      throw new Error(usage());
  }
}

if (import.meta.main) {
  await main(process.argv.slice(2)).catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
