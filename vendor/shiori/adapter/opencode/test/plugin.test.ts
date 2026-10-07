import { afterAll, describe, expect, it } from "bun:test";
import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";

import { CoreClient } from "../src/core-client";
import { compactionAdviceArgs, createPlugin, editResources, hostPolicy, hostTrust, SUPPORTED_HOST_VERSIONS, TOOL_NAMES } from "../src/plugin";
import registration from "../src/registration.json";
import { createFakeHost, toolContext, type FakeHostOptions } from "./fake-host";
import { fingerprint, pause, REPO_ROOT, seedPlan, shioriBin, tempRoot } from "./helpers";

const roots: string[] = [];
afterAll(() => {
  for (const r of roots) rmSync(r, { recursive: true, force: true });
});

async function setup(options: FakeHostOptions & { bin?: string | null; seed?: boolean; pluginOptions?: Record<string, unknown> } = {}) {
  const root = tempRoot("shiori-plugin-");
  roots.push(root);
  const seeded = options.seed === false ? undefined : seedPlan(root);
  const host = createFakeHost(root, options);
  let core: CoreClient | undefined;
  host.ctx.options = { ...(options.bin === null ? {} : { bin: options.bin ?? shioriBin() }), ...options.pluginOptions };
  const plugin = createPlugin({
    bridge: host.bridgeFactory(),
    env: {},
    core: (o) => (core = new CoreClient(o)),
  });
  const cleanup = await plugin.setup(host.ctx as never);
  return {
    root, host, seeded, cleanup: cleanup as () => Promise<void>,
    core: () => core!,
    async read(id = "native-demo") {
      return JSON.parse((await host.tool("workplan_read").execute({ id, includeMarkdown: false }, toolContext("tester"))).content);
    },
  };
}

const wp = (root: string, name: string) => join(root, ".opencode", "workplan", name);

