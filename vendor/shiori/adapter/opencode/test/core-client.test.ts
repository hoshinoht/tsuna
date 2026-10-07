import { afterAll, describe, expect, it } from "bun:test";
import { chmodSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { CoreClient, ShioriError, TOOL_OPERATIONS, type HostContext } from "../src/core-client";
import { fingerprint, fixtureRoot, pause, shioriBin, tempRoot } from "./helpers";

const cleanup: string[] = [];
afterAll(() => {
  for (const p of cleanup) rmSync(p, { recursive: true, force: true });
});

function host(root: string, over: Partial<HostContext> = {}): HostContext {
  return { mode: "native", canonicalRoot: root, sessionID: "ses_1", agent: "orchestrator", messageID: "msg_1", callID: "call_1", ...over };
}

function alive(pid: number | undefined): boolean {
  if (!pid) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** A fake core: a bun script with the given body (handles stdin lines). */
function fakeCore(body: string): string {
  const dir = tempRoot("shiori-fake-core-");
  cleanup.push(dir);
  const path = join(dir, "fake-shiori");
  writeFileSync(path, `#!${process.execPath}\n${body}\n`);
  chmodSync(path, 0o755);
  return path;
}

const handshakeResult = (over: Record<string, unknown> = {}) => ({
  coreVersion: "fake",
  protocolVersion: 1,
  contractVersion: "v1",
  operations: [...TOOL_OPERATIONS, "shiori.handshake", "shiori.commit", "shiori.discard"],
  platform: { os: "darwin", arch: "arm64", supported: true },
  durability: { atomicRename: "supported", fileSync: "supported", directorySync: "supported" },
  writeSupported: true,
  limits: { maxFrameBytes: 16777216, maxResponseBytes: 67108864, maxNesting: 128 },
  ...over,
});

const fakePrelude = (hs: unknown, afterHandshake = "") => `
const rl = require("node:readline").createInterface({ input: process.stdin });
let first = true;
rl.on("line", (line) => {
  const f = JSON.parse(line);
  if (first) {
    first = false;
    process.stdout.write(JSON.stringify({ type: "response", protocolVersion: 1, requestId: f.requestId, ok: true, result: ${JSON.stringify(hs)} }) + "\\n");
    return;
  }
  ${afterHandshake}
});
`;

describe("CoreClient (stdio protocol, real shiori core)", () => {
  it("starts lazily, validates the handshake and returns the exact tool text", async () => {
    const root = fixtureRoot("full-valid");
    cleanup.push(root);
    const core = new CoreClient({ bin: shioriBin() });
    expect(core.pid).toBeUndefined();
    const res = await core.request("workplan_read", { id: "full-plan", includeMarkdown: false }, host(root));
    expect(alive(core.pid)).toBe(true);
    expect(core.handshake?.contractVersion).toBe("v1");
    const cli = Bun.spawnSync([shioriBin(), "read", "full-plan", "--no-markdown", "--json", "--root", root]);
    expect(res.result?.text).toBe(cli.stdout.toString().replace(/\n$/, ""));
    expect(res.hashes?.stateHash).toMatch(/^[0-9a-f]{64}$/);
    // Model-supplied identity/root fields are unknown native keys: rejected.
    for (const forged of [{ workspaceRoot: "/" }, { sessionID: "x" }, { agent: "orchestrator" }, { callID: "c" }]) {
      await expect(core.request("workplan_read", { id: "full-plan", ...forged }, host(root))).rejects.toThrow(/Unrecognized key/);
    }
    await core.dispose();
    expect(alive(core.pid)).toBe(false);
  });

  it("fails closed with actionable diagnostics for a missing, relative or unusable binary", async () => {
    const root = tempRoot();
    cleanup.push(root);
    for (const [bin, pattern] of [
      [undefined, /plugin option "bin" or the SHIORI_BIN/],
      ["shiori", /absolute path/],
      [join(root, "missing-shiori"), /missing or not executable/],
    ] as const) {
      const core = new CoreClient({ bin });
      const error = await core.request("workplan_list", {}, host(root)).catch((e) => e);
      expect(error).toBeInstanceOf(ShioriError);
      expect(error.errorClass).toBe("unsupported_capability");
      expect(error.message).toMatch(pattern);
    }
  });

  it("refuses an unsupported core handshake and terminates it", async () => {
    for (const bad of [{ contractVersion: "v2" }, { protocolVersion: 2 }, { operations: ["workplan_read"] }, { writeSupported: undefined }]) {
      const core = new CoreClient({ bin: fakeCore(fakePrelude(handshakeResult(bad))) });
      const error = await core.request("workplan_list", {}, host("/")).catch((e) => e);
      expect(error.errorClass).toBe("unsupported_protocol");
      expect(error.message).toMatch(/not supported by this adapter/);
      await pause(50);
      expect(core.pid).toBeUndefined();
    }
  });

  it("rejects oversized and too-deep frames locally without sending them", async () => {
    const root = fixtureRoot("minimal-valid");
    cleanup.push(root);
    const core = new CoreClient({ bin: shioriBin() });
    await core.request("workplan_list", {}, host(root));
    const pid = core.pid;
    const huge = await core.request("workplan_read", { id: "minimal", x: "a".repeat(17 * 1024 * 1024) }, host(root)).catch((e) => e);
    expect(huge.errorClass).toBe("invalid_input");
    expect(huge.message).toMatch(/frame limit; no request was sent/);
    let deep: unknown = 1;
    for (let i = 0; i < 140; i++) deep = [deep];
    const nested = await core.request("workplan_read", { id: "minimal", deep }, host(root)).catch((e) => e);
    expect(nested.errorClass).toBe("invalid_input");
    expect(nested.message).toMatch(/nested deeper/);
    // The connection is intact (the core never saw an invalid frame).
    expect(core.pid).toBe(pid);
    expect((await core.request("workplan_list", {}, host(root))).result?.text).toContain("workplans");
    await core.dispose();
  });

  it("bridges AbortSignal to cancel frames and settles cancelled reads immediately", async () => {
    const root = fixtureRoot("full-valid");
    cleanup.push(root);
    const core = new CoreClient({ bin: shioriBin() });
    const before = new AbortController();
    before.abort();
    await expect(core.request("workplan_list", {}, host(root), before.signal)).rejects.toThrow(/cancelled before it was sent/);
    expect(core.pid).toBeUndefined(); // nothing was started or sent
    const controller = new AbortController();
    const pending = core.request("workplan_list", {}, host(root), controller.signal);
    controller.abort();
    const outcome = await pending.catch((e) => e);
    // The abort settles the read as cancelled (the core may also have won
    // the race, but a response is never delivered twice).
    expect(outcome).toBeInstanceOf(ShioriError);
    expect(outcome.errorClass).toBe("cancelled");
    expect((await core.request("workplan_list", {}, host(root))).result?.text).toContain("workplans");
    await core.dispose();
  });

  it("fails in-flight requests closed on transport loss and never replays a prepared intent", async () => {
    const root = fixtureRoot("minimal-valid");
    cleanup.push(root);
    const core = new CoreClient({ bin: shioriBin() });
    const read = JSON.parse((await core.request("workplan_read", { id: "minimal", includeMarkdown: false }, host(root))).result!.text!);
    const prep = await core.request("workplan_update", { id: "minimal", expectedHash: read.stateHash, title: "never" }, host(root));
    expect(prep.prepared?.intentId).toBeString();
    const before = fingerprint(root);
    const firstPid = core.pid!;
    process.kill(firstPid, "SIGKILL");
    await pause(100);
    expect(core.pid).toBeUndefined();
    // The next call respawns and handshakes again; the old intent is gone.
    const replay = await core.request("shiori.commit", {
      intentId: prep.prepared!.intentId, intentDigest: prep.prepared!.intentDigest, capability: prep.prepared!.capability,
    }, host(root)).catch((e) => e);
    expect(replay.errorClass).toBe("stale_state");
    expect(core.pid).not.toBe(firstPid);
    expect(fingerprint(root)).toEqual(before);
    await core.dispose();
  });

  it("reports an in-flight commit lost with the child as outcome_uncertain and reads as cancelled", async () => {
    const core = new CoreClient({ bin: fakeCore(fakePrelude(handshakeResult(), "/* never answers */")) });
    await core.ensure();
    const commit = core.request("shiori.commit", { intentId: "i", intentDigest: "0".repeat(64), capability: "a".repeat(64) }, host("/")).catch((e) => e);
    const read = core.request("workplan_list", {}, host("/")).catch((e) => e);
    await pause(50);
    process.kill(core.pid!, "SIGKILL");
    const [c, r] = await Promise.all([commit, read]);
    expect(c.errorClass).toBe("outcome_uncertain");
    expect(c.message).toMatch(/workplan_doctor/);
    expect(r.errorClass).toBe("cancelled");
    expect(r.message).toMatch(/nothing is replayed/);
  });

  it("closes the connection on a malformed core frame", async () => {
    const core = new CoreClient({ bin: fakeCore(fakePrelude(handshakeResult(), `process.stdout.write("{not json\\n");`)) });
    const error = await core.request("workplan_list", {}, host("/")).catch((e) => e);
    expect(error.errorClass).toBe("cancelled");
    expect(error.message).toMatch(/malformed frame/);
  });

  it("respawns after the core's idle exit", async () => {
    const root = fixtureRoot("minimal-valid");
    cleanup.push(root);
    const core = new CoreClient({ bin: shioriBin(), args: ["serve", "--stdio", "--idle-timeout", "250ms"] });
    await core.request("workplan_list", {}, host(root));
    const first = core.pid;
    await pause(1_500);
    expect(alive(first)).toBe(false);
    expect(core.pid).toBeUndefined();
    await core.request("workplan_list", {}, host(root));
    expect(core.pid).not.toBe(first);
    await core.dispose();
  });

  it("unloads by closing stdin, then SIGTERM, then SIGKILL of its own child only", async () => {
    const stubborn = fakeCore(`${fakePrelude(handshakeResult())}
process.on("SIGTERM", () => {});
process.stdin.on("end", () => {});
setInterval(() => {}, 1000);`);
    const core = new CoreClient({ bin: stubborn, closeGraceMs: 100, termGraceMs: 200 });
    await core.ensure();
    const pid = core.pid!;
    const bystander = Bun.spawn(["sleep", "30"]);
    await core.dispose();
    expect(alive(pid)).toBe(false);
    expect(alive(bystander.pid)).toBe(true);
    bystander.kill();
    await expect(core.request("workplan_list", {}, host("/"))).rejects.toThrow(/unloaded/);
  });

  it("keeps secrets out of diagnostics", async () => {
    const root = fixtureRoot("minimal-valid");
    cleanup.push(root);
    const lines: string[] = [];
    const core = new CoreClient({ bin: shioriBin(), onDiagnostic: (l) => lines.push(l) });
    const read = JSON.parse((await core.request("workplan_read", { id: "minimal", includeMarkdown: false }, host(root))).result!.text!);
    const prep = await core.request("workplan_update", { id: "minimal", expectedHash: read.stateHash, title: "t" }, host(root));
    await core.request("shiori.discard", { intentId: prep.prepared!.intentId, intentDigest: prep.prepared!.intentDigest }, host(root));
    await pause(50);
    expect(lines.length).toBeGreaterThan(0);
    expect(core.stderrTail()).not.toContain(prep.prepared!.capability);
    expect(lines.join("\n")).not.toContain(prep.prepared!.capability);
    await core.dispose();
  });
});
