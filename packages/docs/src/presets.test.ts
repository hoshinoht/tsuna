import { afterEach, describe, expect, test } from "bun:test";
import { join } from "node:path";
import { BUILTIN_PRESETS, mergePresets, PresetStore } from "./presets";
import { makeFixture, type Fixture } from "./testing";

let fx: Fixture;
afterEach(() => fx?.cleanup());

describe("built-in presets", () => {
  test("exactly school-report and eisvogel, with the specified values", () => {
    expect(Object.keys(BUILTIN_PRESETS).sort()).toEqual(["eisvogel", "school-report"]);
    expect(BUILTIN_PRESETS["school-report"]).toEqual({
      name: "school-report",
      description: "SIT/UofG school report with optional logos and adjustable margins",
      template: { path: "sit-uofg/template.latex" },
      pdf_engine: "xelatex",
      logos: { sit: "assets/sit-logo.png", uofg: "assets/uofg-logo.png", both: "both" },
      style: { titlepage: true, toc: true, toc_own_page: true, colorlinks: true, linkcolor: "blue", number_sections: true },
      pandoc: {
        from: "markdown+smart+pipe_tables",
        variables: { "top-margin": "2.5cm", "bottom-margin": "2.5cm", "left-margin": "2.5cm", "right-margin": "2.5cm" },
      },
      required_fields: ["title"],
      optional_fields: ["author", "date", "course", "group", "project-title", "authors"],
    });
    expect(BUILTIN_PRESETS.eisvogel).toEqual({
      name: "eisvogel",
      description: "General purpose professional document (Eisvogel template)",
      template: { path: "eisvogel.latex" },
      pdf_engine: "xelatex",
      pandoc: { from: "markdown+smart" },
      style: { colorlinks: true, linkcolor: "blue" },
      required_fields: ["title"],
      optional_fields: ["author", "date", "subtitle"],
    });
  });

  test("the real bundled dir resolves both built-in templates", () => {
    fx = makeFixture({ realBundled: true });
    const store = new PresetStore(fx.roots);
    expect(store.load("school-report").resolved_template_path).toEndWith("pandoc/templates/sit-uofg/template.latex");
    expect(store.load("eisvogel").resolved_template_path).toEndWith("pandoc/templates/eisvogel.latex");
  });
});