describe("registration (identities, shapes, model-facing text)", () => {
  it("registers exactly the thirteen workplan identities with the reference descriptions and schemas", async () => {
    const t = await setup();
    try {
      expect(t.host.registered.map((x) => x.name)).toEqual([
        "workplan_create", "workplan_update", "workplan_inspect", "workplan_validate", "workplan_read", "workplan_list",
        "workplan_patch", "workplan_reset", "workplan_resume", "workplan_checkpoint", "workplan_compact", "workplan_doctor",
        "workplan_compact_preview",
      ]);
      expect(TOOL_NAMES).toHaveLength(13);
      for (const tool of t.host.registered) {
        const ref = registration.tools.find((x) => x.name === tool.name)!;
        expect(tool.description).toBe(ref.description);
        expect(tool.input).toEqual(ref.input);
        expect(tool.options).toEqual({ codemode: true });
        expect(tool.input.properties?.workspaceRoot).toBeUndefined();
      }
      expect(t.host.tool("workplan_compact_preview").input.properties.mode).toBeUndefined();
    } finally {
      await t.cleanup();
    }
  });

  it("keeps the argument shapes of the frozen v1 tool contracts", () => {
    for (const tool of registration.tools) {
      const v1 = JSON.parse(readFileSync(join(REPO_ROOT, "schema", "v1", "tools", `${tool.name}.input.schema.json`), "utf8"));
      expect(Object.keys((tool.input as any).properties ?? {}).sort()).toEqual(Object.keys(v1.properties ?? {}).sort());
      for (const key of (tool.input as any).required ?? []) expect(v1.required ?? []).toContain(key);
    }
  });

  it("differs from the reference registration only by the approved D.1, D.3, D.4, D.4.1, D.4.3, E1, X3, X4 and X5 changes", () => {
    const text = readFileSync(join(REPO_ROOT, "adapter", "opencode", "src", "registration.json"), "utf8");
    const reg = JSON.parse(text);
    // X5 (contracts §26): workplan_update planLinks.
    expect(reg.x5.additions).toEqual([{ tool: "workplan_update", property: "planLinks" }]);
    expect(reg.tools.find((t: any) => t.name === "workplan_update").input.properties.planLinks.items.properties.relation.enum).toEqual(["blocks", "blockedBy", "related"]);
    for (const a of reg.x5.additions) delete reg.tools.find((t: any) => t.name === a.tool).input.properties[a.property];
    delete reg.x5;
    // X4 (contracts §24): workplan_update rebase.
    expect(reg.x4.additions).toEqual([{ tool: "workplan_update", property: "rebase" }]);
    expect(reg.tools.find((t: any) => t.name === "workplan_update").input.properties.rebase.type).toBe("boolean");
    for (const a of reg.x4.additions) delete reg.tools.find((t: any) => t.name === a.tool).input.properties[a.property];
    delete reg.x4;
    // X3 (contracts §22): workplan_update lanes.
    expect(reg.x3.additions).toEqual([{ tool: "workplan_update", property: "lanes" }]);
    const lanesProp = reg.tools.find((t: any) => t.name === "workplan_update").input.properties.lanes;
    expect(lanesProp.items.properties.op.enum).toEqual(["propose", "transition", "claims"]);
    for (const a of reg.x3.additions) delete reg.tools.find((t: any) => t.name === a.tool).input.properties[a.property];
    delete reg.x3;
    // E1 (contracts §20): workplan_update recordEvidence.
    expect(reg.e1.additions).toEqual([{ tool: "workplan_update", property: "recordEvidence" }]);
    const rec = reg.tools.find((t: any) => t.name === "workplan_update").input.properties.recordEvidence;
    expect(rec.items.required).toEqual(["phaseId", "stepId", "command", "exitCode"]);
    expect(rec.items.additionalProperties).toBe(false);
    for (const a of reg.e1.additions) delete reg.tools.find((t: any) => t.name === a.tool).input.properties[a.property];
    delete reg.e1;
    // D.4.3 (contracts §18): workplan_checkpoint merge mode. Its changes are
    // undone first, so the D.4.1 checks below see the D.4.1 text.
    expect(reg.d4_3.additions).toEqual([{ tool: "workplan_checkpoint", property: "merge" }, { tool: "workplan_checkpoint", property: "appendValidation" }]);
    expect(reg.d4_3.changes.map((c: any) => `${c.tool}:${c.path.join(".")}`)).toEqual([
      "workplan_checkpoint:description", "workplan_checkpoint:input.properties.summary.description",
      "workplan_checkpoint:input.properties.nextAction.description", "workplan_checkpoint:input.required",
    ]);
    const checkpoint = reg.tools.find((t: any) => t.name === "workplan_checkpoint");
    expect(checkpoint.description).toContain("Without merge=true it replaces the whole checkpoint");
    expect(checkpoint.description).toContain("pass merge=true");
    expect(checkpoint.description).toContain("or read the current checkpoint first");
    expect(checkpoint.input.required).toEqual(["id"]);
    expect(checkpoint.input.properties.merge.type).toBe("boolean");
    expect(checkpoint.input.properties.appendValidation.anyOf).toEqual([{ type: "string" }, { type: "array", items: { type: "string" } }]);
    for (const a of reg.d4_3.additions) delete reg.tools.find((t: any) => t.name === a.tool).input.properties[a.property];
    for (const c of [...reg.d4_3.changes].reverse()) {
      let cur = reg.tools.find((t: any) => t.name === c.tool);
      for (const key of c.path.slice(0, -1)) cur = cur[key];
      cur[c.path[c.path.length - 1]] = c.previous;
    }
    delete reg.d4_3;
    expect(reg.d1.additions).toEqual([{ tool: "workplan_read", property: "includeNotes" }]);
    expect(reg.d3.additions).toEqual([{ tool: "workplan_reset", property: "previewToken" }, { tool: "workplan_reset", property: "confirmation" }]);
    expect(reg.d3.changes.map((c: any) => c.path.join("."))).toEqual(["description", "input.properties.mode.description", "input.properties.mode.enum", "input.properties.preserveNotes.description"]);
    expect(reg.d4.additions).toEqual([{ tool: "workplan_compact", property: "noteRollover" }, { tool: "workplan_compact_preview", property: "noteRollover" }]);
    for (const name of ["workplan_compact", "workplan_compact_preview"]) {
      const roll = reg.tools.find((t: any) => t.name === name).input.properties.noteRollover;
      expect(roll.type).toBe("object");
      expect(roll.additionalProperties).toBe(false);
      expect(Object.keys(roll.properties)).toEqual(["keepLatest", "pinNoteIndexes"]);
    }
    const read = reg.tools.find((t: any) => t.name === "workplan_read");
    expect(read.input.properties.includeNotes.type).toBe("boolean");
    const reset = reg.tools.find((t: any) => t.name === "workplan_reset");
    expect(reset.input.properties.mode.enum).toEqual(["draft", "markdown-only", "wipe"]);
    const mutating = ["workplan_create", "workplan_update", "workplan_patch", "workplan_reset", "workplan_checkpoint", "workplan_compact"];
    expect(reg.d4_1.changes.map((c: any) => c.tool)).toEqual(mutating);
    for (const c of reg.d4_1.changes) {
      expect(c.path).toEqual(["description"]);
      const now = reg.tools.find((t: any) => t.name === c.tool).description;
      expect(now.startsWith(`${c.previous} `)).toBe(true);
      expect(now.slice(c.previous.length + 1)).toMatch(/^(With overwrite=true, p|In apply mode, p|P)ass expectedHash = the stateHash from your latest read or successful write\.$/);
    }
    const { d1, d3, d4, d4_1, ...rest } = reg;
    for (const c of [...d4_1.changes].reverse()) rest.tools.find((t: any) => t.name === c.tool).description = c.previous;
    for (const a of [...d1.additions, ...d3.additions, ...d4.additions]) delete rest.tools.find((t: any) => t.name === a.tool).input.properties[a.property];
    for (const c of d3.changes) {
      let cur = rest.tools.find((t: any) => t.name === c.tool);
      for (const key of c.path.slice(0, -1)) cur = cur[key];
      cur[c.path[c.path.length - 1]] = c.reference;
    }
    const reference = JSON.stringify(rest, null, 2) + "\n";
    expect(createHash("sha256").update(reference).digest("hex")).toBe(d1.referenceSha256);
    expect(d1.referenceSha256).toBe("f4dd36c8a942cebbd26945e53ca580839c4eeee9ffaf4398a8b4397f477e6935");
  });

  it("degrades on an unverified host version: read-only tools work, writes are refused (D.3 item 7)", async () => {
    let bridgeStarted = false;
    const probeRoot = tempRoot();
    roots.push(probeRoot);
    const probe = createFakeHost(probeRoot, { hostVersion: "2.1.0" });
    const t = await setup({ hostVersion: "2.1.0" });
    try {
      // Every identity stays registered with the same text and shapes.
      expect(t.host.registered.map((x) => x.name)).toHaveLength(13);
      expect(SUPPORTED_HOST_VERSIONS).toContain("2.0.20");
      const before = fingerprint(t.root);
      const tester = toolContext("tester");
      // Read-only tools (read, list, inspect, validate, resume, doctor, compact_preview).
      const read = await t.read();
      expect(read.workplan.id).toBe("native-demo");
      expect(JSON.parse((await t.host.tool("workplan_list").execute({}, tester)).content).workplans[0].id).toBe("native-demo");
      expect(JSON.parse((await t.host.tool("workplan_inspect").execute({ id: "native-demo" }, tester)).content).workplan.id).toBe("native-demo");
      expect(JSON.parse((await t.host.tool("workplan_validate").execute({ id: "native-demo" }, tester)).content).valid).toBe(true);
      expect(JSON.parse((await t.host.tool("workplan_resume").execute({ id: "native-demo" }, tester)).content).checkpoint.current.stepTitle).toBe("Do next thing");
      expect(JSON.parse((await t.host.tool("workplan_compact_preview").execute({ id: "native-demo", archiveReason: "Preview only" }, tester)).content).mode).toBe("preview");
      const doctor = JSON.parse((await t.host.tool("workplan_doctor").execute({}, tester)).content);
      expect(doctor.runtimeFacts.host).toEqual({
        opencodeVersion: "2.1.0",
        verified: false,
        verifiedVersions: [...SUPPORTED_HOST_VERSIONS],
        writes: "disabled",
        detail: expect.stringContaining("Shiori adapter not verified for OpenCode 2.1.0; writes disabled — update Shiori"),
      });
      expect(doctor.runtimeFacts.permission.detail).toContain("Permission bridge not started");
      // Every mutating tool is refused before any core request or host prompt.
      const hash = read.stateHash;
      const writes: Array<[string, Record<string, unknown>, string]> = [
        ["workplan_create", { id: "other", goal: "g" }, "plan"],
        ["workplan_update", { id: "native-demo", expectedHash: hash, title: "x" }, "plan"],
        ["workplan_patch", { id: "native-demo", expectedHash: hash, patchText: "*** Begin Patch\n*** End Patch" }, "plan"],
        ["workplan_reset", { id: "native-demo", expectedHash: hash, mode: "wipe" }, "plan"],
        ["workplan_checkpoint", { id: "native-demo", expectedHash: hash, summary: "s", nextAction: "n" }, "orchestrator"],
        ["workplan_compact", { id: "native-demo", archiveReason: "r" }, "orchestrator"],
      ];
      for (const [name, input, agent] of writes) {
        const err = await t.host.tool(name).execute(input, toolContext(agent)).then(() => undefined, (e) => e);
        expect(err?.message).toStartWith("Shiori adapter not verified for OpenCode 2.1.0; writes disabled — update Shiori");
        expect(err?.errorClass).toBe("unsupported_capability");
      }
      expect(t.host.requests).toHaveLength(0);
      expect(fingerprint(t.root)).toEqual(before);
    } finally {
      await t.cleanup();
    }
    // The bridge is never started on an unverified host.
    const plugin = createPlugin({ bridge: async () => { bridgeStarted = true; throw new Error("must not start"); }, env: {} });
    const dispose = await plugin.setup(probe.ctx as never);
    expect(bridgeStarted).toBe(false);
    await (dispose as () => Promise<void>)();
  });

  it("reports the verified host in doctor (D.3 item 7)", async () => {
    const t = await setup();
    try {
      const doctor = JSON.parse((await t.host.tool("workplan_doctor").execute({}, toolContext("tester"))).content);
      expect(doctor.runtimeFacts.host).toMatchObject({ opencodeVersion: "2.0.20", verified: true, verifiedVersions: [...SUPPORTED_HOST_VERSIONS], writes: "enabled" });
    } finally {
      await t.cleanup();
    }
  });
});

