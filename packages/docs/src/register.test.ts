import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, realpathSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setupDocsPlugin, TOOL_NAMES, type DocsPluginContext, type ToolRegistration } from "./register";
import { validateInput } from "./schemas";

const EXPECTED = [
  "docs_create",
  "docs_draft",
  "docs_list_drafts",
  "docs_delete_draft",
  "docs_compile",
  "docs_compile_latex",
  "docs_convert",
  "docs_presets_list",
  "docs_presets_show",
  "docs_templates_list",
  "docs_templates_install",
];

let cleanup: string[] = [];
afterEach(() => {
  for (const dir of cleanup) rmSync(dir, { recursive: true, force: true });
  cleanup = [];
});

async function register(directory: string) {
  const tools: ToolRegistration[] = [];
  let disposed = false;
  const ctx: DocsPluginContext = {
    location: { project: { directory } },
    tool: {
      transform: async (edit) => {
        edit({ add: (tool) => tools.push(tool) });
        return { dispose: () => void (disposed = true) };
      },
    },
  };
  const result = await setupDocsPlugin(ctx, {}, { user: join(directory, "no-user-dir") });
  return { tools, result, isDisposed: () => disposed };
}

describe("plugin registration", () => {
  test("registers exactly the 11 tools with strict JSON schemas", async () => {
    const dir = mkdtempSync(join(tmpdir(), "docs-register-"));
    cleanup.push(dir);
    const { tools, result, isDisposed } = await register(dir);
    expect(tools.map((t) => t.name).sort()).toEqual([...EXPECTED].sort());
    expect([...TOOL_NAMES].sort() as string[]).toEqual([...EXPECTED].sort());
    for (const tool of tools) {
      expect(tool.input.type).toBe("object");
      expect(tool.input.additionalProperties).toBe(false);
      expect(tool.description.length).toBeGreaterThan(10);
      for (const required of tool.input.required) expect(Object.keys(tool.input.properties)).toContain(required);
    }
    const byName = Object.fromEntries(tools.map((t) => [t.name, t]));
    expect(byName.docs_compile!.input.required).toEqual(["doc_id"]);
    expect(byName.docs_create!.input.required).toEqual(["input_path", "preset"]);
    expect(byName.docs_convert!.input.properties.output_format).toMatchObject({ enum: ["pdf", "docx", "odt", "markdown", "html", "latex"] });
    expect(byName.docs_templates_install!.input.properties.source).toMatchObject({ enum: ["eisvogel", "csl-ieee", "csl-apa", "csl-acm"] });
    expect(byName.docs_templates_install!.input.properties.force).toMatchObject({ type: "boolean" });
    expect(byName.docs_compile!.input.properties.citation_style).toMatchObject({ enum: ["default", "none", "ieee", "apa", "acm"] });
    await result.dispose();
    expect(isDisposed()).toBe(true);
  });

  test("execute returns {content} and uses the canonical project root", async () => {
    const real = mkdtempSync(join(tmpdir(), "docs-register-real-"));
    const link = `${real}-link`;
    symlinkSync(real, link);
    cleanup.push(real, link);
    const { tools, result } = await register(link);
    expect(result.service.roots.workspace).toBe(realpathSync(real));
    const list = tools.find((t) => t.name === "docs_list_drafts")!;
    expect(await list.execute({}, {})).toEqual({ content: "No active drafts. Create one with docs_draft." });
    const show = tools.find((t) => t.name === "docs_presets_show")!;
    expect((await show.execute({ preset_name: "eisvogel" }, {})).content).toContain("Preset: eisvogel");
  });

  test("unknown enum values, missing required and unknown arguments are rejected", async () => {
    const dir = mkdtempSync(join(tmpdir(), "docs-register-"));
    cleanup.push(dir);
    const { tools } = await register(dir);
    const install = tools.find((t) => t.name === "docs_templates_install")!;
    await expect(install.execute({ source: "ieee" }, {})).rejects.toThrow("'source' must be one of eisvogel, csl-ieee, csl-apa, csl-acm");
    const compile = tools.find((t) => t.name === "docs_compile")!;
    await expect(compile.execute({}, {})).rejects.toThrow("missing required argument 'doc_id'");
    await expect(compile.execute({ doc_id: "a-b-1", citation_style: "mla" }, {})).rejects.toThrow("'citation_style' must be one of");
    await expect(compile.execute({ doc_id: "a-b-1", bogus: 1 }, {})).rejects.toThrow("unknown argument 'bogus'");
    await expect(compile.execute({ doc_id: 5 }, {})).rejects.toThrow("'doc_id' must be a string");
  });

  test("validateInput normalises citation_style case and checks booleans", () => {
    expect(validateInput("docs_create", { input_path: "a.md", preset: "eisvogel", citation_style: "APA" })).toEqual({ input_path: "a.md", preset: "eisvogel", citation_style: "apa" });
    expect(() => validateInput("docs_templates_install", { source: "csl-apa", force: "yes" })).toThrow("'force' must be a boolean");
    expect(validateInput("docs_list_drafts", undefined)).toEqual({});
    expect(() => validateInput("docs_draft", { title: " ", preset: "x" })).toThrow("'title' must not be empty");
  });
});
