import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { readRegistry } from "./drafts";
import { DocsService } from "./service";
import { makeFixture, stubRunner, touchOutput, type Fixture, type StubHandler } from "./testing";

let fx: Fixture;
afterEach(() => fx?.cleanup());

const NOW = new Date("2026-05-06T07:08:09.000Z");

function setup(handler?: StubHandler, options: { realBundled?: boolean } = { realBundled: true }) {
  fx = makeFixture(options);
  const stub = stubRunner(handler ?? ((argv) => touchOutput(argv)));
  const svc = new DocsService(fx.roots, { run: stub.run, now: () => NOW, random: Math.random });
  return { svc, calls: stub.calls };
}

function makeDraft(svc: DocsService, preset = "school-report", content = "Body text.\n"): { id: string; dir: string } {
  const out = svc.draft({ title: "Report", preset, initial_content: `---\ntitle: Report\n---\n\n${content}` });
  const id = /Document ID: (\S+)/.exec(out)![1]!;
  return { id, dir: join(fx.roots.workspace, ".opencode", "docs", id) };
}

function valueOf(argv: string[], key: string): string | undefined {
  for (let i = 0; i < argv.length - 1; i++) if (argv[i] === "-V" && argv[i + 1]!.startsWith(`${key}=`)) return argv[i + 1]!.slice(key.length + 1);
  return undefined;
}

describe("docs_compile soft failures", () => {
  test("invalid ID, unknown ID and missing draft.md are returned, not thrown", async () => {
    const { svc, calls } = setup();
    expect(await svc.compile({ doc_id: "../etc" })).toBe("Invalid document ID format: ../etc");
    expect(await svc.compile({ doc_id: "misty-harbor-123" })).toBe("Draft not found: misty-harbor-123. Use docs_list_drafts to see available drafts.");
    const { id, dir } = makeDraft(svc);
    rmSync(join(dir, "draft.md"));
    expect(await svc.compile({ doc_id: id })).toBe(`Draft file missing: ${join(dir, "draft.md")}`);
    expect(calls).toHaveLength(0);
  });
});