describe("hostPolicy (contracts §19)", () => {
  const latest = SUPPORTED_HOST_VERSIONS[SUPPORTED_HOST_VERSIONS.length - 1];
  const [major, minor, patch] = latest.split(".").map(Number);
  const nextPatch = `${major}.${minor}.${patch + 1}`;

  it("classifies versions: verified, a later patch of a verified line, everything else", () => {
    for (const v of SUPPORTED_HOST_VERSIONS) {
      expect(hostTrust(v)).toBe("verified");
      expect(hostTrust(v, "exact")).toBe("verified");
    }
    expect(hostTrust(nextPatch)).toBe("patch");
    expect(hostTrust(`${major}.${minor}.${patch + 400}`)).toBe("patch");
    expect(hostTrust(nextPatch, "exact")).toBe("unverified");
    for (const v of [`${major}.${minor + 1}.0`, `${major + 1}.0.0`, `${major}.${minor}.3`, `${nextPatch}-beta.1`, `${nextPatch}+build`, `v${nextPatch}`, `0${nextPatch}`, "", undefined, null, 2]) {
      expect(hostTrust(v)).toBe("unverified");
    }
  });

  it("defaults to patch and rejects anything but patch or exact", () => {
    expect(hostPolicy(undefined)).toBe("patch");
    expect(hostPolicy("exact")).toBe("exact");
    for (const v of ["minor", "", null, true, ["patch"]]) expect(() => hostPolicy(v)).toThrow(/invalid plugin option "hostPolicy"/);
  });

  it("writes through the permission bridge on a later patch, and reports it as unverified in doctor", async () => {
    const t = await setup({ hostVersion: nextPatch });
    try {
      const doctor = JSON.parse((await t.host.tool("workplan_doctor").execute({}, toolContext("tester"))).content);
      expect(doctor.runtimeFacts.host).toEqual({
        opencodeVersion: nextPatch,
        verified: false,
        verifiedVersions: [...SUPPORTED_HOST_VERSIONS],
        writes: "enabled",
        detail: expect.stringContaining(`bun run verify-host ${nextPatch}`),
      });
      const hash = (await t.read()).stateHash;
      const out = JSON.parse((await t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, appendNotes: ["patch host"] }, toolContext("plan"))).content);
      expect(out.stateHash).not.toBe(hash);
      expect(t.host.requests).toHaveLength(1);
    } finally {
      await t.cleanup();
    }
  });

  it("still fails closed on a later patch when the host denies", async () => {
    const t = await setup({ hostVersion: nextPatch, effect: "deny" });
    try {
      const before = fingerprint(t.root);
      const hash = (await t.read()).stateHash;
      await expect(t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "denied" }, toolContext("plan"))).rejects.toThrow();
      expect(fingerprint(t.root)).toEqual(before);
    } finally {
      await t.cleanup();
    }
  });

  it("refuses writes on a later patch under hostPolicy exact, without starting the bridge", async () => {
    const t = await setup({ hostVersion: nextPatch, pluginOptions: { hostPolicy: "exact" } });
    try {
      const before = fingerprint(t.root);
      const hash = (await t.read()).stateHash;
      const err = await t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "x" }, toolContext("plan")).then(() => undefined, (e) => e);
      expect(err?.message).toStartWith(`Shiori adapter not verified for OpenCode ${nextPatch}; writes disabled — update Shiori`);
      expect(err?.message).toContain('hostPolicy "exact"');
      expect(err?.errorClass).toBe("unsupported_capability");
      expect(t.host.requests).toHaveLength(0);
      expect(fingerprint(t.root)).toEqual(before);
      const doctor = JSON.parse((await t.host.tool("workplan_doctor").execute({}, toolContext("tester"))).content);
      expect(doctor.runtimeFacts.host).toMatchObject({ verified: false, writes: "disabled" });
      expect(doctor.runtimeFacts.permission.detail).toContain("Permission bridge not started");
    } finally {
      await t.cleanup();
    }
  });

  it("fails plugin load on an invalid hostPolicy", async () => {
    await expect(setup({ pluginOptions: { hostPolicy: "minor" } })).rejects.toThrow(/invalid plugin option "hostPolicy"/);
  });
});

