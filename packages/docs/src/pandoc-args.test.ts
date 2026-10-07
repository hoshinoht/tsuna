import { afterEach, describe, expect, test } from "bun:test";
import { dirname } from "node:path";
import { applyMetadata, applyPreset, computeOutputPath, PandocArgs, usesIeee } from "./pandoc-args";
import { PresetStore, type ResolvedPreset } from "./presets";
import { makeFixture, type Fixture } from "./testing";

let fx: Fixture;
afterEach(() => fx?.cleanup());

describe("applyPreset", () => {
  test("school-report argv order without a logo", () => {
    fx = makeFixture({ realBundled: true });
    const preset = new PresetStore(fx.roots).load("school-report");
    const template = preset.resolved_template_path!;
    expect(applyPreset(new PandocArgs(), preset, { includeCsl: false }).options()).toEqual([
      "-f", "markdown+smart+pipe_tables",
      `--template=${template}`,
      `--resource-path=${dirname(template)}`,
      "--pdf-engine=xelatex",
      "-V", "titlepage=true",
      "--toc",
      "-V", "toc-own-page=true",
      "--number-sections",
      "-V", "colorlinks=true",
      "-V", "linkcolor=blue",
      "-V", "top-margin=2.5cm",
      "-V", "bottom-margin=2.5cm",
      "-V", "left-margin=2.5cm",
      "-V", "right-margin=2.5cm",
    ]);
  });

  test("school-report with a logo appends logo and logo-width last", () => {
    fx = makeFixture({ realBundled: true });
    const preset = new PresetStore(fx.roots).load("school-report", "sit");
    const argv = applyPreset(new PandocArgs(), preset).options();
    expect(argv.slice(-4)).toEqual(["-V", `logo=${preset.resolved_logo_path}`, "-V", "logo-width=150"]);
  });

  test("eisvogel argv and input-format override", () => {
    fx = makeFixture({ realBundled: true });
    const preset = new PresetStore(fx.roots).load("eisvogel");
    const template = preset.resolved_template_path!;
    expect(applyPreset(new PandocArgs(), preset, { inputFormatOverride: "markdown-citations" }).options()).toEqual([
      "-f", "markdown-citations",
      `--template=${template}`,
      `--resource-path=${dirname(template)}`,
      "--pdf-engine=xelatex",
      "-V", "colorlinks=true",
      "-V", "linkcolor=blue",
    ]);
  });

  test("class options, text colour, CSL, extra args and boolean stringification", () => {
    const preset: ResolvedPreset = {
      name: "custom",
      source: "user",
      template: { path: "x", class_options: ["twocolumn", "11pt"] },
      resolved_template_path: "/t/x/template.latex",
      resolved_csl_path: "/c/apa.csl",
      style: { titlepage: false, titlepage_color: "06386e", text_color: "FFFFFF", colorlinks: false },
      pandoc: { variables: { n: 150, flag: false }, extra_args: ["--shift-heading-level-by=1"] },
    };
    expect(applyPreset(new PandocArgs(), preset).options()).toEqual([
      "--template=/t/x/template.latex",
      "--resource-path=/t/x",
      "-V", "classoption=twocolumn",
      "-V", "classoption=11pt",
      "--csl=/c/apa.csl",
      "-V", "titlepage=false",
      "-V", "titlepage-color=06386e",
      "-V", "titlepage-text-color=FFFFFF",
      "-V", "titlepage-rule-color=FFFFFF",
      "-V", "colorlinks=false",
      "-V", "n=150",
      "-V", "flag=false",
      "--shift-heading-level-by=1",
    ]);
    expect(applyPreset(new PandocArgs(), preset, { includeCsl: false }).options()).not.toContain("--csl=/c/apa.csl");
  });
});

describe("applyMetadata and build", () => {
  test("fixed order, truthy only, keywords arrays joined", () => {
    const argv = applyMetadata(new PandocArgs(), {
      keywords: ["a", "b"],
      course_code: "CSC1",
      subject: "S",
      abstract: "Abs",
      subtitle: "",
      date: "2026-01-01",
      author: "Jane",
      title: "T",
    }).build("in.md", "out.pdf");
    expect(argv).toEqual([
      "pandoc",
      "-V", "title=T",
      "-V", "author=Jane",
      "-V", "date=2026-01-01",
      "-V", "abstract=Abs",
      "-V", "subject=S",
      "-V", "course_code=CSC1",
      "-V", "keywords=a, b",
      "in.md", "-o", "out.pdf",
    ]);
  });

  test("builder primitives", () => {
    const argv = new PandocArgs()
      .from("markdown")
      .bibliography("/r.bib", "citeproc")
      .csl("/s.csl")
      .listings()
      .simpleTables()
      .metadataFile("/m.yaml")
      .raw("--x")
      .build("i", "o");
    expect(argv).toEqual(["pandoc", "-f", "markdown", "--bibliography=/r.bib", "--citeproc", "--csl=/s.csl", "--listings", "--columns=1", "--metadata-file=/m.yaml", "--x", "i", "-o", "o"]);
  });
});

describe("usesIeee", () => {
  test("detects IEEE by name, class or template path only", () => {
    expect(usesIeee({ name: "ieee-custom" })).toBe(true);
    expect(usesIeee({ name: "x", template: { path: "a", document_class: "IEEEtran" } })).toBe(true);
    expect(usesIeee({ name: "x", template: { path: "my-ieee/template.latex" } })).toBe(true);
    expect(usesIeee({ name: "school-report", template: { path: "sit-uofg/template.latex" } })).toBe(false);
  });
});

describe("computeOutputPath", () => {
  test("default dir, explicit dir, last extension only", () => {
    expect(computeOutputPath("/a/b/doc.md")).toBe("/a/b/doc.pdf");
    expect(computeOutputPath("/a/b/doc.md", "/out", "docx")).toBe("/out/doc.docx");
    expect(computeOutputPath("/a/b/doc.v2.md", undefined, "html")).toBe("/a/b/doc.v2.html");
    expect(computeOutputPath("/a/b/noext", undefined, "pdf")).toBe("/a/b/noext.pdf");
  });

  test("raw behaviour for bare names and the workspace-relative fix", () => {
    expect(computeOutputPath("doc.md")).toBe("/doc.pdf");
    expect(computeOutputPath("doc.md", undefined, "pdf", "/ws")).toBe("/ws/doc.pdf");
    expect(computeOutputPath("sub/doc.md", "out", "pdf", "/ws")).toBe("/ws/out/doc.pdf");
    expect(computeOutputPath("/abs/doc.md", "/o", "pdf", "/ws")).toBe("/o/doc.pdf");
  });
});
