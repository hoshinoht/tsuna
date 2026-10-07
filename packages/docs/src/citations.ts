import { existsSync, readFileSync } from "node:fs";
import { isAbsolute, join, resolve } from "node:path";
import type { PandocArgs } from "./pandoc-args";
import { resolveCsl, type SearchRoots } from "./paths";
import type { CitationBackend, ResolvedPreset } from "./presets";

export const CITATION_STYLES = ["default", "none", "ieee", "apa", "acm"] as const;
export type CitationStyle = (typeof CITATION_STYLES)[number];

export interface CitationConfig {
  style: string;
  backend: CitationBackend;
  cslPath?: string;
  biblioStyle?: string;
}

export function normalizeCitationStyle(value: string | undefined): CitationStyle {
  if (value === undefined) return "default";
  const lowered = value.toLowerCase();
  if ((CITATION_STYLES as readonly string[]).includes(lowered)) return lowered as CitationStyle;
  throw new Error(`Unsupported citation style '${value}'. Supported values: ${CITATION_STYLES.join(", ")}`);
}

/** Explicit path (resolved against baseDir) or a non-empty baseDir/refs.bib. */
export function resolveBibliography(explicit: string | undefined, baseDir: string): string | undefined {
  if (explicit) {
    const path = isAbsolute(explicit) ? explicit : resolve(baseDir, explicit);
    if (!existsSync(path)) throw new Error(`Bibliography file not found: ${path}`);
    return path;
  }
  const fallback = join(baseDir, "refs.bib");
  try {
    if (readFileSync(fallback, "utf8").trim().length > 0) return fallback;
  } catch {
    return undefined;
  }
  return undefined;
}

const CSL_BY_STYLE: Record<"ieee" | "apa" | "acm", { file: string; source: string }> = {
  ieee: { file: "ieee.csl", source: "csl-ieee" },
  apa: { file: "apa.csl", source: "csl-apa" },
  acm: { file: "acm-sig-proceedings.csl", source: "csl-acm" },
};

function requireCsl(roots: SearchRoots, style: "ieee" | "apa" | "acm"): string {
  const { file, source } = CSL_BY_STYLE[style];
  const csl = resolveCsl(roots, file);
  if (!csl) throw new Error(`CSL style '${file}' not found. Run: docs_templates_install ${source}`);
  return csl.path;
}

export function resolveCitationConfig(
  roots: SearchRoots,
  styleInput: string | undefined,
  hasBibliography: boolean,
  preset?: ResolvedPreset,
): CitationConfig {
  const style = normalizeCitationStyle(styleInput);
  const citation = preset?.citation;
  if (style === "none") return { style, backend: "none" };
  if (style === "default") {
    if (!hasBibliography) return { style, backend: "none" };
    if (citation?.backend === "natbib") return { style: citation.style ?? "ieee", backend: "natbib", biblioStyle: citation.biblio_style ?? "IEEEtranN" };
    if (citation?.backend === "biblatex") return { style: citation.style ?? style, backend: "biblatex", ...(citation.biblio_style ? { biblioStyle: citation.biblio_style } : {}) };
    if (preset?.resolved_csl_path) return { style: citation?.style ?? style, backend: "citeproc", cslPath: preset.resolved_csl_path };
    if (citation?.csl_file) {
      throw new Error(`Preset '${preset?.name}' requires CSL style '${citation.csl_file}', but it is not installed or could not be resolved.`);
    }
    return { style, backend: "citeproc" };
  }
  if (!hasBibliography) throw new Error(`citation_style='${style}' requires a bibliography. Pass bibliography=... or create refs.bib.`);
  if (style === "ieee" && citation?.backend === "natbib") return { style, backend: "natbib", biblioStyle: citation.biblio_style ?? "IEEEtranN" };
  return { style, backend: "citeproc", cslPath: requireCsl(roots, style) };
}

const KEY = "[A-Za-z0-9][\\w:.#$%&+?<>~/-]*";
const CITATION_PATTERN = new RegExp(`\\[[^\\]\\n]*-?@${KEY}[^\\]\\n]*\\]|(?:^|[\\s(])-?@${KEY}`, "m");

export function containsCitations(markdown: string): boolean {
  return CITATION_PATTERN.test(markdown);
}

export function assertCitationRequirements(inputPath: string, bibliography: string | undefined, config: CitationConfig): void {
  if (config.style === "none") return;
  if (config.backend !== "none" && bibliography) return;
  let text: string;
  try {
    text = readFileSync(inputPath, "utf8");
  } catch {
    return; // A missing input is reported by pandoc itself.
  }
  if (containsCitations(text)) {
    throw new Error(`Citations found in ${inputPath} but no bibliography is available. Pass bibliography=... or add refs.bib.`);
  }
}

export function applyCitationArgs(builder: PandocArgs, bibliography: string | undefined, config: CitationConfig): PandocArgs {
  if (!bibliography || config.backend === "none") return builder;
  builder.bibliography(bibliography, config.backend);
  if (config.cslPath) builder.csl(config.cslPath);
  if (config.biblioStyle) builder.variable("biblio-style", config.biblioStyle);
  return builder;
}

/** Disables the pandoc `citations` extension on an input format. */
export function withoutCitations(from: string | undefined): string {
  const base = (from ?? "markdown").replace(/[+-]citations/g, "");
  return `${base}-citations`;
}