describe("P01 trusted identity, root and signal come only from ToolContext", () => {
  it("rejects model-supplied root/identity/authorization fields and uses the native caller", async () => {
    const t = await setup();
    try {
      const read = t.host.tool("workplan_read");
      for (const forged of [{ workspaceRoot: "/" }, { sessionID: "ses_forged" }, { agent: "orchestrator" }, { messageID: "m" }, { callID: "c" }, { approved: true }]) {
        await expect(read.execute({ id: "native-demo", ...forged }, toolContext("tester"))).rejects.toThrow(/^Invalid read input: \$: Unrecognized key/);
      }
      // The role check uses the trusted agent, never an input field.
      await expect(t.host.tool("workplan_checkpoint").execute({ id: "native-demo", agent: "orchestrator" }, toolContext("plan")))
        .rejects.toThrow(/Only the orchestrator may update a workplan checkpoint/);
      const hash = (await t.read()).stateHash;
      const ctx = toolContext("plan", new AbortController().signal, "p01");
      const done = t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "P01" }, ctx);
      await done;
      expect(t.host.requests).toHaveLength(1);
      expect(t.host.requests[0]).toMatchObject({
        sessionID: "ses_test", agent: "plan", action: "edit",
        source: { type: "tool", messageID: "msg_test_p01", id: "call_test_p01" },
      });
    } finally {
      await t.cleanup();
    }
  });

  it("refuses a write without the native invocation AbortSignal and changes nothing", async () => {
    const t = await setup();
    try {
      const before = fingerprint(t.root);
      const hash = (await t.read()).stateHash;
      await expect(t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "x" }, toolContext("plan", null)))
        .rejects.toThrow(/AbortSignal/);
      await pause(50);
      expect(fingerprint(t.root)).toEqual(before);
      expect(t.host.requests).toHaveLength(0);
    } finally {
      await t.cleanup();
    }
  });
});

describe("role matrix (spec 02 §5)", () => {
  it("keeps the reference role checks and messages", async () => {
    const t = await setup();
    try {
      await expect(t.host.tool("workplan_create").execute({}, toolContext("build"))).rejects.toThrow(/plan agent or orchestrator/);
      await expect(t.host.tool("workplan_checkpoint").execute({}, toolContext("plan"))).rejects.toThrow(/Only the orchestrator/);
      await expect(t.host.tool("workplan_update").execute({ id: "native-demo", recovery: "resume" }, toolContext("plan")))
        .rejects.toThrow(/Only the orchestrator may recover/);
      await expect(t.host.tool("workplan_compact").execute({ id: "native-demo", mode: "apply" }, toolContext("plan")))
        .rejects.toThrow(/Only the orchestrator may apply/);
      await expect(t.host.tool("workplan_compact_preview").execute({ id: "native-demo", archiveReason: "p", mode: "apply" }, toolContext("plan")))
        .rejects.toThrow(/compact_preview input/);
      await expect(t.host.tool("workplan_update").execute({ id: "native-demo", appendNotes: ["missing hash"] }, toolContext("orchestrator")))
        .rejects.toThrow(/expectedHash/);
      expect(t.host.requests).toHaveLength(0);
    } finally {
      await t.cleanup();
    }
  });
});

