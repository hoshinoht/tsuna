import { afterEach, describe, expect, test } from "bun:test";
import { join } from "node:path";
import {
  applyCitationArgs,
  assertCitationRequirements,
  containsCitations,
  normalizeCitationStyle,
  resolveBibliography,
  resolveCitationConfig,
  withoutCitations,
} from "./citations";
import { PandocArgs } from "./pandoc-args";
import type { ResolvedPreset } from "./presets";
import { makeFixture, type Fixture } from "./testing";

let fx: Fixture;
afterEach(() => fx?.cleanup());

const natbibPreset: ResolvedPreset = { name: "nat", source: "user", citation: { backend: "natbib" } };
const biblatexPreset: ResolvedPreset = { name: "bl", source: "user", citation: { backend: "biblatex", biblio_style: "authoryear" } };

describe("normalizeCitationStyle", () => {
  test("case-insensitive, default when missing, clear error otherwise", () => {
    expect(normalizeCitationStyle(undefined)).toBe("default");
    expect(normalizeCitationStyle("IEEE")).toBe("ieee");
    expect(() => normalizeCitationStyle("mla")).toThrow("Unsupported citation style 'mla'. Supported values: default, none, ieee, apa, acm");
  });
});

describe("resolveCitationConfig", () => {
  test("none and default without a bibliography", () => {
    fx = makeFixture();
    expect(resolveCitationConfig(fx.roots, "none", true, natbibPreset)).toEqual({ style: "none", backend: "none" });
    expect(resolveCitationConfig(fx.roots, undefined, false, natbibPreset)).toEqual({ style: "default", backend: "none" });
  });

  test("default with natbib, biblatex, CSL presets and plain citeproc", () => {
    fx = makeFixture();
    expect(resolveCitationConfig(fx.roots, "default", true, natbibPreset)).toEqual({ style: "ieee", backend: "natbib", biblioStyle: "IEEEtranN" });
    expect(resolveCitationConfig(fx.roots, "default", true, { ...natbibPreset, citation: { backend: "natbib", style: "x", biblio_style: "plainnat" } })).toEqual({
      style: "x",
      backend: "natbib",
      biblioStyle: "plainnat",
    });
    expect(resolveCitationConfig(fx.roots, "default", true, biblatexPreset)).toEqual({ style: "default", backend: "biblatex", biblioStyle: "authoryear" });
    const csl: ResolvedPreset = { name: "c", source: "user", citation: { style: "acm", csl_file: "acm.csl" }, resolved_csl_path: "/x/acm.csl" };
    expect(resolveCitationConfig(fx.roots, "default", true, csl)).toEqual({ style: "acm", backend: "citeproc", cslPath: "/x/acm.csl" });
    expect(() => resolveCitationConfig(fx.roots, "default", true, { ...csl, resolved_csl_path: undefined })).toThrow(
      "Preset 'c' requires CSL style 'acm.csl', but it is not installed or could not be resolved.",
    );
    expect(resolveCitationConfig(fx.roots, "default", true, { name: "p", source: "builtin" })).toEqual({ style: "default", backend: "citeproc" });
  });

  test("explicit styles need a bibliography and their CSL", () => {
    fx = makeFixture();
    expect(() => resolveCitationConfig(fx.roots, "apa", false)).toThrow("citation_style='apa' requires a bibliography. Pass bibliography=... or create refs.bib.");
    expect(() => resolveCitationConfig(fx.roots, "ieee", true)).toThrow("CSL style 'ieee.csl' not found. Run: docs_templates_install csl-ieee");
    expect(() => resolveCitationConfig(fx.roots, "apa", true)).toThrow("CSL style 'apa.csl' not found. Run: docs_templates_install csl-apa");
    expect(() => resolveCitationConfig(fx.roots, "ACM", true)).toThrow("CSL style 'acm-sig-proceedings.csl' not found. Run: docs_templates_install csl-acm");
    const ieee = fx.write(join(fx.roots.user, "csl", "ieee.csl"));
    expect(resolveCitationConfig(fx.roots, "ieee", true)).toEqual({ style: "ieee", backend: "citeproc", cslPath: ieee });
    expect(resolveCitationConfig(fx.roots, "ieee", true, natbibPreset)).toEqual({ style: "ieee", backend: "natbib", biblioStyle: "IEEEtranN" });
  });
});

