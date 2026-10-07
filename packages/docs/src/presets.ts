import { readFileSync } from "node:fs";
import { parse as parseYaml } from "yaml";
import { listPresetFiles, resolveAsset, resolveCsl, resolvePreset, resolveTemplate, type RootSource, type SearchRoots } from "./paths";

export type PdfEngine = "xelatex" | "pdflatex" | "lualatex";
export type CitationBackend = "none" | "citeproc" | "natbib" | "biblatex";
export type VariableValue = string | number | boolean;

export interface Preset {
  name: string;
  description?: string;
  extends?: string;
  template?: {
    path: string;
    document_class?: string;
    class_options?: string[];
  };
  pdf_engine?: PdfEngine;
  citation?: {
    style?: string;
    csl_file?: string;
    backend?: CitationBackend;
    biblio_style?: string;
  };
  pandoc?: {
    from?: string;
    variables?: Record<string, VariableValue>;
    extra_args?: string[];
  };
  logos?: Record<string, string>;
  style?: {
    titlepage?: boolean;
    titlepage_color?: string;
    text_color?: string;
    toc?: boolean;
    toc_own_page?: boolean;
    number_sections?: boolean;
    colorlinks?: boolean;
    linkcolor?: string;
  };
  required_fields?: string[];
  optional_fields?: string[];
}

export type PresetSource = "builtin" | RootSource;

export interface ResolvedPreset extends Preset {
  source: PresetSource;
  source_path?: string;
  resolved_template_path?: string;
  resolved_csl_path?: string;
  resolved_logo_path?: string;
}

export interface PresetInfo {
  config: Preset;
  source: PresetSource;
  path?: string;
  available_logos: string[];
  template_available: boolean;
  csl_available: boolean;
}

export const BUILTIN_PRESETS: Readonly<Record<string, Preset>> = Object.freeze({
  "school-report": {
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
  },
  eisvogel: {
    name: "eisvogel",
    description: "General purpose professional document (Eisvogel template)",
    template: { path: "eisvogel.latex" },
    pdf_engine: "xelatex",
    pandoc: { from: "markdown+smart" },
    style: { colorlinks: true, linkcolor: "blue" },
    required_fields: ["title"],
    optional_fields: ["author", "date", "subtitle"],
  },
});

function clone<T>(value: T): T {
  return structuredClone(value);
}