describe("read tools", () => {
  it("are pure, request no permission, and report host runtime facts in doctor", async () => {
    const t = await setup();
    try {
      const before = fingerprint(t.root);
      const tester = toolContext("tester");
      expect((await t.read()).workplan.id).toBe("native-demo");
      const resumed = JSON.parse((await t.host.tool("workplan_resume").execute({ id: "native-demo" }, tester)).content);
      expect(resumed.checkpoint.current.stepTitle).toBe("Do next thing");
      expect(JSON.stringify(resumed)).not.toContain("historical-note-must-not-be-returned");
      expect(JSON.parse((await t.host.tool("workplan_validate").execute({ id: "native-demo" }, tester)).content).valid).toBe(true);
      expect(JSON.parse((await t.host.tool("workplan_list").execute({}, tester)).content).workplans[0].id).toBe("native-demo");
      expect(JSON.parse((await t.host.tool("workplan_inspect").execute({ id: "native-demo" }, tester)).content).workplan.id).toBe("native-demo");
      expect(JSON.parse((await t.host.tool("workplan_compact_preview").execute({ id: "native-demo", archiveReason: "Preview only" }, tester)).content).mode).toBe("preview");
      expect(JSON.parse((await t.host.tool("workplan_compact").execute({ id: "native-demo", archiveReason: "Preview only" }, tester)).content).mode).toBe("preview");
      const doctor = JSON.parse((await t.host.tool("workplan_doctor").execute({}, tester)).content);
      expect(doctor.readOnly).toBe(true);
      expect(doctor.runtimeFacts.permission.status).toBe("unknown");
      expect(doctor.runtimeFacts.permission.agent).toBe("tester");
      expect(doctor.runtimeFacts.registrations.effective).toContain("workplan_compact_preview");
      expect(doctor.runtimeFacts.plugin).toMatchObject({ id: "workplan-tools", configured: true, effective: true, canonicalLocation: t.root });
      expect(doctor.runtimeFacts.builtinPlan).toEqual({ configured: null, effective: false });
      // Same detail format as the reference plugin (no adapter-only text).
      expect(doctor.runtimeFacts.permission.detail).toStartWith("Agent rules read; session rules read. Bridge client 2.0.20, RPC available");
      expect(doctor.runtimeFacts.permission.detail).not.toContain("Shiori");
      expect(fingerprint(t.root)).toEqual(before);
      expect(t.host.requests).toHaveLength(0);
    } finally {
      await t.cleanup();
    }
  });
});

