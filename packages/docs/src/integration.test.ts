// End-to-end compiles with the real toolchain. Skipped automatically when pandoc or a
// required TeX binary is missing.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { copyFileSync, existsSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { DocsService } from "./service";
import { spawnRunner } from "./process";
import { makeFixture, type Fixture } from "./testing";

const HAVE = (names: string[]) => names.every((name) => Bun.which(name) !== null);
const TEX_READY = HAVE(["pandoc", "xelatex", "pdflatex", "bibtex"]);
const PDFTOTEXT = HAVE(["pdftotext"]);
const TIMEOUT = 180_000;

let fx: Fixture;
let svc: DocsService;

beforeAll(() => {
  fx = makeFixture({ realBundled: true });
  svc = new DocsService(fx.roots);
});
afterAll(() => fx?.cleanup());

function isPdf(path: string): boolean {
  return existsSync(path) && statSync(path).size > 1000 && readFileSync(path).subarray(0, 5).toString() === "%PDF-";
}

async function pdfText(path: string): Promise<string> {
  if (!PDFTOTEXT) return "";
  const result = await spawnRunner(["pdftotext", "-layout", path, "-"]);
  return result.stdout;
}

function draft(preset: string, body: string, title = "Integration Report"): { id: string; dir: string } {
  const out = svc.draft({ title, preset, initial_content: `---\ntitle: "${title}"\ndate: "2026-09-30"\n---\n\n${body}` });
  const id = /Document ID: (\S+)/.exec(out)![1]!;
  return { id, dir: join(fx.roots.workspace, ".opencode", "docs", id) };
}

const RICH_BODY = [
  "# Introduction",
  "",
  "Inline `code_sample()` and a long paragraph with *emphasis* and ~~strikeout~~.",
  "",
  "![A figure](figure.png)",
  "",
  "- [ ] open task",
  "- [x] done task",
  "",
  "| Column A | Column B |",
  "|----------|----------|",
  "| 1        | 2        |",
  "",
  ": A table caption",
  "",
  "```python",
  "def hello(name):",
  "    return f'hello {name}'  # this comment is deliberately long so listings has to wrap the line",
  "```",
  "",
  "## Details",
  "",
  "More text.",
  "",
].join("\n");

function withFigure(dir: string): void {
  copyFileSync(join(fx.roots.bundled, "assets", "sit-logo.png"), join(dir, "figure.png"));
}

describe.skipIf(!TEX_READY)("integration: school-report", () => {
  const cases = [
    { logo: "sit", authors: [{ name: "Solo Student" }], columns: 1 },
    { logo: "uofg", authors: [{ name: "Alice Tan", sit_id: "2301234" }, { name: "Bob Lim" }], columns: 2 },
    { logo: "both", authors: [{ name: "Alice Tan", sit_id: "2301234", glasgow_id: "2912345T" }, { name: "Bob Lim", glasgow_id: "2900000L" }], columns: 3 },
  ] as const;

  for (const c of cases) {
    test(
      `logo=${c.logo} with a ${c.columns}-column author table and TOC`,
      async () => {
        const { id, dir } = draft("school-report", RICH_BODY);
        withFigure(dir);
        const out = await svc.compile({
          doc_id: id,
          logo: c.logo,
          authors: JSON.stringify(c.authors),
          course: "CSC3101 Capstone",
          project_title: "Smart Widgets",
          group: "Group 7",
          version: "1.2",
          project_topic_id: "PT-42",
        });
        const pdf = join(dir, `${id}.pdf`);
        expect(out).toStartWith(`Compiled: ${pdf}`);
        expect(isPdf(pdf)).toBe(true);
        const text = await pdfText(pdf);
        if (PDFTOTEXT) {
          for (const needle of ["Integration Report", "Student name", "Version: 1.2", "Project Topic ID: PT-42", "CSC3101 Capstone", "Smart Widgets", "Group 7", "Contents"]) {
            expect(text).toContain(needle);
          }
          if (c.columns >= 2) expect(text).toContain("SIT Student ID");
          else expect(text).not.toContain("SIT Student ID");
          if (c.columns === 3) expect(text).toContain("Glasgow Student ID");
        }
      },
      TIMEOUT,
    );
  }

  test(
    "docs_create with logo=sit succeeds (template renders the chosen logo)",
    async () => {
      const input = fx.write(join(fx.roots.workspace, "create", "report.md"), `---\ntitle: Created Report\n---\n\n${RICH_BODY}`);
      withFigure(join(fx.roots.workspace, "create"));
      const out = await svc.create({ input_path: input, preset: "school-report", logo: "sit", author: "Jane Doe" });
      const pdf = join(fx.roots.workspace, "create", "report.pdf");
      expect(out).toBe(`Created: ${pdf}\nPreset: school-report`);
      expect(isPdf(pdf)).toBe(true);
    },
    TIMEOUT,
  );

  test(
    "the template renders `-V logo` alone (no logo-mode)",
    async () => {
      const dir = join(fx.roots.workspace, "logo-only");
      const input = fx.write(join(dir, "doc.md"), "---\ntitle: Logo Only\n---\n\nBody `x`.\n");
      const template = join(fx.roots.bundled, "templates", "sit-uofg", "template.latex");
      const output = join(dir, "doc.pdf");
      const result = await spawnRunner([
        "pandoc", "--template", template, "--pdf-engine=xelatex", "--listings",
        "-V", "titlepage=true", "-V", `logo=${join(fx.roots.bundled, "assets", "sit-logo.png")}`, input, "-o", output,
      ]);
      expect(result.code).toBe(0);
      expect(isPdf(output)).toBe(true);
      if (PDFTOTEXT) expect(await pdfText(output)).toContain("Logo Only");
    },
    TIMEOUT,
  );

  test(
    "citeproc bibliography (default CSL) renders the references environment",
    async () => {
      const { id, dir } = draft("school-report", "# Body\n\nAs argued by @smith2020 and others [@doe2021].\n");
      fx.write(join(dir, "refs.bib"), "@article{smith2020, author={Smith, John}, title={A Study}, journal={J. Stuff}, year={2020}}\n@book{doe2021, author={Doe, Jane}, title={Book Title}, publisher={Pub}, year={2021}}\n");
      await svc.compile({ doc_id: id });
      const pdf = join(dir, `${id}.pdf`);
      expect(isPdf(pdf)).toBe(true);
      if (PDFTOTEXT) expect(await pdfText(pdf)).toContain("Book Title");
    },
    TIMEOUT,
  );

  test(
    "citation_style=none compiles text with @ signs; apa without its CSL gives the install hint",
    async () => {
      const { id, dir } = draft("school-report", "# Contact\n\nPing @alice or mail a@b.com [@not-a-cite].\n");
      await svc.compile({ doc_id: id, citation_style: "none" });
      expect(isPdf(join(dir, `${id}.pdf`))).toBe(true);
      fx.write(join(dir, "refs.bib"), "@misc{k, title={K}}\n");
      await expect(svc.compile({ doc_id: id, citation_style: "apa" })).rejects.toThrow("CSL style 'apa.csl' not found. Run: docs_templates_install csl-apa");
    },
    TIMEOUT,
  );
});