export function mergePresets(parent: Preset, child: Preset): Preset {
  const merged: Preset = { ...parent, ...child };
  for (const key of ["template", "citation", "logos", "style"] as const) {
    if (child[key] !== undefined) (merged as unknown as Record<string, unknown>)[key] = { ...(parent[key] ?? {}), ...child[key] };
    else if (parent[key] !== undefined) (merged as unknown as Record<string, unknown>)[key] = parent[key];
  }
  if (child.pandoc !== undefined) {
    const pandoc: NonNullable<Preset["pandoc"]> = { ...(parent.pandoc ?? {}), ...child.pandoc };
    if (parent.pandoc?.variables || child.pandoc.variables) pandoc.variables = { ...(parent.pandoc?.variables ?? {}), ...(child.pandoc.variables ?? {}) };
    if (parent.pandoc?.extra_args || child.pandoc.extra_args) pandoc.extra_args = [...(parent.pandoc?.extra_args ?? []), ...(child.pandoc.extra_args ?? [])];
    merged.pandoc = pandoc;
  } else if (parent.pandoc !== undefined) {
    merged.pandoc = parent.pandoc;
  }
  merged.required_fields = child.required_fields ?? parent.required_fields;
  merged.optional_fields = child.optional_fields ?? parent.optional_fields;
  if (merged.required_fields === undefined) delete merged.required_fields;
  if (merged.optional_fields === undefined) delete merged.optional_fields;
  return merged;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parsePresetFile(path: string, fallbackName: string): Preset {
  const raw = readFileSync(path, "utf8");
  let parsed: unknown;
  try {
    parsed = parseYaml(raw);
  } catch (error) {
    throw new Error(`Preset file ${path} is not valid YAML: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (!isRecord(parsed)) throw new Error(`Preset file ${path} must contain a YAML mapping`);
  const preset = parsed as unknown as Preset;
  if (typeof preset.name !== "string" || preset.name.length === 0) preset.name = fallbackName;
  if (preset.extends !== undefined && typeof preset.extends !== "string") throw new Error(`Preset file ${path}: 'extends' must be a string`);
  return preset;
}

export class PresetStore {
  private readonly cache = new Map<string, ResolvedPreset>();

  constructor(private readonly roots: SearchRoots) {}

  clearCache(): void {
    this.cache.clear();
  }

  get cacheSize(): number {
    return this.cache.size;
  }

  availableNames(): string[] {
    const names = new Set([...Object.keys(BUILTIN_PRESETS), ...listPresetFiles(this.roots).map((entry) => entry.name)]);
    return [...names].sort();
  }

  /** Loads the raw (merged) preset config. Built-ins always win over same-named files. */
  private loadRaw(name: string, notFound: (name: string) => Error, chain: string[] = []): { config: Preset; source: PresetSource; path?: string } {
    if (chain.includes(name)) throw new Error(`Preset '${name}' has a circular 'extends' chain: ${[...chain, name].join(" -> ")}`);
    let config: Preset;
    let source: PresetSource;
    let path: string | undefined;
    const builtin = BUILTIN_PRESETS[name];
    if (builtin) {
      config = clone(builtin);
      source = "builtin";
    } else {
      const resolved = resolvePreset(this.roots, name);
      if (!resolved) throw notFound(name);
      config = parsePresetFile(resolved.path, name.replace(/\.yaml$/, ""));
      source = resolved.source;
      path = resolved.path;
    }
    if (config.extends) {
      const parent = this.loadRaw(config.extends, notFound, [...chain, name]);
      config = mergePresets(parent.config, config);
    }
    return { config, source, ...(path ? { path } : {}) };
  }

  load(name: string, logoOption?: string): ResolvedPreset {
    const key = `${name}\u0000${logoOption ?? ""}`;
    const cached = this.cache.get(key);
    if (cached) return clone(cached);
    const { config, source, path } = this.loadRaw(name, (missing) => new Error(`Preset '${missing}' not found. Available presets: ${this.availableNames().join(", ")}`));
    const resolved: ResolvedPreset = { ...config, source, ...(path ? { source_path: path } : {}) };
    if (config.template?.path) {
      const template = resolveTemplate(this.roots, config.template.path);
      if (template) resolved.resolved_template_path = template.path;
    }
    if (config.citation?.csl_file) {
      const csl = resolveCsl(this.roots, config.citation.csl_file);
      if (csl) resolved.resolved_csl_path = csl.path;
    }
    if (logoOption && config.logos) {
      const value = config.logos[logoOption];
      if (value === undefined) throw new Error(`Logo option '${logoOption}' not found. Available options: ${Object.keys(config.logos).join(", ")}`);
      const asset = resolveAsset(this.roots, value);
      if (asset) resolved.resolved_logo_path = asset.path;
    }
    this.cache.set(key, resolved);
    return clone(resolved);
  }

  info(name: string): PresetInfo {
    const { config, source, path } = this.loadRaw(name, (missing) => new Error(`Preset '${missing}' not found`));
    return {
      config,
      source,
      ...(path ? { path } : {}),
      available_logos: config.logos ? Object.keys(config.logos) : [],
      template_available: config.template?.path ? resolveTemplate(this.roots, config.template.path) !== undefined : false,
      csl_available: config.citation?.csl_file ? resolveCsl(this.roots, config.citation.csl_file) !== undefined : false,
    };
  }

  /** Combined listing for docs_presets_list, sorted by name. */
  list(): Array<{ name: string; source: PresetSource; description?: string }> {
    const out: Array<{ name: string; source: PresetSource; description?: string }> = [];
    const seen = new Set<string>();
    for (const file of listPresetFiles(this.roots)) {
      seen.add(file.name);
      let description: string | undefined;
      try {
        const parsed = parseYaml(readFileSync(file.path, "utf8")) as unknown;
        if (isRecord(parsed) && typeof parsed.description === "string") description = parsed.description;
      } catch {
        description = undefined;
      }
      out.push({ name: file.name, source: file.source, ...(description ? { description } : {}) });
    }
    for (const [name, preset] of Object.entries(BUILTIN_PRESETS)) {
      if (seen.has(name)) continue;
      out.push({ name, source: "builtin", ...(preset.description ? { description: preset.description } : {}) });
    }
    return out.sort((a, b) => a.name.localeCompare(b.name));
  }
}