describe("docs_compile orchestration", () => {
  test("default output, direct pandoc run and return text", async () => {
    const { svc, calls } = setup();
    const { id, dir } = makeDraft(svc);
    const out = await svc.compile({ doc_id: id });
    expect(out).toBe(`Compiled: ${join(dir, `${id}.pdf`)}\nDocument: Report\nPreset: school-report`);
    expect(calls).toHaveLength(1);
    const argv = calls[0]!.argv;
    expect(argv[0]).toBe("pandoc");
    expect(argv.slice(-3)).toEqual([join(dir, "draft.md"), "-o", join(dir, `${id}.pdf`)]);
    expect(argv).toContain("--listings");
    expect(argv).toContain(`--resource-path=${dir}`);
    expect(argv).not.toContain("--columns=1");
    const entry = readRegistry(join(fx.roots.workspace, ".opencode", "docs-registry.json")).drafts[id]!;
    expect(entry.status).toBe("compiled");
    expect(entry.last_modified).toBe(NOW.toISOString());
  });

  test("custom output path resolves against the workspace", async () => {
    const { svc } = setup();
    const { id } = makeDraft(svc);
    const out = await svc.compile({ doc_id: id, output_path: "build/final.pdf" });
    expect(out).toStartWith(`Compiled: ${join(fx.roots.workspace, "build", "final.pdf")}`);
    expect(existsSync(join(fx.roots.workspace, "build", "final.pdf"))).toBe(true);
  });

  test("logo variables for sit, uofg and both", async () => {
    const { svc, calls } = setup();
    const { id } = makeDraft(svc);
    const bundled = fx.roots.bundled;
    await svc.compile({ doc_id: id, logo: "sit" });
    await svc.compile({ doc_id: id, logo: "uofg" });
    await svc.compile({ doc_id: id, logo: "both" });
    const [sit, uofg, both] = calls.map((c) => c.argv);
    expect(valueOf(sit!, "logo-mode")).toBe("single");
    expect(valueOf(sit!, "sit-logo")).toBe(join(bundled, "assets", "sit-logo.png"));
    expect(valueOf(sit!, "uofg-logo")).toBeUndefined();
    expect(valueOf(sit!, "logo")).toBe(join(bundled, "assets", "sit-logo.png"));
    expect(valueOf(uofg!, "logo-mode")).toBe("single");
    expect(valueOf(uofg!, "uofg-logo")).toBe(join(bundled, "assets", "uofg-logo.png"));
    expect(valueOf(both!, "logo-mode")).toBe("both");
    expect(valueOf(both!, "sit-logo")).toBeDefined();
    expect(valueOf(both!, "uofg-logo")).toBeDefined();
    expect(valueOf(both!, "logo")).toBeUndefined();
    await expect(svc.compile({ doc_id: id, logo: "acme" })).rejects.toThrow("Logo option 'acme' not found. Available options: sit, uofg, both");
  });

  test("a missing logo fails the compile, not plugin setup", async () => {
    const { svc } = setup(undefined, { realBundled: false });
    fx.write(join(fx.roots.bundled, "templates", "sit-uofg", "template.latex"), "x");
    const { id } = makeDraft(svc);
    await expect(svc.compile({ doc_id: id, logo: "uofg" })).rejects.toThrow("Required asset not found: assets/uofg-logo.png");
    expect(await svc.compile({ doc_id: id })).toStartWith("Compiled:");
  });

  test("metadata overrides and school fields map to hyphenated variables", async () => {
    const { svc, calls } = setup();
    const { id } = makeDraft(svc);
    await svc.compile({
      doc_id: id,
      author: "Jane",
      keywords: "a, b",
      course: "CSC3101",
      project_title: "Proj",
      group: "G7",
      version: "1.0",
      project_topic_id: "PT-1",
    });
    const argv = calls[0]!.argv;
    expect(valueOf(argv, "author")).toBe("Jane");
    expect(valueOf(argv, "keywords")).toBe("a, b");
    expect(valueOf(argv, "course")).toBe("CSC3101");
    expect(valueOf(argv, "project-title")).toBe("Proj");
    expect(valueOf(argv, "group")).toBe("G7");
    expect(valueOf(argv, "version")).toBe("1.0");
    expect(valueOf(argv, "project-topic-id")).toBe("PT-1");
    expect(argv.indexOf("-V")).toBeLessThan(argv.indexOf("course=CSC3101"));
  });

  test("authors YAML is written escaped, passed, and deleted on success and failure", async () => {
    let seen = "";
    let fail = false;
    const { svc, calls } = setup((argv) => {
      const meta = argv.find((a) => a.startsWith("--metadata-file="));
      if (meta) seen = readFileSync(meta.slice("--metadata-file=".length), "utf8");
      if (fail) return { code: 1, stderr: "boom" };
      touchOutput(argv);
    });
    const { id, dir } = makeDraft(svc);
    await svc.compile({ doc_id: id, authors: JSON.stringify([{ name: 'A "B"', sit_id: "1", glasgow_id: "2G" }]) });
    expect(seen).toBe('authors:\n  - name: "A \\"B\\""\n    sit-id: "1"\n    glasgow-id: "2G"\n');
    expect(calls[0]!.argv.find((a) => a.startsWith("--metadata-file="))).toMatch(/authors-\d+\.yaml$/);
    expect(readdirSync(dir).filter((f) => f.startsWith("authors-"))).toEqual([]);
    fail = true;
    await expect(svc.compile({ doc_id: id, authors: '[{"name":"x"}]' })).rejects.toThrow("Compilation failed:\nboom");
    expect(readdirSync(dir).filter((f) => f.startsWith("authors-"))).toEqual([]);
    await expect(svc.compile({ doc_id: id, authors: "not json" })).rejects.toThrow(/^Invalid authors JSON: /);
  });

  test("registry is marked compiled even when pandoc fails; error falls back to stdout tail", async () => {
    const stdout = Array.from({ length: 40 }, (_, i) => `line ${i + 1}`).join("\n");
    const { svc } = setup(() => ({ code: 43, stdout }));
    const { id } = makeDraft(svc);
    const error = await svc.compile({ doc_id: id }).catch((e: Error) => e);
    expect((error as Error).message).toBe(`Compilation failed:\n${Array.from({ length: 30 }, (_, i) => `line ${i + 11}`).join("\n")}`);
    expect(readRegistry(join(fx.roots.workspace, ".opencode", "docs-registry.json")).drafts[id]!.status).toBe("compiled");
  });

  test("template-not-found error", async () => {
    const { svc } = setup(undefined, { realBundled: false });
    const { id } = makeDraft(svc, "eisvogel");
    await expect(svc.compile({ doc_id: id })).rejects.toThrow("Template 'eisvogel.latex' not found. Use docs_templates_install.");
  });

  test("citation_style=none strips citations from the input format", async () => {
    const { svc, calls } = setup();
    const { id } = makeDraft(svc, "school-report", "Email me @ home or @someone.\n");
    await svc.compile({ doc_id: id, citation_style: "none" });
    const argv = calls[0]!.argv;
    expect(argv.slice(argv.indexOf("-f"), argv.indexOf("-f") + 2)).toEqual(["-f", "markdown+smart+pipe_tables-citations"]);
    await expect(svc.compile({ doc_id: id })).rejects.toThrow("Citations found in");
  });

  test("citeproc with refs.bib and apa without its CSL", async () => {
    const { svc, calls } = setup();
    const { id, dir } = makeDraft(svc, "school-report", "See [@k].\n");
    fx.write(join(dir, "refs.bib"), "@misc{k, title={K}}\n");
    await svc.compile({ doc_id: id });
    expect(calls[0]!.argv).toContain(`--bibliography=${join(dir, "refs.bib")}`);
    expect(calls[0]!.argv).toContain("--citeproc");
    await expect(svc.compile({ doc_id: id, citation_style: "apa" })).rejects.toThrow("CSL style 'apa.csl' not found. Run: docs_templates_install csl-apa");
  });
});

