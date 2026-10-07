// Shiori OpenCode adapter (stage D, spec 02). It registers exactly the
// thirteen existing workplan_* identities with the reference plugin's
// descriptions and input schemas, takes trusted identity, root and
// AbortSignal only from the native ToolContext, asks the executing host's
// permission engine for the exact canonical resources of every prepared
// intent, and commits only that intent over the private stdio protocol.
import { realpath } from "node:fs/promises";
import { dirname, isAbsolute, relative, resolve, sep } from "node:path";

import type { Plugin } from "@opencode/plugin";
import type { ToolContext } from "@opencode/plugin/promise/tool";

import { CoreClient, ShioriError, type CoreClientOptions, type HostContext, type PreparedIntent } from "./core-client";
import { registerNativePermissionBridge, type NativePermissionBridge, type NativePermissionBridgeContext } from "./permission-bridge";
import registration from "./registration.json" with { type: "json" };

/** Plugin identity kept from the reference plugin (one writer per root). */
export const PLUGIN_ID = "workplan-tools";
/**
 * Verified hosts (contracts §5.6): each passed `bun run verify-host`. Which
 * other versions may write depends on the `hostPolicy` option (contracts
 * §19); a host outside it keeps the read-only tools and every mutating tool
 * is refused (D.3, contracts §13 item 7).
 */
export const SUPPORTED_HOST_VERSIONS = ["2.0.19", "2.0.20", "2.0.21"] as const;

/**
 * `exact`: only the verified versions write. `patch` (default): a stable
 * X.Y.Z in the same X.Y line as a verified version, at or above the lowest
 * verified patch of that line, writes too; the permission bridge still
 * proves every authorization and fails closed.
 */
export const HOST_POLICIES = ["patch", "exact"] as const;
export type HostPolicy = (typeof HOST_POLICIES)[number];

/** How far a host version is trusted: tested, an untested patch accepted by policy, or not at all. */
export type HostTrust = "verified" | "patch" | "unverified";

export function hostPolicy(value: unknown): HostPolicy {
  if (value === undefined) return "patch";
  if ((HOST_POLICIES as readonly unknown[]).includes(value)) return value as HostPolicy;
  throw new Error(`Shiori adapter: invalid plugin option "hostPolicy": expected "patch" or "exact", got ${JSON.stringify(value)}.`);
}

function parseStable(version: string): [number, number, number] | undefined {
  const m = /^(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})$/.exec(version);
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : undefined;
}

export function hostTrust(version: unknown, policy: HostPolicy = "patch"): HostTrust {
  if (typeof version !== "string") return "unverified";
  if ((SUPPORTED_HOST_VERSIONS as readonly string[]).includes(version)) return "verified";
  if (policy !== "patch") return "unverified";
  const v = parseStable(version);
  if (!v) return "unverified";
  const floor = SUPPORTED_HOST_VERSIONS.map(parseStable)
    .filter((s): s is [number, number, number] => s !== undefined && s[0] === v[0] && s[1] === v[1])
    .reduce((min, s) => Math.min(min, s[2]), Infinity);
  return v[2] >= floor ? "patch" : "unverified";
}

export function hostVerified(version: unknown): boolean {
  return hostTrust(version, "exact") === "verified";
}

export const TOOL_NAMES = registration.tools.map((tool) => tool.name);

/** Tools that can write (schema x-shiori-mutating); refused on an unverified host. */
export const MUTATING_TOOLS: ReadonlySet<string> = new Set([
  "workplan_create", "workplan_update", "workplan_patch", "workplan_reset", "workplan_checkpoint", "workplan_compact",
]);

/** The refusal of a mutating tool on an unverified host (actionable, class unsupported_capability). */
export function unverifiedWriteMessage(version: unknown, policy: HostPolicy = "patch"): string {
  const scope = policy === "patch" ? "; hostPolicy \"patch\" also accepts later patches of a verified X.Y line" : "; hostPolicy \"exact\"";
  return `Shiori adapter not verified for OpenCode ${String(version)}; writes disabled — update Shiori (verified: ${SUPPORTED_HOST_VERSIONS.join(", ")}${scope}). ` +
    "Read-only workplan tools (read, list, inspect, validate, resume, doctor, compact_preview) still work; nothing was changed.";
}

function hostDetail(version: unknown, trust: HostTrust, policy: HostPolicy): string {
  if (trust === "verified") return "The Shiori adapter is verified for this OpenCode version; writes go through the host permission engine.";
  if (trust === "patch") {
    return `OpenCode ${String(version)} is an unverified patch of a verified line; hostPolicy "patch" enables writes, which still go through ` +
      `the fail-closed permission bridge. Run \`bun run verify-host ${String(version)}\` in adapter/opencode to verify it.`;
  }
  return unverifiedWriteMessage(version, policy);
}

