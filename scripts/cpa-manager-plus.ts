import { randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { parseDocument } from "yaml";

export const overlay = "integrations/cpa-manager-plus/compose.yml";
const portal = "http://127.0.0.1:18318";

function target(root: string) {
  const value = JSON.parse(readFileSync(resolve(root, "config/proxy.json"), "utf8"));
  if (value.projectDirectory !== "." || value.service !== "gateway" ||
      !Array.isArray(value.composeFiles) || value.composeFiles[0] !== "compose.yml" ||
      value.composeFiles.some((file: string) => file !== "compose.yml" && file !== overlay)) {
    throw new Error("This integration requires the local compose.yml gateway target; see integrations/cpa-manager-plus/README.md for external gateways.");
  }
  return value as { projectDirectory: string; service: string; composeFiles: string[] };
}

export function trackingConfig(source: string, managementKey: string): string {
  const document = parseDocument(source);
  if (document.errors.length) throw new Error("Cannot parse gateway YAML; no configuration was changed.");
  if (document.get("config-version") !== 8) throw new Error("This integration requires a CPA v8 configuration.");
  document.setIn(["management", "allow-remote"], true);
  document.setIn(["management", "secret-key"], managementKey);
  document.setIn(["management", "disable-control-panel"], true);
  document.setIn(["observability", "usage", "usage-statistics-enabled"], true);
  return document.toString();
}

export function prepare(root: string): string {
  target(root);
  const directory = resolve(root, ".runtime/cpa-manager-plus");
  const base = resolve(root, ".runtime/proxy/config.yaml");
  if (!existsSync(base)) throw new Error("Run tsuna proxy init first.");
  // Validate before creating any credentials or state.
  trackingConfig(readFileSync(base, "utf8"), "validation-only");
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  chmodSync(directory, 0o700);
  mkdirSync(resolve(directory, "data"), { recursive: true, mode: 0o700 });
  for (const name of ["management-key", "admin-key"]) {
    const path = resolve(directory, name);
    if (!existsSync(path)) writeFileSync(path, randomBytes(32).toString("hex") + "\n", { mode: 0o600, flag: "wx" });
    chmodSync(path, 0o600);
  }
  const config = resolve(directory, "gateway.yaml");
  if (!existsSync(config)) {
    writeFileSync(config, trackingConfig(readFileSync(base, "utf8"),
      readFileSync(resolve(directory, "management-key"), "utf8").trim()), { mode: 0o600, flag: "wx" });
  }
  chmodSync(config, 0o600);
  return directory;
}

async function docker(root: string, enabled: boolean, args: string[]) {
  const files = ["compose.yml", ...(enabled ? [overlay] : [])];
  const command = ["docker", "compose", "--project-directory", root,
    ...files.flatMap(file => ["-f", resolve(root, file)]), ...args];
  const child = Bun.spawn(command, { cwd: root, env: { ...process.env }, stdin: "ignore", stdout: "inherit", stderr: "inherit" });
  if (await child.exited !== 0) throw new Error("Docker Compose failed; integration state was retained.");
}

function selectOverlay(root: string, enabled: boolean) {
  const config = target(root);
  config.composeFiles = enabled ? ["compose.yml", overlay] : ["compose.yml"];
  writeFileSync(resolve(root, "config/proxy.json"), JSON.stringify(config, null, 2) + "\n");
}

export async function run(root: string, command: string) {
  switch (command) {
    case "init":
      prepare(root);
      console.log("Prepared CPA-Manager-Plus. Start with bun run integration:cpa start.");
      break;
    case "start":
      prepare(root);
      await docker(root, true, ["up", "-d", "--wait", "--wait-timeout", "60", "gateway", "cpa-manager-plus"]);
      selectOverlay(root, true);
      console.log(`CPA-Manager-Plus: ${portal}/management.html\nLogin key: .runtime/cpa-manager-plus/admin-key`);
      break;
    case "stop":
      target(root);
      await docker(root, true, ["stop", "cpa-manager-plus"]);
      await docker(root, false, ["up", "-d", "gateway"]);
      selectOverlay(root, false);
      console.log("Integration stopped; gateway uses its default configuration. History and keys retained.");
      break;
    case "status": {
      const response = await fetch(`${portal}/status`, {
        headers: { Authorization: `Bearer ${readFileSync(resolve(root, ".runtime/cpa-manager-plus/admin-key"), "utf8").trim()}` },
        signal: AbortSignal.timeout(5000),
      });
      if (!response.ok) throw new Error(`Manager status returned HTTP ${response.status}`);
      const status = await response.json() as { configured?: boolean; eventCount?: number; collector?: { lastError?: string } };
      console.log(JSON.stringify({ configured: status.configured, eventCount: status.eventCount,
        collectorError: Boolean(status.collector?.lastError) }, null, 2));
      break;
    }
    default:
      throw new Error("Usage: bun run integration:cpa <init|start|stop|status>");
  }
}

if (import.meta.main) {
  try {
    await run(resolve(import.meta.dir, ".."), process.argv[2] ?? "");
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