describe("P02–P06 prepare → host authorization → commit", () => {
  it("asks the host for the exact canonical resources of the prepared intent, then commits it (allow)", async () => {
    const t = await setup();
    try {
      const hash = (await t.read()).stateHash;
      const out = JSON.parse((await t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, appendNotes: ["allowed"] }, toolContext("plan"))).content);
      expect(out.stateHash).not.toBe(hash);
      expect(readFileSync(t.seeded!.jsonPath, "utf8")).toContain("allowed");
      const [req] = t.host.requests;
      expect(req.resources).toEqual([...req.resources].sort());
      expect(new Set(req.resources).size).toBe(req.resources.length);
      const rel = req.resources.map((r) => relative(t.root, r));
      expect(rel).toContain(".opencode/workplan/native-demo.json");
      expect(rel).toContain(".opencode/workplan/native-demo.transaction.json");
      expect(rel).toContain(".opencode/workplan/.native-demo.lock");
      expect(rel).toContain(".opencode/workplan/.workspace-mutation.lock");
      expect(rel).toContain(".opencode/workplan");
      expect(rel).toContain(".opencode");
      for (const r of rel) expect(r.startsWith(".opencode")).toBe(true);
      for (const r of req.resources) expect(r).not.toMatch(/[*?[\]]/);
      expect(req.metadata).toMatchObject({ workplanToolsBridge: { version: 1, agent: "plan" } });
      expect(existsSync(wp(t.root, "native-demo.transaction.json"))).toBe(false);
    } finally {
      await t.cleanup();
    }
  });

  it("commits after a genuine user grant and changes nothing on a rejection", async () => {
    const t = await setup({ effect: "ask" });
    try {
      const hash = (await t.read()).stateHash;
      const op = t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "granted" }, toolContext("plan", new AbortController().signal, "g"));
      await t.host.nextAsk();
      await pause(20);
      t.host.reply("per_1", "once");
      expect(JSON.parse((await op).content).stateHash).toMatch(/^[0-9a-f]{64}$/);
      expect(readFileSync(t.seeded!.jsonPath, "utf8")).toContain("granted");

      const hash2 = (await t.read()).stateHash;
      const before = fingerprint(t.root);
      const rejected = t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash2, title: "rejected" }, toolContext("plan", new AbortController().signal, "r"));
      await t.host.nextAsk();
      await pause(20);
      t.host.reply("per_2", "reject");
      await expect(rejected).rejects.toThrow(/user rejected this exact workplan edit/);
      await pause(50);
      expect(fingerprint(t.root)).toEqual(before);
    } finally {
      await t.cleanup();
    }
  });

  it("changes nothing on a host deny", async () => {
    const t = await setup({ effect: "deny" });
    try {
      const hash = (await t.read()).stateHash;
      const before = fingerprint(t.root);
      await expect(t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "denied" }, toolContext("plan")))
        .rejects.toThrow(/denied this exact edit intent/);
      await pause(50);
      expect(fingerprint(t.root)).toEqual(before);
    } finally {
      await t.cleanup();
    }
  });

  it("cannot be revived by a late approval after cancellation", async () => {
    const t = await setup({ effect: "ask" });
    try {
      const hash = (await t.read()).stateHash;
      const before = fingerprint(t.root);
      const controller = new AbortController();
      let continued = false;
      const op = t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "late" }, toolContext("orchestrator", controller.signal, "late"))
        .then((r) => { continued = true; return r; });
      await t.host.nextAsk();
      controller.abort();
      await expect(op).rejects.toThrow(/cancelled/);
      t.host.reply("per_1", "once");
      await pause(150);
      expect(continued).toBe(false);
      expect(fingerprint(t.root)).toEqual(before);
      // The core expired the intent: nothing to commit, nothing to discard.
      expect(t.core().stderrTail()).toMatch(/cancelled: prepared intent expired|shiori\.discard/);
    } finally {
      await t.cleanup();
    }
  });

  it("ignores unrelated asks and replies, and fails closed when only those arrive", async () => {
    const unrelated: FakeHostOptions["unrelatedAsks"] = [
      { source: { type: "tool", messageID: "msg_other", id: "call_other" } },
      { metadata: { workplanToolsBridge: { version: 1, authorizationID: "foreign", agent: "plan" } } },
      { resources: ["/private/tmp/broader"] },
      { action: "read" },
      { location: "/private/tmp/other-project" },
    ];
    const t = await setup({ effect: "ask", unrelatedAsks: unrelated });
    try {
      const hash = (await t.read()).stateHash;
      const op = t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "correlated" }, toolContext("plan", new AbortController().signal, "c"));
      await t.host.nextAsk();
      await pause(20);
      t.host.reply("per_other", "once"); // unrelated reply: must not authorize
      await pause(30);
      expect(readFileSync(t.seeded!.jsonPath, "utf8")).not.toContain("correlated");
      t.host.reply("per_1", "once");
      await op;
      expect(readFileSync(t.seeded!.jsonPath, "utf8")).toContain("correlated");
    } finally {
      await t.cleanup();
    }
    for (const askOverride of unrelated) {
      const u = await setup({ effect: "ask", askOverride });
      try {
        const hash = (await u.read()).stateHash;
        const before = fingerprint(u.root);
        const controller = new AbortController();
        const op = u.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "x" }, toolContext("plan", controller.signal, "u"));
        await u.host.nextAsk();
        await pause(30);
        controller.abort();
        await expect(op).rejects.toThrow(/cancelled/);
        await pause(50);
        expect(fingerprint(u.root)).toEqual(before);
      } finally {
        await u.cleanup();
      }
    }
  });

  it("fails closed when the event stream is lost during authorization (P07)", async () => {
    const t = await setup({ effect: "ask", closeStreamOnAsk: true });
    try {
      const hash = (await t.read()).stateHash;
      const before = fingerprint(t.root);
      await expect(t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "lost" }, toolContext("plan")))
        .rejects.toThrow(/event stream disconnected|could not be correlated|not received/);
      await pause(50);
      expect(fingerprint(t.root)).toEqual(before);
    } finally {
      await t.cleanup();
    }
  });

  it("fails closed for a session outside the canonical project (P03 location)", async () => {
    const other = tempRoot("shiori-other-");
    roots.push(other);
    const t = await setup({ sessionDirectory: other });
    try {
      const hash = (await t.read()).stateHash;
      await expect(t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "x" }, toolContext("plan")))
        .rejects.toThrow(/not owned by this canonical project/);
      expect(t.host.requests).toHaveLength(0);
    } finally {
      await t.cleanup();
    }
  });

  it("rejects a receipt that does not match the exact intent", async () => {
    const root = tempRoot();
    roots.push(root);
    seedPlan(root);
    const host = createFakeHost(root);
    host.ctx.options = { bin: shioriBin() };
    const seen: string[][] = [];
    const plugin = createPlugin({
      env: {},
      bridge: async () => ({
        async authorizeEdit(intent: any) {
          seen.push([...intent.resources]);
          return { decision: "allow", via: "runtime-policy", authorizationID: "a", requestID: "r", sessionID: intent.sessionID, agent: intent.agent, source: { type: "tool", messageID: intent.messageID, id: intent.toolCallID }, resources: intent.resources.slice(1) } as any;
        },
        diagnostics: () => ({ clientVersion: "2.0.20", rpcRegistration: "available", serviceDiscovery: "unknown", hostBinding: "unknown", eventStream: "unknown", permissionDecision: "unknown" }) as any,
        async dispose() {},
      }),
    });
    const cleanup = await plugin.setup(host.ctx as never) as () => Promise<void>;
    try {
      const hash = JSON.parse((await host.tool("workplan_read").execute({ id: "native-demo" }, toolContext("t"))).content).stateHash;
      const before = fingerprint(root);
      await expect(host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "x" }, toolContext("plan")))
        .rejects.toThrow(/receipt no longer matches/);
      await pause(50);
      expect(fingerprint(root)).toEqual(before);
      expect(seen).toHaveLength(1);
    } finally {
      await cleanup();
    }
  });

  it("covers create, patch, reset, checkpoint, compact apply and recovery through the host", async () => {
    const t = await setup({ seed: false });
    try {
      const plan = toolContext("plan", new AbortController().signal, "c1");
      const created = JSON.parse((await t.host.tool("workplan_create").execute({
        id: "life", goal: "Lifecycle goal", status: "in_progress", notes: ["archive me"],
        phases: [
          { id: "done", title: "Done", status: "completed", steps: [{ id: "s", title: "S", action: "A", validation: "V", status: "completed" }] },
          { id: "active", title: "Active", status: "in_progress", steps: [{ id: "next", title: "Next", action: "Act", validation: "Check", status: "in_progress" }] },
        ],
      }, plan)).content);
      expect(created.stateHash).toMatch(/^[0-9a-f]{64}$/);
      const patchText = ["*** Begin Patch", "*** Update File: .opencode/workplan/life.md", "@@", "-Lifecycle goal", "+Lifecycle goal after patch", "*** End Patch"].join("\n");
      const patched = JSON.parse((await t.host.tool("workplan_patch").execute({ id: "life", expectedHash: created.stateHash, patchText }, toolContext("plan", new AbortController().signal, "c2"))).content);
      expect(typeof patched.output).toBe("string");
      expect(patched.metadata.stateHash).toMatch(/^[0-9a-f]{64}$/);
      const cp = JSON.parse((await t.host.tool("workplan_checkpoint").execute({ id: "life", expectedHash: patched.metadata.stateHash, summary: "Ready", nextAction: "Compact", phaseId: "active", stepId: "next" }, toolContext("orchestrator", new AbortController().signal, "c3"))).content);
      const preview = JSON.parse((await t.host.tool("workplan_compact_preview").execute({ id: "life", archiveReason: "tidy", completedPhaseIds: ["done"], noteIndexes: [0] }, toolContext("tester"))).content);
      expect(preview.stateHash).toBe(cp.stateHash);
      const applied = JSON.parse((await t.host.tool("workplan_compact").execute({ id: "life", mode: "apply", archiveReason: "tidy", completedPhaseIds: ["done"], noteIndexes: [0], confirmation: "ARCHIVE_SELECTED_HISTORY", previewToken: preview.previewToken, expectedHash: preview.stateHash }, toolContext("orchestrator", new AbortController().signal, "c4"))).content);
      expect(applied.compacted).toBe(true);
      expect(existsSync(applied.archivePath)).toBe(true);
      const beforeReset = JSON.parse((await t.host.tool("workplan_read").execute({ id: "life", includeMarkdown: false }, toolContext("t"))).content).stateHash;
      const reset = JSON.parse((await t.host.tool("workplan_reset").execute({ id: "life", expectedHash: beforeReset, mode: "draft", replaceMarkdown: true }, toolContext("plan", new AbortController().signal, "c5"))).content);
      expect(reset.reset).toBe(true);

      // Recovery of a journal left by an interrupted transaction.
      const jsonPath = wp(t.root, "life.json");
      const beforeBytes = readFileSync(jsonPath);
      const doc = JSON.parse(beforeBytes.toString("utf8"));
      doc.title = "Recovered journal title";
      const after = Buffer.from(`${JSON.stringify(doc, null, 2)}\n`);
      const sha = (b: Buffer) => createHash("sha256").update(b).digest("hex");
      writeFileSync(wp(t.root, "life.transaction.json"), `${JSON.stringify({
        schemaVersion: 1, transactionId: "fixture-tx", workplanId: "life", operation: "update", createdAt: "2026-09-30T00:00:00.000Z",
        targets: [{ path: ".opencode/workplan/life.json", beforeHash: sha(beforeBytes), afterHash: sha(after), beforeContent: beforeBytes.toString("base64"), afterContent: after.toString("base64"), mode: statSync(jsonPath).mode & 0o777 }],
      }, null, 2)}\n`);
      const pending = JSON.parse((await t.host.tool("workplan_read").execute({ id: "life" }, toolContext("t"))).content);
      expect(pending.recoveryRequired).toBe(true);
      await expect(t.host.tool("workplan_update").execute({ id: "life", recovery: "resume", expectedHash: pending.stateHash }, toolContext("plan"))).rejects.toThrow(/Only the orchestrator may recover/);
      const recovered = JSON.parse((await t.host.tool("workplan_update").execute({ id: "life", recovery: "resume", expectedHash: pending.stateHash }, toolContext("orchestrator", new AbortController().signal, "c6"))).content);
      expect(recovered.recovered).toBe(true);
      const recoveryAsk = t.host.requests.at(-1)!;
      expect(recoveryAsk.resources).toContain(wp(t.root, "life.transaction.json"));
      expect(JSON.parse(readFileSync(jsonPath, "utf8")).title).toBe("Recovered journal title");
      expect(existsSync(wp(t.root, "life.transaction.json"))).toBe(false);
      // No stage, lock or journal debris.
      expect(readdirSync(wp(t.root, "")).filter((n) => n.endsWith(".stage") || n.endsWith(".lock") || n.endsWith(".transaction.json"))).toEqual([]);
    } finally {
      await t.cleanup();
    }
  });

  it("asks for nothing when a mutation would not change any byte (D12)", async () => {
    const t = await setup({ seed: false });
    try {
      const created = JSON.parse((await t.host.tool("workplan_create").execute({ id: "same", goal: "g" }, toolContext("plan", new AbortController().signal, "s1"))).content);
      const asks = t.host.requests.length;
      const out = JSON.parse((await t.host.tool("workplan_reset").execute({ id: "same", expectedHash: created.stateHash, mode: "markdown-only" }, toolContext("plan", new AbortController().signal, "s2"))).content);
      expect(out.stateHash).toBe(created.stateHash);
      expect(t.host.requests.length).toBe(asks);
    } finally {
      await t.cleanup();
    }
  });
});

