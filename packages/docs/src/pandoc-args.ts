import { dirname, isAbsolute, resolve } from "node:path";
import type { Preset, ResolvedPreset, VariableValue } from "./presets";

export type CitationProcessor = "citeproc" | "natbib" | "biblatex";

/** Accumulates a pandoc argv in call order; input and `-o output` are appended last. */
export class PandocArgs {
  private readonly args: string[] = [];

  from(format: string): this {
    this.args.push("-f", format);
    return this;
  }
  template(path: string): this {
    this.args.push(`--template=${path}`);
    return this;
  }
  resourcePath(dir: string): this {
    this.args.push(`--resource-path=${dir}`);
    return this;
  }
  pdfEngine(engine: string): this {
    this.args.push(`--pdf-engine=${engine}`);
    return this;
  }
  variable(key: string, value: VariableValue): this {
    this.args.push("-V", `${key}=${String(value)}`);
    return this;
  }
  bibliography(path: string, processor: CitationProcessor): this {
    this.args.push(`--bibliography=${path}`, `--${processor}`);
    return this;
  }
  csl(path: string): this {
    this.args.push(`--csl=${path}`);
    return this;
  }
  toc(): this {
    this.args.push("--toc");
    return this;
  }
  numberSections(): this {
    this.args.push("--number-sections");
    return this;
  }
  listings(): this {
    this.args.push("--listings");
    return this;
  }
  simpleTables(): this {
    this.args.push("--columns=1");
    return this;
  }
  metadataFile(path: string): this {
    this.args.push(`--metadata-file=${path}`);
    return this;
  }
  raw(...values: string[]): this {
    this.args.push(...values);
    return this;
  }
  /** Options only, without the leading `pandoc`, input or output. */
  options(): string[] {
    return [...this.args];
  }
  build(input: string, output: string): string[] {
    return ["pandoc", ...this.args, input, "-o", output];
  }
}

export interface ApplyPresetOptions {
  includeCsl?: boolean;
  inputFormatOverride?: string;
}

export function applyPreset(builder: PandocArgs, preset: ResolvedPreset, options: ApplyPresetOptions = {}): PandocArgs {
  const includeCsl = options.includeCsl ?? true;
  const from = options.inputFormatOverride ?? preset.pandoc?.from;
  if (from) builder.from(from);
  if (preset.resolved_template_path) {
    builder.template(preset.resolved_template_path);
    builder.resourcePath(dirname(preset.resolved_template_path));
  }
  // Class options are handed to templates as `classoption` (both bundled templates read it),
  // so a preset's declared class options actually reach the document class.
  for (const option of preset.template?.class_options ?? []) builder.variable("classoption", option);
  if (preset.pdf_engine) builder.pdfEngine(preset.pdf_engine);
  if (includeCsl && preset.resolved_csl_path) builder.csl(preset.resolved_csl_path);
  const style = preset.style;
  if (style) {
    if (style.titlepage !== undefined) builder.variable("titlepage", style.titlepage);
    if (style.titlepage_color) builder.variable("titlepage-color", style.titlepage_color);
    if (style.text_color) {
      builder.variable("titlepage-text-color", style.text_color);
      builder.variable("titlepage-rule-color", style.text_color);
    }
    if (style.toc) builder.toc();
    if (style.toc_own_page) builder.variable("toc-own-page", true);
    if (style.number_sections) builder.numberSections();
    if (style.colorlinks !== undefined) builder.variable("colorlinks", style.colorlinks);
    if (style.linkcolor) builder.variable("linkcolor", style.linkcolor);
  }
  for (const [key, value] of Object.entries(preset.pandoc?.variables ?? {})) builder.variable(key, value);
  for (const arg of preset.pandoc?.extra_args ?? []) builder.raw(arg);
  if (preset.resolved_logo_path) {
    builder.variable("logo", preset.resolved_logo_path);
    builder.variable("logo-width", 150);
  }
  return builder;
}

export interface Metadata {
  title?: string;
  author?: string;
  date?: string;
  subtitle?: string;
  abstract?: string;
  subject?: string;
  course_code?: string;
  keywords?: string | string[];
}

export function applyMetadata(builder: PandocArgs, metadata: Metadata): PandocArgs {
  for (const key of ["title", "author", "date", "subtitle", "abstract", "subject", "course_code"] as const) {
    const value = metadata[key];
    if (value) builder.variable(key, value);
  }
  const keywords = Array.isArray(metadata.keywords) ? metadata.keywords.join(", ") : metadata.keywords;
  if (keywords) builder.variable("keywords", keywords);
  return builder;
}

/** Presets targeting IEEEtran (user YAML presets only; none are built in) get `--columns=1`. */
export function usesIeee(preset: Preset): boolean {
  return preset.name.startsWith("ieee") || preset.template?.document_class === "IEEEtran" || (preset.template?.path ?? "").includes("ieee");
}

/**
 * `<outputDir or dirname(input)>/<basename(input) minus last extension>.<ext>`.
 * When `baseDir` is given, relative input and output paths are first resolved against it
 * (the tools pass the workspace root).
 */
export function computeOutputPath(inputPath: string, outputDir?: string, ext = "pdf", baseDir?: string): string {
  const input = baseDir && !isAbsolute(inputPath) ? resolve(baseDir, inputPath) : inputPath;
  const out = outputDir && baseDir && !isAbsolute(outputDir) ? resolve(baseDir, outputDir) : outputDir;
  const slash = input.lastIndexOf("/");
  const dir = slash >= 0 ? input.slice(0, slash) : "";
  const file = slash >= 0 ? input.slice(slash + 1) : input;
  const dot = file.lastIndexOf(".");
  const stem = dot > 0 ? file.slice(0, dot) : file;
  return `${out || dir}/${stem}.${ext}`;
}
