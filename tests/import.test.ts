import { describe, expect, test } from "bun:test";
import { modelSelector, translateMcp } from "../scripts/import-opencode";
import roles from "../config/roles.json";
import permissions from "../config/permissions.json";
import commands from "../config/commands.json";
import { compressToolResults } from "../lib/hoshi";

describe("OpenCode v2 import", () => {
  test("all active agent policies and command targets survive", () => {
    expect(Object.keys(roles)).toHaveLength(17);
    expect(Object.keys(permissions).sort()).toEqual(Object.keys(roles).sort());
    expect(Object.keys(commands)).toHaveLength(7);
    for (const command of Object.values(commands)) expect(Object.keys(roles)).toContain(command.agent);
  });
  test("maps provider and effort without treating local -1m aliases as upstream IDs", () => {
    expect(modelSelector("openai/gpt-6.1-sol-1m#high")).toBe("cliproxy-openai/gpt-6.1-sol:high");
    expect(modelSelector("anthropic/claude-opus-5-5#medium")).toBe("cliproxy-anthropic/claude-opus-5-5:medium");
  });
  test("v2 disabled servers stay disabled and environment placeholders stay references", () => {
    const result = translateMcp({ test: { type: "local", command: ["{env:HOME}/.config/opencode/bin/test", "arg"], disabled: true, environment: { TOKEN: "{env:TOKEN}" }, timeout: { execution: 300000 } } });
    expect(result.test).toEqual({ type: "stdio", enabled: false, command: "${HOSHI_OMP_ROOT}/bin/test", args: ["arg"], env: { TOKEN: "${TOKEN}" }, timeout: 300000 });
  });
});
test("compression cannot remove user messages or tool-call pairing", () => {
  const input = [{ role: "user", content: "Do not push" }, { role: "assistant", content: [{ type: "toolCall", id: "a" }] }, { role: "toolResult", toolCallId: "a", content: [{ type: "text", text: "long output" }] }];
  const result = compressToolResults(input, new Map([["a", "short"]]));
  expect(result[0]).toBe(input[0]);
  expect(result[1]).toBe(input[1]);
  expect(result[2].toolCallId).toBe("a");
  expect(input[2].content).toEqual([{ type: "text", text: "long output" }]);
});
