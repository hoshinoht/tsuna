import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { composeArgs, configYaml, initializeProxy, loginArgs, localhostPortMapping, proxyPaths } from "../scripts/proxy";

let cleanup: string[] = [];
afterEach(() => {
  for (const directory of cleanup) rmSync(directory, { recursive: true, force: true });
  cleanup = [];
});

function mode(path: string): number {
  return statSync(path).mode & 0o777;
}

describe("CLIProxyAPI gateway setup", () => {
  test("initializes private, persistent credentials without replacing them", async () => {
    const repository = mkdtempSync(join(tmpdir(), "hoshi-omp-proxy-"));
    cleanup.push(repository);
    const paths = await initializeProxy(repository);
    const originalKey = readFileSync(paths.clientKey, "utf8");
    const originalConfig = readFileSync(paths.config, "utf8");

    expect(existsSync(paths.authDirectory)).toBe(true);
    expect(mode(paths.directory)).toBe(0o700);
    expect(mode(paths.authDirectory)).toBe(0o700);
    expect(mode(paths.clientKey)).toBe(0o600);
    expect(mode(paths.config)).toBe(0o600);
    expect(originalKey.trim().length).toBeGreaterThanOrEqual(40);
    expect(originalConfig).toContain(`    - "${originalKey.trim()}"`);
    expect(originalConfig).toContain('  host: "0.0.0.0"');
    expect(originalConfig).toContain("  session-affinity: true");
    expect(originalConfig).toContain("  disable-control-panel: true");
    expect(originalConfig).toContain("    request-log: false");

    await initializeProxy(repository);
    expect(readFileSync(paths.clientKey, "utf8")).toBe(originalKey);
    expect(readFileSync(paths.config, "utf8")).toBe(originalConfig);
  });

  test("keeps Docker publication and OAuth callback publication on localhost", () => {
    const compose = readFileSync(join(import.meta.dir, "..", "compose.yml"), "utf8");
    expect(compose).toContain("name: hoshi-omp");
    expect(compose).toContain('"127.0.0.1:18317:8317"');
    expect(compose).not.toContain('"0.0.0.0:18317:8317"');
    expect(compose).toContain("eceasy/cli-proxy-api@sha256:2a7d31faf13e4f9a92112edd0d947faaed9fb1d529b5a825f9b829df98088634");
    expect(localhostPortMapping(18_317)).toBe("127.0.0.1:18317:18317");
    expect(localhostPortMapping(54_545)).toBe("127.0.0.1:54545:54545");
    expect(() => localhostPortMapping(0)).toThrow("Invalid localhost port");
    expect(composeArgs("/repo", ["up", "-d", "gateway"])).toEqual(["compose", "-f", "/repo/compose.yml", "up", "-d", "gateway"]);
    expect(loginArgs("/repo", "claude")).toEqual([
      "compose", "-f", "/repo/compose.yml", "run", "--rm", "--no-deps", "-p", "127.0.0.1:54545:54545", "gateway", "./CLIProxyAPI", "--claude-login", "--no-browser",
    ]);
    expect(loginArgs("/repo", "codex")).toContain("127.0.0.1:1455:1455");
    expect(proxyPaths("/repo")).toEqual({
      directory: "/repo/.runtime/proxy",
      authDirectory: "/repo/.runtime/proxy/auth",
      clientKey: "/repo/.runtime/proxy/client-key",
      config: "/repo/.runtime/proxy/config.yaml",
    });
    expect(configYaml("test-key")).toContain('  auth-dir: "/root/.cli-proxy-api"');
  });
});
