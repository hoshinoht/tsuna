import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { authorsYaml, DOC_ID_PATTERN, DRAFT_WORDS, generateDocId, isValidDocId, readRegistry } from "./drafts";
import { DocsService } from "./service";
import { makeFixture, type Fixture } from "./testing";

let fx: Fixture;
afterEach(() => fx?.cleanup());

const FIXED = new Date("2026-03-04T23:30:00.000Z");

function service(random: () => number = () => 0.5) {
  return new DocsService(fx.roots, { now: () => FIXED, random });
}

function idFrom(output: string): string {
  return /Document ID: (\S+)/.exec(output)![1]!;
}

describe("doc IDs", () => {
  test("word lists are lower-case letters only", () => {
    for (const word of [...DRAFT_WORDS.adjectives, ...DRAFT_WORDS.nouns]) expect(word).toMatch(/^[a-z]+$/);
  });

  test("generated IDs match the validation regex and avoid collisions", () => {
    const taken = new Set<string>();
    for (let i = 0; i < 200; i++) {
      const id = generateDocId(taken);
      expect(id).toMatch(DOC_ID_PATTERN);
      expect(taken.has(id)).toBe(false);
      taken.add(id);
    }
  });

  test("falls back to a timestamp suffix after 100 colliding attempts", () => {
    const fixed = generateDocId(new Set(), () => 0);
    expect(generateDocId(new Set([fixed]), () => 0, () => 1234)).toBe(`${fixed}-1234`);
    expect(isValidDocId(`${fixed}-1234`)).toBe(true);
  });

  test("the 100th attempt is used when it is unique", () => {
    let calls = 0;
    // Each ID consumes 3 random draws; the first 99 IDs collide, the 100th differs.
    const random = () => (calls++ < 99 * 3 ? 0 : 0.99);
    const collide = generateDocId(new Set(), () => 0);
    const id = generateDocId(new Set([collide]), random, () => 1);
    expect(id).not.toBe(collide);
    expect(id.endsWith("-1")).toBe(false);
  });

  test("validation blocks traversal and odd shapes", () => {
    for (const bad of ["../x", "a-b", "Misty-harbor-123", "misty-harbor-12a", "misty/harbor-1", ""]) expect(isValidDocId(bad)).toBe(false);
    expect(isValidDocId("misty-harbor-123")).toBe(true);
  });
});

describe("docs_draft", () => {
  test("creates files, registry entry and exact output", () => {
    fx = makeFixture();
    const out = service().draft({ title: 'Say "hi"\nthere', preset: "school-report" });
    const id = idFrom(out);
    const dir = join(fx.roots.workspace, ".opencode", "docs", id);
    expect(out).toBe(
      [
        `Draft created: Say "hi"\nthere`,
        "",
        `Document ID: ${id}`,
        `Draft path: ${dir}/draft.md`,
        `Bibliography path: ${dir}/refs.bib`,
        "Preset: school-report",
        "",
        "Next steps:",
        `1. Edit the draft: ${dir}/draft.md`,
        `2. Add references if needed: ${dir}/refs.bib`,
        `3. Compile when ready: docs_compile doc_id="${id}"`,
      ].join("\n"),
    );
    expect(readFileSync(join(dir, "draft.md"), "utf8")).toBe('---\ntitle: "Say \\"hi\\" there"\ndate: "2026-03-04"\n---\n\n');
    expect(readFileSync(join(dir, "refs.bib"), "utf8")).toBe("");
    const meta = JSON.parse(readFileSync(join(dir, "meta.json"), "utf8"));
    expect(meta).toEqual({ title: 'Say "hi"\nthere', preset: "school-report", created_at: FIXED.toISOString(), last_modified: FIXED.toISOString(), status: "draft" });
    expect(readRegistry(join(fx.roots.workspace, ".opencode", "docs-registry.json")).drafts[id]).toEqual(meta);
  });

  test("keeps existing front matter; initial_content beats source; refs.bib copied; preset unvalidated", () => {
    fx = makeFixture();
    const source = fx.write(join(fx.roots.workspace, "notes", "src.md"), "# From source\n");
    fx.write(join(fx.roots.workspace, "notes", "refs.bib"), "@misc{k, title={K}}\n");
    const svc = service(Math.random);
    const a = idFrom(svc.draft({ title: "A", preset: "does-not-exist", initial_content: "---\ntitle: Own\n---\nBody", source_markdown: "notes/src.md" }));
    const aDir = join(fx.roots.workspace, ".opencode", "docs", a);
    expect(readFileSync(join(aDir, "draft.md"), "utf8")).toBe("---\ntitle: Own\n---\nBody");
    expect(readFileSync(join(aDir, "refs.bib"), "utf8")).toContain("@misc{k");
    expect(JSON.parse(readFileSync(join(aDir, "meta.json"), "utf8")).source_markdown).toBe(source);

    const b = idFrom(svc.draft({ title: "B", preset: "eisvogel", source_markdown: source }));
    expect(readFileSync(join(fx.roots.workspace, ".opencode", "docs", b, "draft.md"), "utf8")).toEndWith("# From source\n");

    const c = idFrom(svc.draft({ title: "C", preset: "eisvogel", source_markdown: "nope/missing.md" }));
    expect(readFileSync(join(fx.roots.workspace, ".opencode", "docs", c, "draft.md"), "utf8")).toBe('---\ntitle: "C"\ndate: "2026-03-04"\n---\n\n');
  });

  test("date is computed in UTC", () => {
    fx = makeFixture();
    const late = new DocsService(fx.roots, { now: () => new Date("2026-12-31T23:59:59.000Z") });
    const id = idFrom(late.draft({ title: "T", preset: "p" }));
    expect(readFileSync(join(fx.roots.workspace, ".opencode", "docs", id, "draft.md"), "utf8")).toContain('date: "2026-12-31"');
  });

  test("a workspace of / is rejected", () => {
    fx = makeFixture();
    const root = new DocsService({ ...fx.roots, workspace: "/" });
    expect(() => root.draft({ title: "T", preset: "p" })).toThrow("Cannot determine project directory. Please run from a project directory.");
    expect(() => root.listDrafts()).toThrow("Cannot determine project directory");
  });
});

