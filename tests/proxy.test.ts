import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { rustCli } from "./rust-cli-helper";

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
    const repository = mkdtempSync(join(tmpdir(), "tsuna-proxy-"));
    cleanup.push(repository);
    expect(rustCli(repository, ["proxy", "init"]).code).toBe(0);
    const paths = {
      directory: join(repository, ".runtime/proxy"),
      authDirectory: join(repository, ".runtime/proxy/auth"),
      clientKey: join(repository, ".runtime/proxy/client-key"),
      config: join(repository, ".runtime/proxy/config.yaml"),
    };
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

    expect(rustCli(repository, ["proxy", "init"]).code).toBe(0);
    expect(readFileSync(paths.clientKey, "utf8")).toBe(originalKey);
    expect(readFileSync(paths.config, "utf8")).toBe(originalConfig);
  });

  test("keeps Docker publication and OAuth callback publication on localhost", () => {
    const compose = readFileSync(join(import.meta.dir, "..", "compose.yml"), "utf8");
    expect(compose).toContain("name: hoshi-omp");
    expect(compose).toContain('"127.0.0.1:18317:8317"');
    expect(compose).not.toContain('"0.0.0.0:18317:8317"');
    expect(compose).toContain("eceasy/cli-proxy-api@sha256:2a7d31faf13e4f9a92112edd0d947faaed9fb1d529b5a825f9b829df98088634");
    // Rust unit tests exercise Docker/OAuth arguments and port validation.
  });
});
