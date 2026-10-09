import { describe, expect, test } from "bun:test";
import roles from "../config/roles.json";
import permissions from "../config/permissions.json";
import commands from "../config/commands.json";
import { resolve } from "node:path";
import { loadExtensions } from "@oh-my-pi/pi-coding-agent";
import tsuna from "../extensions/tsuna";
import { compressToolResults } from "../lib/tsuna";

describe("OpenCode v2 import", () => {
  test("merged agent policies and command targets remain aligned", () => {
    expect(Object.keys(roles)).toHaveLength(16);
    // Fidelity is a project-local agent with a Tsuna tool policy, not a global model role.
    expect(Object.keys(permissions).sort()).toEqual([...Object.keys(roles), "fidelity"].sort());
    expect(Object.keys(commands)).toHaveLength(7);
    for (const command of Object.values(commands)) expect(Object.keys(roles)).toContain(command.agent);
  });
});

test("Tsuna registers canonical commands and the temporary Hoshi extension re-export stays loadable", async () => {
  for (const extension of ["../extensions/tsuna.ts", "../extensions/hoshi.ts"]) {
    const loaded = await loadExtensions([resolve(import.meta.dir, extension)], import.meta.dir);
    expect(loaded.errors).toEqual([]);
    const commands = [...loaded.extensions[0]!.commands.keys()];
    expect(commands).toContain("tsuna-agent");
    expect(commands.some(name => name.startsWith("hoshi-"))).toBe(false);
  }
});

test("Tsuna restores historical Hoshi role and compression entries while writing Tsuna entries", () => {
  const listeners = new Map<string, (...args: never[]) => unknown>();
  const written: Array<{ type: string; data: unknown }> = [];
  const statuses: Array<[string, string]> = [];
  const scalar = { optional: () => ({}), min: () => ({}) };
  const zod = { object: () => scalar, boolean: () => scalar, string: () => scalar, array: () => scalar };
  tsuna({
    zod,
    on: (name: string, handler: unknown) => listeners.set(name, handler as (...args: never[]) => unknown),
    appendEntry: (type: string, data: unknown) => written.push({ type, data }),
    registerCommand: () => {}, registerTool: () => {},
  } as never);
  const originalRole = process.env.TSUNA_AGENT;
  const originalExplicit = process.env.TSUNA_AGENT_EXPLICIT;
  try {
    delete process.env.TSUNA_AGENT;
    process.env.TSUNA_AGENT_EXPLICIT = "0";
    const branch = [
      { type: "custom", customType: "hoshi-role", data: { role: "scholar" } },
      { type: "custom", customType: "hoshi-compress", data: { toolCallIds: ["tool-1"], summary: "saved summary" } },
    ];
    listeners.get("session_start")!(undefined as never, {
      agent: { kind: "main" }, sessionManager: { getBranch: () => branch },
      ui: { setStatus: (key: string, value: string) => statuses.push([key, value]) },
    } as never);
    expect(process.env.TSUNA_AGENT as string | undefined).toBe("scholar");
    expect(written).toContainEqual({ type: "tsuna-role", data: { role: "scholar" } });
    expect(statuses.at(-1)).toEqual(["tsuna-role", "tsuna · scholar"]);
    const context = listeners.get("context")!({ messages: [{ role: "toolResult", toolCallId: "tool-1", content: [{ type: "text", text: "old output" }] }] } as never) as { messages: unknown[] };
    expect(JSON.stringify(context.messages)).toContain("saved summary");
  } finally {
    if (originalRole === undefined) delete process.env.TSUNA_AGENT; else process.env.TSUNA_AGENT = originalRole;
    if (originalExplicit === undefined) delete process.env.TSUNA_AGENT_EXPLICIT; else process.env.TSUNA_AGENT_EXPLICIT = originalExplicit;
  }
});

test("compression cannot remove user messages or tool-call pairing", () => {
  const input = [{ role: "user", content: "Do not push" }, { role: "assistant", content: [{ type: "toolCall", id: "a" }] }, { role: "toolResult", toolCallId: "a", content: [{ type: "text", text: "long output" }] }];
  const result = compressToolResults(input, new Map([["a", "short"]]));
  expect(result[0]).toBe(input[0]);
  expect(result[1]).toBe(input[1]);
  expect(result[2].toolCallId).toBe("a");
  expect(input[2].content).toEqual([{ type: "text", text: "long output" }]);
});