const authoringTools = new Set(["workplan_create", "workplan_update", "workplan_patch", "workplan_reset"]);
const authoringAgents = new Set(["plan", "orchestrator"]);
const progressTitles: Record<string, string> = {
  workplan_create: "Create workplan",
  workplan_checkpoint: "Checkpoint workplan",
  workplan_compact: "Compact workplan",
  workplan_compact_preview: "Compact workplan",
  workplan_list: "List workplans",
  workplan_patch: "Patch workplan",
  workplan_read: "Read workplan",
  workplan_resume: "Resume workplan",
  workplan_validate: "Validate workplan",
};

type ToolEditor = Parameters<Parameters<Plugin.Context["tool"]["transform"]>[0]>[0];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Role matrix (spec 02 §5); supplements host policy, never widens it. */
export function assertRole(toolName: string, rawInput: unknown, agent: string): void {
  const input = isRecord(rawInput) ? rawInput : {};
  if (authoringTools.has(toolName) && !authoringAgents.has(agent)) {
    throw new Error("Only the plan agent or orchestrator may author workplan lifecycle changes");
  }
  if (toolName === "workplan_checkpoint" && agent !== "orchestrator") {
    throw new Error("Only the orchestrator may update a workplan checkpoint");
  }
  if (toolName === "workplan_update" && input.recovery !== undefined && agent !== "orchestrator") {
    throw new Error("Only the orchestrator may recover a workplan transaction");
  }
  if (toolName === "workplan_compact" && input.mode === "apply" && agent !== "orchestrator") {
    throw new Error("Only the orchestrator may apply workplan compaction");
  }
}

function within(root: string, path: string): boolean {
  const r = relative(root, path);
  return r !== "" && r !== ".." && !r.startsWith(`..${sep}`) && !isAbsolute(r);
}

/**
 * Every filesystem path a commit may create, replace, delete, lock or
 * stage, plus their parent directories inside the project: the resources
 * the host is asked to allow as "edit" (the reference requested the same
 * class of resources). Reads are preconditions only.
 */
export function editResources(root: string, prepared: PreparedIntent): string[] {
  const r = prepared.resources;
  const paths = [...r.writePaths, ...r.deletePaths, ...r.lockPaths, ...r.stagingPaths, ...r.archivePaths];
  const all = new Set<string>();
  for (const path of paths) {
    all.add(path);
    let dir = dirname(path);
    while (dir !== root && within(root, dir)) {
      all.add(dir);
      const parent = dirname(dir);
      if (parent === dir) break;
      dir = parent;
    }
  }
  return [...all].sort();
}

/**
 * Independent scope check of a prepared intent before any host request
 * (spec 02 §3 step 2): bound to this root and tool, every resource an
 * exact absolute path inside the project's .opencode directory.
 */
export function validatePrepared(root: string, toolName: string, prepared: PreparedIntent): void {
  if (!isRecord(prepared) || prepared.canonicalRoot !== root) {
    throw new Error("Native mutation intent does not match the active canonical project");
  }
  if (prepared.tool !== toolName || typeof prepared.intentId !== "string" || !/^[0-9a-f]{64}$/.test(prepared.intentDigest) ||
    !/^[0-9a-f]{64}$/.test(prepared.capability) || typeof prepared.workplanId !== "string" || !isRecord(prepared.resources)) {
    throw new Error("Native mutation intent is malformed; no permission was requested and nothing was changed");
  }
  const scope = resolve(root, ".opencode");
  for (const key of ["readPaths", "writePaths", "deletePaths", "lockPaths", "stagingPaths", "archivePaths"] as const) {
    const list = prepared.resources[key];
    if (!Array.isArray(list)) throw new Error("Native mutation intent is malformed; no permission was requested and nothing was changed");
    for (const path of list) {
      if (typeof path !== "string" || !isAbsolute(path) || resolve(path) !== path || path.includes("\0")) {
        throw new Error("Native mutation intent contains a non-canonical resource; no permission was requested and nothing was changed");
      }
      // Linked Markdown and specs may be read anywhere in the project;
      // every write-class resource must stay under .opencode.
      if (key === "readPaths" ? !within(root, path) : !within(scope, path)) {
        throw new Error("Native mutation intent reaches outside the workplan artifact scope; no permission was requested and nothing was changed");
      }
    }
  }
}

