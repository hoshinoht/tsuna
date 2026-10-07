import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, realpathSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { loadExtensions } from "@oh-my-pi/pi-coding-agent";

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
  for (const directory of cleanup) rmSync(directory, { recursive: true, force: true });
  cleanup = [];
});

function extensionPath(): string {
  return resolve(import.meta.dir, "../extensions/docs.ts");
}

describe("OMP document-tools extension", () => {
  test("loads through OMP and registers all document tools with strict schemas", async () => {
    const workspace = mkdtempSync(join(tmpdir(), "hoshi-omp-docs-"));
    cleanup.push(workspace);
    const loaded = await loadExtensions([extensionPath()], workspace);

    expect(loaded.errors).toEqual([]);
    expect(loaded.extensions).toHaveLength(1);
    const tools = loaded.extensions[0]!.tools;
    expect([...tools.keys()].sort()).toEqual([...EXPECTED].sort());
    expect(tools.get("docs_compile")!.definition.parameters.safeParse({}).success).toBe(false);
    expect(tools.get("docs_draft")!.definition.parameters.safeParse({ title: "Draft", preset: "eisvogel", unknown: true }).success).toBe(false);
    expect(tools.get("docs_draft")!.definition.parameters.safeParse({ title: "Draft", preset: "eisvogel" }).success).toBe(true);
  });

  test("uses the invocation cwd, returns OMP content, and forwards cancellation", async () => {
    const workspace = mkdtempSync(join(tmpdir(), "hoshi-omp-docs-"));
    cleanup.push(workspace);
    const loaded = await loadExtensions([extensionPath()], workspace);
    const draft = loaded.extensions[0]!.tools.get("docs_draft")!.definition;

    const result = await draft.execute(
      "draft-1",
      { title: "Trial document", preset: "eisvogel", initial_content: "# Trial" },
      undefined,
      undefined,
      { cwd: workspace } as never,
    );
    expect(result.content).toHaveLength(1);
    expect(result.content[0]).toEqual(expect.objectContaining({ type: "text" }));
    expect((result.content[0] as { text: string }).text).toContain("Draft created: Trial document");
    expect(result.details).toEqual({ workspace: realpathSync(workspace) });
    expect(existsSync(join(workspace, ".opencode", "docs-registry.json"))).toBe(true);

    const aborted = new AbortController();
    aborted.abort(new Error("cancelled by test"));
    await expect(draft.execute(
      "draft-cancelled",
      { title: "Cancelled", preset: "eisvogel" },
      aborted.signal,
      undefined,
      { cwd: workspace } as never,
    )).rejects.toThrow("cancelled by test");
  });
});