describe("P07 unsupported capability, transport loss and unload", () => {
  it("reports a missing core binary actionably and registers tools that fail closed", async () => {
    const t = await setup({ bin: null });
    try {
      await expect(t.host.tool("workplan_list").execute({}, toolContext("t"))).rejects.toThrow(/plugin option "bin" or the SHIORI_BIN/);
    } finally {
      await t.cleanup();
    }
  });

  it("fails a mutation closed when the core dies before commit and never replays it", async () => {
    const t = await setup({ effect: "ask" });
    try {
      const hash = (await t.read()).stateHash;
      const before = fingerprint(t.root);
      const op = t.host.tool("workplan_update").execute({ id: "native-demo", expectedHash: hash, title: "crash" }, toolContext("plan", new AbortController().signal, "x"));
      await t.host.nextAsk();
      process.kill(t.core().pid!, "SIGKILL");
      await pause(100);
      t.host.reply("per_1", "once");
      const error = await op.catch((e) => e);
      expect(error.message).toMatch(/not live on this connection|prepare the mutation again/);
      expect(fingerprint(t.root)).toEqual(before);
      // The next call runs on a new core.
      expect((await t.read()).stateHash).toBe(hash);
    } finally {
      await t.cleanup();
    }
  });

  it("terminates only its own child on unload", async () => {
    const t = await setup();
    await t.read();
    const pid = t.core().pid!;
    await t.cleanup();
    let alive = true;
    try {
      process.kill(pid, 0);
    } catch {
      alive = false;
    }
    expect(alive).toBe(false);
  });

  it("computes edit resources with parents inside the project only", () => {
    const res = editResources("/r", {
      resources: { readPaths: ["/r/a.md"], writePaths: ["/r/.opencode/workplan/x.json"], deletePaths: [], lockPaths: ["/r/.opencode/workplan/.x.lock"], stagingPaths: [], archivePaths: [] },
    } as any);
    expect(res).toEqual(["/r/.opencode", "/r/.opencode/workplan", "/r/.opencode/workplan/.x.lock", "/r/.opencode/workplan/x.json"]);
  });
});

