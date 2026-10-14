import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parse } from "yaml";
import { overlay, prepare, run, trackingConfig } from "../scripts/cpa-manager-plus";

const roots: string[] = [];
afterEach(() => { for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true }); });

const base = `# Keep this comment\nconfig-version: 8
management:
  allow-remote: false
  secret-key: ""
  disable-control-panel: true
access:
  api-keys: [existing-client-key]
routing:
  session-affinity: true
observability:
  logs:
    request-log: false
  usage:
    usage-statistics-enabled: false
`;

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "tsuna-cpamp-"));
  roots.push(root);
  mkdirSync(join(root, "config"));
  mkdirSync(join(root, ".runtime/proxy"), { recursive: true });
  writeFileSync(join(root, "config/proxy.json"), JSON.stringify({ projectDirectory: ".", composeFiles: ["compose.yml"], service: "gateway" }));
  writeFileSync(join(root, ".runtime/proxy/config.yaml"), base);
  return root;
}

describe("optional CPA-Manager-Plus", () => {
  test("enables only management and publishing while preserving client configuration", () => {
    const result = trackingConfig(base, "private-management-key");
    const config = parse(result);
    expect(result).toContain("# Keep this comment");
    expect(config.management).toEqual({ "allow-remote": true, "secret-key": "private-management-key", "disable-control-panel": true });
    expect(config.access["api-keys"]).toEqual(["existing-client-key"]);
    expect(config.routing["session-affinity"]).toBe(true);
    expect(config.observability.logs["request-log"]).toBe(false);
    expect(config.observability.usage["usage-statistics-enabled"]).toBe(true);
    expect(() => trackingConfig("config-version: 7\n", "key")).toThrow("v8");
    expect(() => trackingConfig("bad: [\n", "key")).toThrow("parse");
  });

  test("prepares private stable credentials without changing the base or portal edits", () => {
    const root = fixture();
    const directory = prepare(root);
    const managementKey = readFileSync(join(directory, "management-key"), "utf8");
    const adminKey = readFileSync(join(directory, "admin-key"), "utf8");
    expect(managementKey.trim()).toHaveLength(64);
    expect(adminKey).not.toBe(managementKey);
    expect(parse(readFileSync(join(directory, "gateway.yaml"), "utf8")).management["secret-key"]).toBe(managementKey.trim());
    writeFileSync(join(directory, "gateway.yaml"), "portal-edited-config\n");
    prepare(root);
    expect(readFileSync(join(directory, "gateway.yaml"), "utf8")).toBe("portal-edited-config\n");
    expect(readFileSync(join(directory, "management-key"), "utf8")).toBe(managementKey);
    expect(readFileSync(join(directory, "admin-key"), "utf8")).toBe(adminKey);
    expect(readFileSync(join(root, ".runtime/proxy/config.yaml"), "utf8")).toBe(base);
    expect(statSync(directory).mode & 0o777).toBe(0o700);
    for (const name of ["management-key", "admin-key", "gateway.yaml"]) expect(statSync(join(directory, name)).mode & 0o777).toBe(0o600);
    expect(JSON.parse(readFileSync(join(root, "config/proxy.json"), "utf8")).composeFiles).toEqual(["compose.yml"]);
  });

  test("refuses external gateway targets", () => {
    const root = fixture();
    writeFileSync(join(root, "config/proxy.json"), JSON.stringify({ projectDirectory: "~/kanata", composeFiles: ["compose.yml"], service: "cliproxy" }));
    expect(() => prepare(root)).toThrow("local compose.yml gateway");
  });

  test("start selects overlay only after success; stop restores default without deleting data", async () => {
    const root = fixture();
    const bin = join(root, "bin");
    mkdirSync(bin);
    const log = join(root, "docker-args");
    writeFileSync(join(bin, "docker"), '#!/bin/sh\nprintf "%s\\n" "$@" >> "$CPA_TEST_LOG"\nexit "$CPA_TEST_EXIT"\n', { mode: 0o755 });
    const saved = { PATH: process.env.PATH, CPA_TEST_LOG: process.env.CPA_TEST_LOG, CPA_TEST_EXIT: process.env.CPA_TEST_EXIT };
    try {
      process.env.PATH = `${bin}:${saved.PATH}`;
      process.env.CPA_TEST_LOG = log;
      process.env.CPA_TEST_EXIT = "1";
      await expect(run(root, "start")).rejects.toThrow("Docker Compose failed");
      expect(JSON.parse(readFileSync(join(root, "config/proxy.json"), "utf8")).composeFiles).toEqual(["compose.yml"]);
      process.env.CPA_TEST_EXIT = "0";
      await run(root, "start");
      expect(JSON.parse(readFileSync(join(root, "config/proxy.json"), "utf8")).composeFiles).toEqual(["compose.yml", overlay]);
      const directory = join(root, ".runtime/cpa-manager-plus");
      writeFileSync(join(directory, "data/history"), "history");
      await run(root, "stop");
      expect(JSON.parse(readFileSync(join(root, "config/proxy.json"), "utf8")).composeFiles).toEqual(["compose.yml"]);
      expect(readFileSync(join(directory, "data/history"), "utf8")).toBe("history");
      const args = readFileSync(log, "utf8");
      expect(args).toContain("--wait\n--wait-timeout\n60");
      expect(args).toContain("stop\ncpa-manager-plus");
      expect(args).not.toContain("down");
    } finally {
      for (const [name, value] of Object.entries(saved)) {
        if (value === undefined) delete process.env[name]; else process.env[name] = value;
      }
    }
  });
});