describe("natbib / BibTeX pipeline", () => {
  function natbibSetup(handler: StubHandler) {
    const ctx = setup(handler);
    // Support files next to a project template are staged into the build dir.
    const template = fx.write(join(fx.roots.project, "templates", "nat", "template.latex"), "tpl");
    fx.write(join(fx.roots.project, "templates", "nat", "Custom.bst"), "bst");
    fx.write(join(fx.roots.project, "templates", "nat", "readme.txt"), "no");
    fx.write(join(fx.roots.project, "presets", "nat.yaml"), "name: nat\nextends: school-report\npdf_engine: pdflatex\ntemplate: { path: nat }\ncitation: { backend: natbib, biblio_style: plainnat }\n");
    return { ...ctx, template };
  }

  test("build dir, staged files, env paths, command order and copied PDF", async () => {
    const pdfBody = "%PDF-from-build";
    const { svc, calls, template } = natbibSetup((argv, options) => {
      if (argv[0] === "pandoc") touchOutput(argv);
      if (argv[0] === "pdflatex") {
        const name = argv.at(-1)!.replace(/\.tex$/, "");
        writeFileSync(join(options.cwd!, `${name}.aux`), "\\citation{k}\n");
        writeFileSync(join(options.cwd!, `${name}.pdf`), pdfBody);
      }
      if (argv[0] === "bibtex") return { code: 1, stdout: "Warning--empty year" };
    });
    const { id, dir } = makeDraft(svc, "nat", "Cite [@k].\n");
    fx.write(join(dir, "refs.bib"), "@misc{k, title={K}}\n");
    const out = await svc.compile({ doc_id: id });
    const buildDir = join(dir, ".opencode-build", id);
    expect(out).toStartWith(`Compiled: ${join(dir, `${id}.pdf`)}`);
    expect(calls.map((c) => c.argv[0])).toEqual(["pandoc", "pdflatex", "bibtex", "pdflatex", "pdflatex"]);
    const pandoc = calls[0]!;
    expect(pandoc.options.cwd).toBe(buildDir);
    expect(pandoc.argv.slice(-2)).toEqual(["-o", join(buildDir, `${id}.tex`)]);
    expect(pandoc.argv).toContain("--bibliography=refs.bib");
    expect(pandoc.argv).toContain("--natbib");
    expect(valueOf(pandoc.argv, "biblio-style")).toBe("plainnat");
    expect(existsSync(join(buildDir, "refs.bib"))).toBe(true);
    expect(existsSync(join(buildDir, "Custom.bst"))).toBe(true);
    expect(existsSync(join(buildDir, "readme.txt"))).toBe(false);
    const latex = calls[1]!;
    expect(latex.argv).toEqual(["pdflatex", "-interaction=nonstopmode", "-halt-on-error", "-file-line-error", `${id}.tex`]);
    expect(calls[2]!.argv).toEqual(["bibtex", id]);
    const templateDir = dirname(template);
    const env = latex.options.env!;
    expect(env.TEXINPUTS).toStartWith(`${buildDir}:${dir}:${templateDir}:`);
    expect(env.TEXINPUTS).toEndWith(":");
    expect(env.BIBINPUTS).toStartWith(`${dir}:${buildDir}:`);
    expect(env.BIBINPUTS).toEndWith(":");
    expect(env.BSTINPUTS).toStartWith(`${buildDir}:${templateDir}:`);
    expect(env.BSTINPUTS).toEndWith(":");
    expect(readFileSync(join(dir, `${id}.pdf`), "utf8")).toBe(pdfBody);
  });

  test("bibtex errors (exit >= 2) abort; missing PDF is reported", async () => {
    let bibtexCode = 2;
    let writePdf = true;
    const { svc } = natbibSetup((argv, options) => {
      if (argv[0] === "pandoc") touchOutput(argv);
      if (argv[0] === "pdflatex" && writePdf) {
        writeFileSync(join(options.cwd!, argv.at(-1)!.replace(/\.tex$/, ".pdf")), "%PDF");
      }
      if (argv[0] === "bibtex") return { code: bibtexCode, stdout: "I couldn't open database file" };
    });
    const { id, dir } = makeDraft(svc, "nat", "Cite [@k].\n");
    fx.write(join(dir, "refs.bib"), "@misc{k, title={K}}\n");
    await expect(svc.compile({ doc_id: id })).rejects.toThrow("Compilation failed:\nI couldn't open database file");
    bibtexCode = 0;
    writePdf = false;
    await expect(svc.compile({ doc_id: id })).rejects.toThrow(`Expected PDF not found after BibTeX build: ${join(dir, ".opencode-build", id, `${id}.pdf`)}`);
  });

  test("bibtex is skipped when the aux file has no citations", async () => {
    const { svc, calls } = natbibSetup((argv, options) => {
      if (argv[0] === "pandoc") touchOutput(argv);
      if (argv[0] === "pdflatex") {
        writeFileSync(join(options.cwd!, argv.at(-1)!.replace(/\.tex$/, ".aux")), "\\relax\n");
        writeFileSync(join(options.cwd!, argv.at(-1)!.replace(/\.tex$/, ".pdf")), "%PDF");
      }
    });
    const { id, dir } = makeDraft(svc, "nat", "No citations here.\n");
    fx.write(join(dir, "refs.bib"), "@misc{k, title={K}}\n");
    await svc.compile({ doc_id: id });
    expect(calls.map((c) => c.argv[0])).toEqual(["pandoc", "pdflatex", "pdflatex", "pdflatex"]);
  });
});