describe("compactionAdvice plugin option (D.4.2, contracts §17 item 4)", () => {
  it("maps the option to the trusted serve flag and rejects anything else", () => {
    expect(compactionAdviceArgs(undefined)).toEqual([]);
    expect(compactionAdviceArgs({})).toEqual([]);
    expect(compactionAdviceArgs("off")).toEqual(["--compaction-advice", "off"]);
    expect(compactionAdviceArgs({ minSavingsKiB: 8, notes: 10, terminalPercent: 30, planKiB: 64, keepNotes: 5 }))
      .toEqual(["--compaction-advice", "min-savings-kib=8,notes=10,terminal-percent=30,plan-kib=64,keep-notes=5"]);
    for (const bad of [null, "on", 3, [], { notes: 0 }, { notes: 1.5 }, { notes: "5" }, { terminalPercent: 101 }, { keepNotes: 10001 }, { size: 1 }, { toString: 1 }]) {
      expect(() => compactionAdviceArgs(bad)).toThrow(/invalid plugin option "compactionAdvice"/);
    }
  });

  async function adviceSetup(compactionAdvice: unknown) {
    const root = tempRoot("shiori-advice-");
    roots.push(root);
    const { jsonPath } = seedPlan(root);
    const plan = JSON.parse(readFileSync(jsonPath, "utf8"));
    plan.notes = Array.from({ length: 80 }, (_, i) => `routine receipt ${i}: ${"x".repeat(100)}`);
    writeFileSync(jsonPath, `${JSON.stringify(plan, null, 2)}\n`);
    const host = createFakeHost(root);
    host.ctx.options = compactionAdvice === undefined ? { bin: shioriBin() } : { bin: shioriBin(), compactionAdvice };
    const args: (readonly string[] | undefined)[] = [];
    const plugin = createPlugin({ bridge: host.bridgeFactory(), env: {}, core: (o) => (args.push(o.args), new CoreClient(o)) });
    const cleanup = await plugin.setup(host.ctx as never) as () => Promise<void>;
    const doctor = JSON.parse((await host.tool("workplan_doctor").execute({ id: "native-demo" }, toolContext("tester"))).content);
    await cleanup();
    return { args, advice: doctor.plans[0].compactionRecommended };
  }

  it("passes the thresholds to the spawned core; absent keeps the defaults", async () => {
    const tuned = await adviceSetup({ minSavingsKiB: 1, notes: 5, keepNotes: 3 });
    expect(tuned.args).toEqual([["serve", "--stdio", "--compaction-advice", "min-savings-kib=1,notes=5,keep-notes=3"]]);
    expect(tuned.advice.thresholds).toEqual({ minSavingsBytes: 1024, notes: 5, terminalPercent: 25, planBytes: 196608, keepNotes: 3 });
    expect(tuned.advice.notes.eligible).toBe(77);
    const absent = await adviceSetup(undefined);
    expect(absent.args).toEqual([undefined]);
    expect(absent.advice).toBeUndefined();
    const off = await adviceSetup("off");
    expect(off.args).toEqual([["serve", "--stdio", "--compaction-advice", "off"]]);
    expect(off.advice).toBeUndefined();
  });

  it("fails plugin load on an invalid option before any core is started", async () => {
    const root = tempRoot("shiori-advice-bad-");
    roots.push(root);
    const host = createFakeHost(root);
    host.ctx.options = { bin: shioriBin(), compactionAdvice: { notes: -1 } };
    let started = 0;
    const plugin = createPlugin({ bridge: host.bridgeFactory(), env: {}, core: (o) => (started++, new CoreClient(o)) });
    await expect(plugin.setup(host.ctx as never)).rejects.toThrow(/compactionAdvice.*notes must be a positive integer/);
    expect(started).toBe(0);
    expect(host.registered).toHaveLength(0);
  });
});