describe("docs_list_drafts and docs_delete_draft", () => {
  test("empty message, formatted entries and missing status", () => {
    fx = makeFixture();
    const svc = service(Math.random);
    expect(svc.listDrafts()).toBe("No active drafts. Create one with docs_draft.");
    const id = idFrom(svc.draft({ title: "One", preset: "eisvogel" }));
    const draftPath = join(fx.roots.workspace, ".opencode", "docs", id, "draft.md");
    expect(svc.listDrafts()).toBe(
      ["Active document drafts:", "", id, "  Title: One", "  Preset: eisvogel", "  Status: draft", "  Modified: 2026-03-04", `  Path: ${draftPath}`, ""].join("\n"),
    );
    rmSync(draftPath);
    expect(svc.listDrafts()).toContain("  Status: missing");
  });

  test("delete validates, reports unknown IDs and removes files and entry", () => {
    fx = makeFixture();
    const svc = service(Math.random);
    for (const bad of ["../x", "a-b", "UPPER-case-1"]) expect(svc.deleteDraft({ doc_id: bad })).toBe(`Invalid document ID format: ${bad}`);
    expect(svc.deleteDraft({ doc_id: "misty-harbor-123" })).toBe("Draft not found: misty-harbor-123");
    const id = idFrom(svc.draft({ title: "Gone", preset: "eisvogel" }));
    const dir = join(fx.roots.workspace, ".opencode", "docs", id);
    expect(svc.deleteDraft({ doc_id: id })).toBe(`Deleted draft: ${id}`);
    expect(existsSync(dir)).toBe(false);
    expect(readRegistry(join(fx.roots.workspace, ".opencode", "docs-registry.json")).drafts[id]).toBeUndefined();
  });

  test("a corrupt registry is treated as empty", () => {
    fx = makeFixture();
    fx.write(join(fx.roots.workspace, ".opencode", "docs-registry.json"), "{not json");
    expect(service().listDrafts()).toBe("No active drafts. Create one with docs_draft.");
    writeFileSync(join(fx.roots.workspace, ".opencode", "docs-registry.json"), "[]");
    expect(service().listDrafts()).toBe("No active drafts. Create one with docs_draft.");
  });
});

describe("authorsYaml", () => {
  test("hyphenated keys, quoted values, escaping", () => {
    expect(authorsYaml(JSON.stringify([{ name: 'A "Q"\nB', sit_id: "123", glasgow_id: "" }, { name: "C\\D", glasgow_id: "9G" }]))).toBe(
      'authors:\n  - name: "A \\"Q\\" B"\n    sit-id: "123"\n  - name: "C\\\\D"\n    glasgow-id: "9G"\n',
    );
  });

  test("invalid JSON and non-arrays are rejected", () => {
    expect(() => authorsYaml("{oops")).toThrow(/^Invalid authors JSON: /);
    expect(() => authorsYaml('{"name":"x"}')).toThrow(/^Invalid authors JSON: /);
    expect(() => authorsYaml('[{"sit_id":"1"}]')).toThrow(/^Invalid authors JSON: /);
  });
});