describe.skipIf(!TEX_READY)("integration: natbib/BibTeX pipeline", () => {
  test(
    "a natbib preset builds through pdflatex + bibtex with images, inline code and citations",
    async () => {
      fx.write(
        join(fx.roots.project, "presets", "natbib-report.yaml"),
        "name: natbib-report\nextends: school-report\npdf_engine: pdflatex\ncitation: { backend: natbib, biblio_style: plainnat }\n",
      );
      // Missing `year` makes bibtex emit a warning (exit 1), which must not abort the build.
      const { id, dir } = draft("natbib-report", `${RICH_BODY}\nAs shown by @knuth1984 and [@lamport1994].\n`);
      withFigure(dir);
      fx.write(
        join(dir, "refs.bib"),
        "@book{knuth1984, author={Donald E. Knuth}, title={The {TeX}book}, publisher={Addison-Wesley}}\n@book{lamport1994, author={Leslie Lamport}, title={{LaTeX}: A Document Preparation System}, publisher={Addison-Wesley}, year={1994}}\n",
      );
      const out = await svc.compile({ doc_id: id, logo: "both", authors: JSON.stringify([{ name: "Ada", sit_id: "1" }]) });
      const pdf = join(dir, `${id}.pdf`);
      expect(out).toStartWith(`Compiled: ${pdf}`);
      expect(isPdf(pdf)).toBe(true);
      expect(existsSync(join(dir, ".opencode-build", id, `${id}.bbl`))).toBe(true);
      if (PDFTOTEXT) {
        const text = await pdfText(pdf);
        expect(text).toContain("Lamport");
        expect(text).toContain("code_sample()");
        expect(text).not.toContain("[?]");
      }
    },
    TIMEOUT,
  );
});

describe.skipIf(!TEX_READY)("integration: eisvogel, convert, compile_latex", () => {
  test(
    "eisvogel preset produces a PDF with the kept template",
    async () => {
      const input = fx.write(join(fx.roots.workspace, "eis", "doc.md"), `---\ntitle: Eisvogel Doc\nauthor: Jane\n---\n\n${RICH_BODY}`);
      withFigure(join(fx.roots.workspace, "eis"));
      const out = await svc.create({ input_path: input, preset: "eisvogel" });
      expect(out).toBe(`Created: ${join(fx.roots.workspace, "eis", "doc.pdf")}\nPreset: eisvogel`);
      expect(isPdf(join(fx.roots.workspace, "eis", "doc.pdf"))).toBe(true);
    },
    TIMEOUT,
  );

  test(
    "docs_convert md -> docx, html and latex; docs_compile_latex builds the .tex",
    async () => {
      const input = fx.write(join(fx.roots.workspace, "conv", "note.md"), "# Note\n\nHello **world**.\n");
      for (const format of ["docx", "html", "latex"] as const) {
        const out = await svc.convert({ input_path: input, output_format: format });
        const expected = join(fx.roots.workspace, "conv", `note.${format}`);
        expect(out).toBe(`Converted ${input} to ${expected}`);
        expect(existsSync(expected)).toBe(true);
      }
      const standalone = await svc.convert({ input_path: input, output_format: "latex", output_dir: "conv/tex", from_format: "markdown" });
      expect(standalone).toContain("conv/tex/note.latex");
      const tex = fx.write(join(fx.roots.workspace, "conv", "tex", "plain.tex"), "\\documentclass{article}\\begin{document}Hello.\\end{document}\n");
      expect(await svc.compileLatex({ file_path: tex })).toBe(`Compiled ${tex} to PDF.`);
      expect(isPdf(join(fx.roots.workspace, "conv", "tex", "plain.pdf"))).toBe(true);
      const broken = fx.write(join(fx.roots.workspace, "conv", "tex", "broken.tex"), "\\documentclass{article}\\begin{document}\\undefinedmacro\\end{document}\n");
      await expect(svc.compileLatex({ file_path: broken })).rejects.toThrow(/Compilation failed:\n[\s\S]*Undefined control sequence/);
    },
    TIMEOUT,
  );
});