function sameResources(actual: readonly string[], expected: readonly string[]): boolean {
  const expectedSorted = [...expected].sort();
  return actual.length === expectedSorted.length && actual.every((path, index) => path === expectedSorted[index]);
}

function permissionDecision(value: string): "allow" | "deny" | "ask" | "unknown" {
  return value === "allow" || value === "deny" || value === "ask" ? value : "unknown";
}

async function collectDoctorRuntimeFacts(ctx: Plugin.Context, root: string, toolContext: ToolContext, bridge: NativePermissionBridge | undefined, hostVersion: unknown, trust: HostTrust, policy: HostPolicy) {
  let effectiveTools: string[] | null = null;
  let plugins: any[] | null = null;
  const notes: string[] = [];
  try {
    effectiveTools = (await ctx.tool.list()).map((tool) => tool.id).filter((name) => name.startsWith("workplan_"));
  } catch {
    notes.push("Effective tool catalog is unavailable through the public plugin context.");
  }
  try {
    plugins = (await ctx.plugin.list(undefined, { signal: toolContext.signal })).data as any[];
  } catch {
    notes.push("Active plugin facts are unavailable through the public plugin context.");
  }
  const ownPlugin = plugins?.find((plugin) => plugin.id === PLUGIN_ID);
  const builtinPlan = plugins?.find((plugin) => plugin.id === "opencode.plan");
  const rules: Array<{ resource: string; decision: "allow" | "deny" | "ask" | "unknown"; source: string }> = [];
  let agentRead = false;
  let sessionRead = false;
  try {
    const agent = (await ctx.agent.get({ agentID: toolContext.agent } as any, { signal: toolContext.signal } as any)).data as any;
    agentRead = true;
    for (const rule of agent.permissions) rules.push({ resource: rule.resource, decision: permissionDecision(rule.effect), source: `agent:${rule.action}` });
  } catch {
    notes.push("Caller agent permission facts are unavailable.");
  }
  try {
    const session = await ctx.session.get({ sessionID: toolContext.sessionID } as any, { signal: toolContext.signal } as any) as any;
    if (await realpath(session.location.directory) === root) {
      sessionRead = true;
      for (const rule of session.permissions ?? []) rules.push({ resource: rule.resource, decision: permissionDecision(rule.effect), source: `session:${rule.action}` });
    } else {
      notes.push("The current session location differs from the canonical project; session rules are unknown.");
    }
  } catch {
    notes.push("Current session permission facts are unavailable.");
  }
  const bridgeFacts = bridge?.diagnostics();
  notes.push("Effective permission remains unknown: public plugin APIs do not expose complete organization/hard-policy precedence.");
  if (effectiveTools === null) notes.push("Configured-only tool names are unknown; only a successful effective tool catalog is authoritative.");
  return {
    registrations: { effective: effectiveTools, configured: null },
    plugin: {
      id: PLUGIN_ID,
      configured: ownPlugin ? true : null,
      effective: plugins === null ? null : ownPlugin ? ownPlugin.state?.status === "active" : false,
      canonicalLocation: root,
    },
    permission: {
      status: "unknown",
      agent: toolContext.agent,
      sessionID: toolContext.sessionID,
      rules,
      detail: [
        `Agent rules ${agentRead ? "read" : "unknown"}; session rules ${sessionRead ? "read" : "unknown"}.`,
        bridgeFacts
          ? `Bridge client ${bridgeFacts.clientVersion}, RPC ${bridgeFacts.rpcRegistration}, service ${bridgeFacts.serviceDiscovery}, host ${bridgeFacts.hostBinding}, event stream ${bridgeFacts.eventStream}, runtime ${bridgeFacts.runtimeVersion ?? "unknown"}, last failure ${bridgeFacts.lastFailure ?? "none"}.`
          : "Permission bridge not started: the host version is outside hostPolicy, so writes are disabled.",
        ...notes,
      ].join(" "),
    },
    builtinPlan: {
      configured: null,
      effective: plugins === null ? null : Boolean(builtinPlan && builtinPlan.state?.status === "active"),
    },
    host: {
      opencodeVersion: typeof hostVersion === "string" ? hostVersion : null,
      verified: trust === "verified",
      verifiedVersions: [...SUPPORTED_HOST_VERSIONS],
      writes: trust === "unverified" ? "disabled" : "enabled",
      detail: hostDetail(hostVersion, trust, policy),
    },
  };
}