describe("containsCitations", () => {
  test.each(["[@a]", "[see @a, p. 3]", "[-@a]", "@a at line start", "text (@a) here", "see @smith2020"])("matches %p", (text) => {
    expect(containsCitations(`intro\n${text}\n`)).toBe(true);
  });
  test.each(["mail a@b.com today", "no at signs", "trailing @ alone"])("does not match %p", (text) => {
    expect(containsCitations(text)).toBe(false);
  });
});

describe("bibliography resolution and requirement check", () => {
  test("refs.bib must be non-empty; explicit paths resolve against baseDir", () => {
    fx = makeFixture();
    const base = join(fx.dir, "doc");
    fx.write(join(base, "refs.bib"), "  \n\n");
    expect(resolveBibliography(undefined, base)).toBeUndefined();
    const refs = fx.write(join(base, "refs.bib"), "@misc{a, title={A}}");
    expect(resolveBibliography(undefined, base)).toBe(refs);
    const other = fx.write(join(base, "bib", "other.bib"), "x");
    expect(resolveBibliography("bib/other.bib", base)).toBe(other);
    expect(() => resolveBibliography("missing.bib", base)).toThrow(`Bibliography file not found: ${join(base, "missing.bib")}`);
  });

  test("throws only when citations exist without a usable bibliography", () => {
    fx = makeFixture();
    const cited = fx.write("cited.md", "As shown [@a].");
    const plain = fx.write("plain.md", "No citations, mail a@b.com.");
    expect(() => assertCitationRequirements(cited, undefined, { style: "default", backend: "none" })).toThrow(
      `Citations found in ${cited} but no bibliography is available. Pass bibliography=... or add refs.bib.`,
    );
    expect(() => assertCitationRequirements(cited, undefined, { style: "none", backend: "none" })).not.toThrow();
    expect(() => assertCitationRequirements(cited, "/r.bib", { style: "default", backend: "citeproc" })).not.toThrow();
    expect(() => assertCitationRequirements(plain, undefined, { style: "default", backend: "none" })).not.toThrow();
    expect(() => assertCitationRequirements(join(fx.dir, "missing.md"), undefined, { style: "default", backend: "none" })).not.toThrow();
  });
});

describe("applyCitationArgs and withoutCitations", () => {
  test("adds args only with a bibliography and a backend", () => {
    expect(applyCitationArgs(new PandocArgs(), "/r.bib", { style: "apa", backend: "citeproc", cslPath: "/apa.csl" }).options()).toEqual([
      "--bibliography=/r.bib",
      "--citeproc",
      "--csl=/apa.csl",
    ]);
    expect(applyCitationArgs(new PandocArgs(), "refs.bib", { style: "ieee", backend: "natbib", biblioStyle: "IEEEtranN" }).options()).toEqual([
      "--bibliography=refs.bib",
      "--natbib",
      "-V",
      "biblio-style=IEEEtranN",
    ]);
    expect(applyCitationArgs(new PandocArgs(), undefined, { style: "default", backend: "citeproc" }).options()).toEqual([]);
    expect(applyCitationArgs(new PandocArgs(), "/r.bib", { style: "none", backend: "none" }).options()).toEqual([]);
  });

  test("strips and disables the citations extension", () => {
    expect(withoutCitations("markdown+citations+smart")).toBe("markdown+smart-citations");
    expect(withoutCitations(undefined)).toBe("markdown-citations");
    expect(withoutCitations("markdown+smart+pipe_tables")).toBe("markdown+smart+pipe_tables-citations");
  });
});