describe("docs_create", () => {
  test("argv, output path, logo variables and return text", async () => {
    const { svc, calls } = setup();
    const input = fx.write(join(fx.roots.workspace, "docs", "paper.md"), "# Hi\n");
    const out = await svc.create({ input_path: "docs/paper.md", preset: "school-report", logo: "both", title: "T", output_dir: "out" });
    const output = join(fx.roots.workspace, "out", "paper.pdf");
    expect(out).toBe(`Created: ${output}\nPreset: school-report`);
    const argv = calls[0]!.argv;
    expect(argv.slice(-3)).toEqual([input, "-o", output]);
    expect(argv).toContain("--listings");
    expect(argv).toContain(`--resource-path=${dirname(input)}`);
    expect(valueOf(argv, "title")).toBe("T");
    expect(valueOf(argv, "logo-mode")).toBe("both");
  });

  test("template check precedes citation checks; failures are prefixed", async () => {
    const { svc } = setup((argv) => (argv[0] === "pandoc" ? { code: 2, stderr: "pandoc: bad" } : undefined), { realBundled: false });
    fx.write(join(fx.roots.workspace, "c.md"), "[@x]\n");
    await expect(svc.create({ input_path: "c.md", preset: "eisvogel", citation_style: "apa" })).rejects.toThrow("Template 'eisvogel.latex' not found");
    fx.write(join(fx.roots.bundled, "templates", "eisvogel.latex"), "x");
    svc.presets.clearCache(); // resolved template paths are cached until an install clears them
    await expect(svc.create({ input_path: "c.md", preset: "eisvogel", citation_style: "none" })).rejects.toThrow("Failed:\npandoc: bad");
  });
});