/** Keys of the `compactionAdvice` plugin option and their `--compaction-advice` spellings. */
const COMPACTION_ADVICE_KEYS: Readonly<Record<string, string>> = {
  minSavingsKiB: "min-savings-kib",
  notes: "notes",
  terminalPercent: "terminal-percent",
  planKiB: "plan-kib",
  keepNotes: "keep-notes",
};

/**
 * D.4.2 (contracts §17 item 4): the optional plugin option
 * `compactionAdvice` — `"off"` or `{minSavingsKiB, notes, terminalPercent,
 * planKiB, keepNotes}` (each optional, a positive integer; terminalPercent
 * at most 100, keepNotes at most 10000) — becomes the trusted
 * `--compaction-advice` flag of the spawned `shiori serve --stdio`
 * (protocol Options.Compaction). Absent (or `{}`): no flag, the core's
 * defaults. Anything else fails plugin load with an actionable message;
 * it is operator configuration and never model input.
 */
export function compactionAdviceArgs(value: unknown): string[] {
  if (value === undefined) return [];
  const bad = (why: string): never => {
    throw new Error(`Shiori adapter: invalid plugin option "compactionAdvice": ${why}. ` +
      'Use "off" or an object with any of minSavingsKiB, notes, terminalPercent, planKiB, keepNotes (positive integers; terminalPercent 1-100, keepNotes 1-10000).');
  };
  if (value === "off") return ["--compaction-advice", "off"];
  if (value === null || typeof value !== "object" || Array.isArray(value)) bad(`expected "off" or an object, got ${JSON.stringify(value)}`);
  const parts: string[] = [];
  for (const [key, n] of Object.entries(value as Record<string, unknown>)) {
    if (!Object.prototype.hasOwnProperty.call(COMPACTION_ADVICE_KEYS, key)) bad(`unknown key ${JSON.stringify(key)}`);
    if (typeof n !== "number" || !Number.isInteger(n) || n <= 0 || n > 2 ** 30) bad(`${key} must be a positive integer, got ${JSON.stringify(n)}`);
    if (key === "terminalPercent" && (n as number) > 100) bad("terminalPercent must be 1-100");
    if (key === "keepNotes" && (n as number) > 10000) bad("keepNotes must be 1-10000");
    parts.push(`${COMPACTION_ADVICE_KEYS[key]}=${n}`);
  }
  return parts.length === 0 ? [] : ["--compaction-advice", parts.join(",")];
}

export interface AdapterDeps {
  /** Core client factory (tests inject a fake transport). */
  readonly core?: (options: CoreClientOptions) => CoreClient;
  /** Permission bridge factory (tests inject fakes of the host domains). */
  readonly bridge?: (ctx: NativePermissionBridgeContext) => Promise<NativePermissionBridge>;
  readonly env?: NodeJS.ProcessEnv;
}

function progress(toolContext: ToolContext, toolName: string): void {
  const title = progressTitles[toolName];
  if (!title) return;
  try {
    void toolContext.progress({ status: title }).catch(() => {});
  } catch {
    // Progress is best-effort; it never authorizes or commits anything.
  }
}

function toolError(error: unknown): Error {
  if (error instanceof ShioriError) {
    const e = new Error(error.message) as Error & { errorClass?: string };
    e.errorClass = error.errorClass;
    return e;
  }
  return error instanceof Error ? error : new Error(String(error));
}