describe("PresetStore.load", () => {
  test("built-ins win over same-named YAML files", () => {
    fx = makeFixture();
    fx.write(join(fx.roots.user, "presets", "eisvogel.yaml"), "name: eisvogel\npdf_engine: lualatex\n");
    expect(new PresetStore(fx.roots).load("eisvogel").pdf_engine).toBe("xelatex");
  });

  test("YAML presets load from project and organizations/", () => {
    fx = makeFixture();
    fx.write(join(fx.roots.user, "presets", "organizations", "acme.yaml"), "name: acme\ndescription: Acme\npdf_engine: lualatex\n");
    fx.write(join(fx.roots.project, "presets", "local.yaml"), "description: local one\n");
    const store = new PresetStore(fx.roots);
    const acme = store.load("acme");
    expect(acme.source).toBe("user");
    expect(acme.pdf_engine).toBe("lualatex");
    const local = store.load("local");
    expect(local.name).toBe("local");
    expect(local.source).toBe("project");
  });

  test("unknown preset lists the sorted union of names", () => {
    fx = makeFixture();
    fx.write(join(fx.roots.user, "presets", "zeta.yaml"), "name: zeta\n");
    fx.write(join(fx.roots.project, "presets", "alpha.yaml"), "name: alpha\n");
    expect(() => new PresetStore(fx.roots).load("nope")).toThrow("Preset 'nope' not found. Available presets: alpha, eisvogel, school-report, zeta");
  });

  test("extends merges per the rules", () => {
    fx = makeFixture();
    fx.write(
      join(fx.roots.user, "presets", "base.yaml"),
      [
        "name: base",
        "description: base desc",
        "pdf_engine: pdflatex",
        "template: { path: base/template.latex, document_class: article }",
        "citation: { style: apa, backend: citeproc }",
        "pandoc:",
        "  from: markdown",
        "  variables: { a: 1, b: two }",
        "  extra_args: [--p1]",
        "style: { toc: true, colorlinks: true }",
        "required_fields: [title, author]",
        "optional_fields: [date]",
      ].join("\n"),
    );
    fx.write(
      join(fx.roots.user, "presets", "child.yaml"),
      [
        "name: child",
        "extends: base",
        "pdf_engine: xelatex",
        "template: { path: child/template.latex }",
        "pandoc:",
        "  variables: { b: three, c: true }",
        "  extra_args: [--c1]",
        "style: { toc: false }",
        "required_fields: [title]",
      ].join("\n"),
    );
    const child = new PresetStore(fx.roots).load("child");
    expect(child.name).toBe("child");
    expect(child.description).toBe("base desc");
    expect(child.pdf_engine).toBe("xelatex");
    expect(child.template).toEqual({ path: "child/template.latex", document_class: "article" });
    expect(child.citation).toEqual({ style: "apa", backend: "citeproc" });
    expect(child.pandoc).toEqual({ from: "markdown", variables: { a: 1, b: "three", c: true }, extra_args: ["--p1", "--c1"] });
    expect(child.style).toEqual({ toc: false, colorlinks: true });
    expect(child.required_fields).toEqual(["title"]);
    expect(child.optional_fields).toEqual(["date"]);
  });

  test("YAML child extending a built-in inherits its config", () => {
    fx = makeFixture({ realBundled: true });
    fx.write(join(fx.roots.project, "presets", "my-report.yaml"), "name: my-report\nextends: school-report\ncitation: { backend: natbib, biblio_style: plainnat }\n");
    const preset = new PresetStore(fx.roots).load("my-report");
    expect(preset.template?.path).toBe("sit-uofg/template.latex");
    expect(preset.logos?.sit).toBe("assets/sit-logo.png");
    expect(preset.citation).toEqual({ backend: "natbib", biblio_style: "plainnat" });
    expect(preset.resolved_template_path).toBeDefined();
  });

  test("circular extends is reported", () => {
    fx = makeFixture();
    fx.write(join(fx.roots.user, "presets", "a.yaml"), "name: a\nextends: b\n");
    fx.write(join(fx.roots.user, "presets", "b.yaml"), "name: b\nextends: a\n");
    expect(() => new PresetStore(fx.roots).load("a")).toThrow("circular");
  });

  test("logo options: unknown throws, known resolves, both stays unset, no logos map ignores", () => {
    fx = makeFixture({ realBundled: true });
    const store = new PresetStore(fx.roots);
    expect(() => store.load("school-report", "acme")).toThrow("Logo option 'acme' not found. Available options: sit, uofg, both");
    expect(store.load("school-report", "sit").resolved_logo_path).toEndWith("pandoc/assets/sit-logo.png");
    expect(store.load("school-report", "uofg").resolved_logo_path).toEndWith("pandoc/assets/uofg-logo.png");
    expect(store.load("school-report", "both").resolved_logo_path).toBeUndefined();
    expect(store.load("eisvogel", "sit").resolved_logo_path).toBeUndefined();
  });

  test("a missing logo file leaves resolved_logo_path unset instead of failing", () => {
    fx = makeFixture();
    fx.write(join(fx.roots.bundled, "templates", "sit-uofg", "template.latex"));
    expect(new PresetStore(fx.roots).load("school-report", "sit").resolved_logo_path).toBeUndefined();
  });

  test("cache is keyed by (name, logo) and cleared explicitly", () => {
    fx = makeFixture();
    const store = new PresetStore(fx.roots);
    store.load("eisvogel");
    store.load("eisvogel");
    expect(store.cacheSize).toBe(1);
    store.load("school-report", "sit");
    store.load("school-report");
    expect(store.cacheSize).toBe(3);
    expect(store.load("eisvogel").resolved_template_path).toBeUndefined();
    const installed = fx.write(join(fx.roots.user, "templates", "eisvogel.latex"));
    expect(store.load("eisvogel").resolved_template_path).toBeUndefined();
    store.clearCache();
    expect(store.cacheSize).toBe(0);
    expect(store.load("eisvogel").resolved_template_path).toBe(installed);
  });

  test("cached results are copies", () => {
    fx = makeFixture();
    const store = new PresetStore(fx.roots);
    store.load("eisvogel").style!.linkcolor = "red";
    expect(store.load("eisvogel").style?.linkcolor).toBe("blue");
  });
});

describe("PresetStore.info and list", () => {
  test("info reports availability and merges the parent", () => {
    fx = makeFixture({ realBundled: true });
    fx.write(join(fx.roots.user, "presets", "cited.yaml"), "name: cited\nextends: eisvogel\ncitation: { style: apa, csl_file: apa.csl }\n");
    const store = new PresetStore(fx.roots);
    const info = store.info("cited");
    expect(info.source).toBe("user");
    expect(info.template_available).toBe(true);
    expect(info.csl_available).toBe(false);
    expect(info.config.pdf_engine).toBe("xelatex");
    expect(store.info("school-report").available_logos).toEqual(["sit", "uofg", "both"]);
    expect(() => store.info("nope")).toThrow(/^Preset 'nope' not found$/);
  });

  test("list merges files and built-ins, sorted by name", () => {
    fx = makeFixture();
    fx.write(join(fx.roots.user, "presets", "zed.yaml"), "description: user zed\n");
    fx.write(join(fx.roots.project, "presets", "eisvogel.yaml"), "description: shadow\n");
    fx.write(join(fx.roots.user, "presets", "broken.yaml"), ": : :\n  - [");
    expect(new PresetStore(fx.roots).list()).toEqual([
      { name: "broken", source: "user" },
      { name: "eisvogel", source: "project", description: "shadow" },
      { name: "school-report", source: "builtin", description: "SIT/UofG school report with optional logos and adjustable margins" },
      { name: "zed", source: "user", description: "user zed" },
    ]);
  });
});

describe("mergePresets", () => {
  test("keeps parent objects when the child omits them", () => {
    const merged = mergePresets({ name: "p", style: { toc: true }, pandoc: { from: "markdown" } }, { name: "c" });
    expect(merged).toEqual({ name: "c", style: { toc: true }, pandoc: { from: "markdown" } });
  });
});