describe("docs_convert and docs_compile_latex", () => {
  test("convert argv, extension and errors", async () => {
    const { svc, calls } = setup();
    const input = fx.write(join(fx.roots.workspace, "a", "doc.md"), "x");
    expect(await svc.convert({ input_path: "a/doc.md", output_format: "markdown", from_format: "html" })).toBe(`Converted a/doc.md to ${join(dirname(input), "doc.markdown")}`);
    expect(calls[0]!.argv).toEqual(["pandoc", "-f", "html", `--resource-path=${dirname(input)}`, input, "-o", join(dirname(input), "doc.markdown")]);
    const failing = setup(() => ({ code: 1, stderr: "nope" }));
    fx.write(join(fx.roots.workspace, "d.md"), "x");
    await expect(failing.svc.convert({ input_path: "d.md", output_format: "docx" })).rejects.toThrow("Pandoc failed:\nnope");
  });

  test("compile_latex validates, runs one pdflatex pass and reports stdout on failure", async () => {
    const { svc, calls } = setup();
    await expect(svc.compileLatex({ file_path: "/x/paper.md" })).rejects.toThrow("Input file must be a .tex file");
    const tex = fx.write(join(fx.roots.workspace, "tex", "p.tex"), "x");
    expect(await svc.compileLatex({ file_path: tex })).toBe(`Compiled ${tex} to PDF.`);
    expect(calls[0]!.argv).toEqual(["pdflatex", "-output-directory", dirname(tex), "-interaction=nonstopmode", tex]);
    const failing = setup(() => ({ code: 1, stdout: "! Undefined control sequence." }));
    const tex2 = fx.write(join(fx.roots.workspace, "p.tex"), "x");
    await expect(failing.svc.compileLatex({ file_path: tex2, output_dir: "out" })).rejects.toThrow("Compilation failed:\n! Undefined control sequence.");
    expect(existsSync(join(fx.roots.workspace, "out"))).toBe(true);
  });
});

describe("listing tools", () => {
  test("presets list groups and blank-line layout", () => {
    const { svc } = setup(undefined, { realBundled: false });
    fx.write(join(fx.roots.user, "presets", "mine.yaml"), "description: ignored in list\n");
    fx.write(join(fx.roots.project, "presets", "team.yaml"), "name: team\n");
    expect(svc.presetsList()).toBe(
      [
        "Available presets:",
        "",
        "Built-in:",
        "  - eisvogel: General purpose professional document (Eisvogel template)",
        "  - school-report: SIT/UofG school report with optional logos and adjustable margins",
        "",
        "User:",
        "  - mine",
        "Project:",
        "  - team",
      ].join("\n"),
    );
  });

  test("presets show", () => {
    const { svc } = setup();
    expect(svc.presetsShow({ preset_name: "school-report" })).toBe(
      [
        "Preset: school-report",
        "Description: SIT/UofG school report with optional logos and adjustable margins",
        "Source: builtin",
        "Template: sit-uofg/template.latex (installed)",
        "Logos: sit, uofg, both",
        "Required: title",
      ].join("\n"),
    );
    fx.write(join(fx.roots.user, "presets", "cite.yaml"), "name: cite\ncitation: { style: apa }\n");
    expect(svc.presetsShow({ preset_name: "cite" })).toBe("Preset: cite\nSource: user\nCitations: apa via citeproc");
    expect(() => svc.presetsShow({ preset_name: "zzz" })).toThrow(/^Preset 'zzz' not found$/);
  });

  test("templates list with and without entries", () => {
    const { svc } = setup(undefined, { realBundled: false });
    expect(svc.templatesList()).toBe("Templates:\n  (none - use docs_templates_install)\n\nCSL Styles:\n  (none)");
    fx.write(join(fx.roots.bundled, "templates", "sit-uofg", "template.latex"));
    fx.write(join(fx.roots.user, "csl", "apa.csl"));
    expect(svc.templatesList()).toBe("Templates:\n  - sit-uofg (bundled)\n\nCSL Styles:\n  - apa");
  });

  test("the real bundled dir lists eisvogel and sit-uofg", () => {
    const { svc } = setup();
    const text = svc.templatesList();
    expect(text).toContain("  - eisvogel (bundled)");
    expect(text).toContain("  - sit-uofg (bundled)");
    expect(text).not.toContain("ieee");
  });
});