export function createPlugin(deps: AdapterDeps = {}) {
  return {
    id: PLUGIN_ID,
    async setup(ctx: Plugin.Context) {
      const hostVersion = ctx.app?.version;
      // D.3 (contracts §13 item 7, §19): a host outside hostPolicy keeps the
      // read-only tools and refuses every mutating tool before anything is
      // sent to the core. The permission bridge (which depends on host
      // internals) is not started, so no write can be authorized either.
      const policy = hostPolicy(ctx.options?.hostPolicy);
      const trust = hostTrust(hostVersion, policy);
      const writable = trust !== "unverified";
      const root = await realpath(ctx.location.project.directory);
      const env = deps.env ?? process.env;
      const bin = typeof ctx.options?.bin === "string" ? ctx.options.bin : env.SHIORI_BIN;
      const adviceArgs = compactionAdviceArgs(ctx.options?.compactionAdvice);
      const coreOptions: CoreClientOptions = {
        bin, env, clientName: "shiori-opencode",
        ...(adviceArgs.length > 0 ? { args: ["serve", "--stdio", ...adviceArgs] } : {}),
      };
      const core = deps.core ? deps.core(coreOptions) : new CoreClient(coreOptions);
      const bridge = writable
        ? await (deps.bridge ?? ((c) => registerNativePermissionBridge(c)))(ctx as unknown as NativePermissionBridgeContext)
        : undefined;

      const execute = async (toolName: string, rawInput: unknown, toolContext: ToolContext): Promise<{ content: string }> => {
        if (!writable && MUTATING_TOOLS.has(toolName)) {
          const e = new Error(unverifiedWriteMessage(hostVersion, policy)) as Error & { errorClass?: string };
          e.errorClass = "unsupported_capability";
          throw e;
        }
        assertRole(toolName, rawInput, toolContext.agent);
        const signal = toolContext.signal;
        const hostContext: HostContext = {
          mode: "native",
          canonicalRoot: root,
          sessionID: toolContext.sessionID,
          agent: toolContext.agent,
          messageID: toolContext.messageID,
          callID: toolContext.id,
          ...(toolName === "workplan_doctor" ? { runtimeFacts: await collectDoctorRuntimeFacts(ctx, root, toolContext, bridge, hostVersion, trust, policy) } : {}),
        };
        const input = rawInput === undefined ? {} : rawInput;
        if (!isRecord(input)) {
          // A non-object never reaches the core (its frame requires an object).
          const received = input === null ? "null" : Array.isArray(input) ? "array" : typeof input;
          throw new Error(`Invalid ${toolName.replace(/^workplan_/, "")} input: $: Invalid input: expected object, received ${received}`);
        }
        const prepareId = core.newRequestId();
        let response;
        try {
          response = await core.request(toolName, input, hostContext, signal, prepareId);
        } catch (error) {
          throw toolError(error);
        }
        if (response.result && typeof response.result.text === "string") {
          progress(toolContext, toolName);
          return { content: response.result.text };
        }
        const prepared = response.prepared;
        if (!prepared) throw new Error("The Shiori core returned neither a result nor a prepared intent; nothing was changed.");
        if (!bridge) throw new Error(unverifiedWriteMessage(hostVersion, policy)); // fail closed: no bridge, no write
        progress(toolContext, toolName);
        let committed = false;
        // An abort at any point expires the prepared intent in the core, so
        // a later approval can never commit it.
        const expire = () => core.cancel(prepareId);
        signal?.addEventListener?.("abort", expire, { once: true });
        try {
          validatePrepared(root, toolName, prepared);
          const resources = editResources(root, prepared);
          const trusted = {
            sessionID: toolContext.sessionID,
            agent: toolContext.agent,
            messageID: toolContext.messageID,
            toolCallID: toolContext.id,
            resources,
          };
          const receipt = await bridge.authorizeEdit(trusted, { signal });
          if (receipt.sessionID !== trusted.sessionID || receipt.agent !== trusted.agent ||
            receipt.source.messageID !== trusted.messageID || receipt.source.id !== trusted.toolCallID ||
            !sameResources(receipt.resources, resources)) {
            throw new Error("Native permission receipt no longer matches the trusted caller and exact write intent");
          }
          if (signal?.aborted) throw new ShioriError("cancelled", "The workplan permission request was cancelled; a later user reply cannot authorize this invocation.");
          committed = true;
          const result = await core.request("shiori.commit", {
            intentId: prepared.intentId,
            intentDigest: prepared.intentDigest,
            capability: prepared.capability,
          }, hostContext, signal);
          if (typeof result.result?.text !== "string") throw new Error("The Shiori core commit returned no result text");
          return { content: result.result.text };
        } catch (error) {
          if (!committed) {
            // Release the intent without side effects (best effort; the core
            // also expires it on cancel and disconnect).
            core.request("shiori.discard", { intentId: prepared.intentId, intentDigest: prepared.intentDigest }, hostContext).catch(() => {});
          }
          throw toolError(error);
        } finally {
          signal?.removeEventListener?.("abort", expire);
        }
      };

      let disposeTools: (() => Promise<void>) | undefined;
      try {
        const handle = await ctx.tool.transform((editor: ToolEditor) => {
          for (const tool of registration.tools) {
            editor.add({
              name: tool.name,
              description: tool.description,
              input: tool.input as any,
              options: { codemode: true },
              execute: (input: unknown, toolContext: ToolContext) => execute(tool.name, input, toolContext),
            } as any);
          }
        });
        disposeTools = () => handle.dispose();
      } catch (error) {
        await bridge?.dispose();
        await core.dispose();
        throw error;
      }
      return async () => {
        try {
          await disposeTools?.();
        } finally {
          try {
            await bridge?.dispose();
          } finally {
            await core.dispose();
          }
        }
      };
    },
  };
}
