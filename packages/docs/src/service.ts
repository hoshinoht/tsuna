import { copyFileSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join, resolve } from "node:path";
import {
  applyCitationArgs,
  assertCitationRequirements,
  resolveBibliography,
  resolveCitationConfig,
  withoutCitations,
  type CitationConfig,
} from "./citations";
import {
  authorsYaml,
  draftPaths,
  frontMatter,
  generateDocId,
  isValidDocId,
  readRegistry,
  writeRegistry,
  type DraftEntry,
} from "./drafts";
import { installTemplate, type FetchLike, type InstallSource } from "./install";
import { natbibBibliographyArg, runBibtexPipeline } from "./latex";
import { applyMetadata, applyPreset, computeOutputPath, PandocArgs, usesIeee, type Metadata } from "./pandoc-args";
import { listCsl, listTemplates, resolveAsset, type SearchRoots } from "./paths";
import { PresetStore, type ResolvedPreset } from "./presets";
import { failureText, spawnRunner, type Runner } from "./process";

export interface DocsDeps {
  run: Runner;
  fetch: FetchLike;
  now: () => Date;
  random: () => number;
  tmpdir: () => string;
}

export const defaultDeps: DocsDeps = {
  run: spawnRunner,
  fetch: (url, init) => fetch(url, init),
  now: () => new Date(),
  random: Math.random,
  tmpdir,
};

export interface CallOptions {
  signal?: AbortSignal;
}

export const SIT_LOGO = "assets/sit-logo.png";
export const UOFG_LOGO = "assets/uofg-logo.png";
export const CONVERT_FORMATS = ["pdf", "docx", "odt", "markdown", "html", "latex"] as const;

export interface DraftArgs {
  title: string;
  preset: string;
  initial_content?: string;
  source_markdown?: string;
}

export interface CompileArgs {
  doc_id: string;
  output_path?: string;
  author?: string;
  date?: string;
  subtitle?: string;
  abstract?: string;
  keywords?: string;
  bibliography?: string;
  citation_style?: string;
  logo?: string;
  course?: string;
  project_title?: string;
  group?: string;
  version?: string;
  project_topic_id?: string;
  authors?: string;
}

export interface CreateArgs {
  input_path: string;
  preset: string;
  output_dir?: string;
  logo?: string;
  title?: string;
  author?: string;
  date?: string;
  subtitle?: string;
  abstract?: string;
  keywords?: string;
  bibliography?: string;
  citation_style?: string;
}

export interface ConvertArgs {
  input_path: string;
  output_format: (typeof CONVERT_FORMATS)[number];
  from_format?: string;
  output_dir?: string;
}

export interface CompileLatexArgs {
  file_path: string;
  output_dir?: string;
}

function signalOption(options: CallOptions): { signal?: AbortSignal } {
  return options.signal ? { signal: options.signal } : {};
}

export class DocsService {
  readonly presets: PresetStore;
  private readonly deps: DocsDeps;

  constructor(readonly roots: SearchRoots, deps: Partial<DocsDeps> = {}) {
    this.presets = new PresetStore(roots);
    this.deps = { ...defaultDeps, ...deps };
  }

  private get workspace(): string {
    return this.roots.workspace;
  }

  private fromWorkspace(path: string): string {
    return isAbsolute(path) ? path : resolve(this.workspace, path);
  }

  /** Logos are resolved lazily so a missing file only fails the compile that needs it. */
  requireAsset(relPath: string): string {
    const asset = resolveAsset(this.roots, relPath);
    if (!asset) throw new Error(`Required asset not found: ${relPath}`);
    return asset.path;
  }

  private applyLogoVariables(builder: PandocArgs, logo: string | undefined): void {
    if (logo !== "sit" && logo !== "uofg" && logo !== "both") return;
    const sit = logo === "uofg" ? undefined : this.requireAsset(SIT_LOGO);
    const uofg = logo === "sit" ? undefined : this.requireAsset(UOFG_LOGO);
    builder.variable("logo-mode", logo === "both" ? "both" : "single");
    if (sit) builder.variable("sit-logo", sit);
    if (uofg) builder.variable("uofg-logo", uofg);
  }

  private inputFormatOverride(config: CitationConfig, preset: ResolvedPreset): string | undefined {
    return config.style === "none" ? withoutCitations(preset.pandoc?.from) : undefined;
  }

  private async runPandocDirect(argv: string[], outputPath: string, failurePrefix: string, options: CallOptions): Promise<void> {
    mkdirSync(dirname(outputPath), { recursive: true });
    const result = await this.deps.run(argv, signalOption(options));
    if (result.code !== 0) throw new Error(`${failurePrefix}:\n${failureText(result)}`);
  }

  // docs_draft ---------------------------------------------------------------------------

  draft(args: DraftArgs): string {
    const paths = draftPaths(this.workspace);
    mkdirSync(paths.base, { recursive: true });
    const registry = readRegistry(paths.registry);
    const id = generateDocId(registry.drafts, this.deps.random, () => this.deps.now().getTime());
    const draftDir = join(paths.base, id);
    mkdirSync(draftDir, { recursive: true });

    const source = args.source_markdown ? this.fromWorkspace(args.source_markdown) : undefined;
    let content = "";
    if (args.initial_content) content = args.initial_content;
    else if (source && existsSync(source)) content = readFileSync(source, "utf8");
    const now = this.deps.now();
    if (!content.startsWith("---")) content = `${frontMatter(args.title, now)}${content}`;

    const draftPath = join(draftDir, "draft.md");
    const bibPath = join(draftDir, "refs.bib");
    writeFileSync(draftPath, content);
    const sourceBib = source ? join(dirname(source), "refs.bib") : undefined;
    if (sourceBib && existsSync(sourceBib)) copyFileSync(sourceBib, bibPath);
    else writeFileSync(bibPath, "");

    const stamp = now.toISOString();
    const entry: DraftEntry = {
      title: args.title,
      preset: args.preset,
      created_at: stamp,
      last_modified: stamp,
      ...(source ? { source_markdown: source } : {}),
      status: "draft",
    };
    writeFileSync(join(draftDir, "meta.json"), `${JSON.stringify(entry, null, 2)}\n`);
    registry.drafts[id] = entry;
    writeRegistry(paths.registry, registry);

    return [
      `Draft created: ${args.title}`,
      "",
      `Document ID: ${id}`,
      `Draft path: ${draftPath}`,
      `Bibliography path: ${bibPath}`,
      `Preset: ${args.preset}`,
      "",
      "Next steps:",
      `1. Edit the draft: ${draftPath}`,
      `2. Add references if needed: ${bibPath}`,
      `3. Compile when ready: docs_compile doc_id="${id}"`,
    ].join("\n");
  }

  // docs_list_drafts ---------------------------------------------------------------------

  listDrafts(): string {
    const paths = draftPaths(this.workspace);
    const entries = Object.entries(readRegistry(paths.registry).drafts);
    if (entries.length === 0) return "No active drafts. Create one with docs_draft.";
    const lines = ["Active document drafts:", ""];
    for (const [id, entry] of entries) {
      const draftPath = join(paths.base, id, "draft.md");
      lines.push(
        id,
        `  Title: ${entry.title}`,
        `  Preset: ${entry.preset}`,
        `  Status: ${existsSync(draftPath) ? entry.status : "missing"}`,
        `  Modified: ${String(entry.last_modified ?? "").slice(0, 10)}`,
        `  Path: ${draftPath}`,
        "",
      );
    }
    return lines.join("\n");
  }

  // docs_delete_draft --------------------------------------------------------------------

  deleteDraft(args: { doc_id: string }): string {
    if (!isValidDocId(args.doc_id)) return `Invalid document ID format: ${args.doc_id}`;
    const paths = draftPaths(this.workspace);
    const registry = readRegistry(paths.registry);
    if (!Object.hasOwn(registry.drafts, args.doc_id)) return `Draft not found: ${args.doc_id}`;
    rmSync(join(paths.base, args.doc_id), { recursive: true, force: true });
    delete registry.drafts[args.doc_id];
    writeRegistry(paths.registry, registry);
    return `Deleted draft: ${args.doc_id}`;
  }

  // docs_compile -------------------------------------------------------------------------

  async compile(args: CompileArgs, options: CallOptions = {}): Promise<string> {
    if (!isValidDocId(args.doc_id)) return `Invalid document ID format: ${args.doc_id}`;
    const paths = draftPaths(this.workspace);
    const registry = readRegistry(paths.registry);
    const entry = Object.hasOwn(registry.drafts, args.doc_id) ? registry.drafts[args.doc_id] : undefined;
    if (!entry) return `Draft not found: ${args.doc_id}. Use docs_list_drafts to see available drafts.`;
    const draftDir = join(paths.base, args.doc_id);
    const draftPath = join(draftDir, "draft.md");
    if (!existsSync(draftPath)) return `Draft file missing: ${draftPath}`;

    // Marked compiled before compiling (kept behaviour): a failed compile still shows as compiled.
    entry.last_modified = this.deps.now().toISOString();
    entry.status = "compiled";
    writeRegistry(paths.registry, registry);

    const outputPath = args.output_path ? this.fromWorkspace(args.output_path) : join(draftDir, `${args.doc_id}.pdf`);
    const preset = this.presets.load(entry.preset, args.logo);
    const bibliography = resolveBibliography(args.bibliography, draftDir);
    const citation = resolveCitationConfig(this.roots, args.citation_style, bibliography !== undefined, preset);
    assertCitationRequirements(draftPath, bibliography, citation);
    if (preset.template?.path && !preset.resolved_template_path) {
      throw new Error(`Template '${preset.template.path}' not found. Use docs_templates_install.`);
    }

    const natbib = citation.backend === "natbib" && bibliography !== undefined;
    const builder = new PandocArgs();
    applyPreset(builder, preset, { includeCsl: false, inputFormatOverride: this.inputFormatOverride(citation, preset) });
    builder.resourcePath(draftDir);
    builder.listings();
    if (usesIeee(preset)) builder.simpleTables();
    applyMetadata(builder, { author: args.author, date: args.date, subtitle: args.subtitle, abstract: args.abstract, keywords: args.keywords });
    applyCitationArgs(builder, natbib && bibliography ? natbibBibliographyArg(bibliography) : bibliography, citation);
    this.applyLogoVariables(builder, args.logo);
    const school: Array<[string, string | undefined]> = [
      ["course", args.course],
      ["project-title", args.project_title],
      ["group", args.group],
      ["version", args.version],
      ["project-topic-id", args.project_topic_id],
    ];
    for (const [key, value] of school) if (value) builder.variable(key, value);

    let authorsFile: string | undefined;
    try {
      if (args.authors !== undefined) {
        const yaml = authorsYaml(args.authors);
        authorsFile = join(draftDir, `authors-${this.deps.now().getTime()}.yaml`);
        try {
          writeFileSync(authorsFile, yaml);
        } catch (error) {
          authorsFile = undefined;
          throw new Error(`Invalid authors JSON: ${error instanceof Error ? error.message : String(error)}`);
        }
        builder.metadataFile(authorsFile);
      }

      if (natbib && bibliography) {
        await runBibtexPipeline({
          run: this.deps.run,
          pandocOptions: builder.options(),
          inputPath: draftPath,
          outputPath,
          baseDir: draftDir,
          name: args.doc_id,
          bibliography,
          resourceDir: draftDir,
          pandocFailurePrefix: "Compilation failed",
          ...(preset.resolved_template_path ? { templatePath: preset.resolved_template_path } : {}),
          ...signalOption(options),
        });
      } else {
        await this.runPandocDirect(builder.build(draftPath, outputPath), outputPath, "Compilation failed", options);
      }
    } finally {
      if (authorsFile) rmSync(authorsFile, { force: true });
    }

    return [`Compiled: ${outputPath}`, `Document: ${entry.title}`, `Preset: ${entry.preset}`].join("\n");
  }

  // docs_create --------------------------------------------------------------------------

  async create(args: CreateArgs, options: CallOptions = {}): Promise<string> {
    const inputPath = this.fromWorkspace(args.input_path);
    const inputDir = dirname(inputPath);
    const preset = this.presets.load(args.preset, args.logo);
    if (preset.template?.path && !preset.resolved_template_path) {
      throw new Error(`Template '${preset.template.path}' not found. Use docs_templates_install.`);
    }
    const bibliography = resolveBibliography(args.bibliography, inputDir);
    const citation = resolveCitationConfig(this.roots, args.citation_style, bibliography !== undefined, preset);
    assertCitationRequirements(inputPath, bibliography, citation);
    const outputPath = computeOutputPath(inputPath, args.output_dir, "pdf", this.workspace);
    const natbib = citation.backend === "natbib" && bibliography !== undefined;

    const builder = new PandocArgs();
    applyPreset(builder, preset, { includeCsl: false, inputFormatOverride: this.inputFormatOverride(citation, preset) });
    builder.resourcePath(inputDir);
    builder.listings();
    const metadata: Metadata = { title: args.title, author: args.author, date: args.date, subtitle: args.subtitle, abstract: args.abstract, keywords: args.keywords };
    applyMetadata(builder, metadata);
    // Presets with a logo map (school-report) get the same logo variables as docs_compile.
    if (args.logo && preset.logos && Object.hasOwn(preset.logos, args.logo)) this.applyLogoVariables(builder, args.logo);
    applyCitationArgs(builder, natbib && bibliography ? natbibBibliographyArg(bibliography) : bibliography, citation);
    if (usesIeee(preset)) builder.simpleTables();

    if (natbib && bibliography) {
      const stem = basename(outputPath).replace(/\.pdf$/, "");
      await runBibtexPipeline({
        run: this.deps.run,
        pandocOptions: builder.options(),
        inputPath,
        outputPath,
        baseDir: dirname(outputPath),
        name: stem,
        bibliography,
        resourceDir: inputDir,
        pandocFailurePrefix: "Failed",
        ...(preset.resolved_template_path ? { templatePath: preset.resolved_template_path } : {}),
        ...signalOption(options),
      });
    } else {
      await this.runPandocDirect(builder.build(inputPath, outputPath), outputPath, "Failed", options);
    }
    return `Created: ${outputPath}\nPreset: ${preset.name}`;
  }

  // docs_convert -------------------------------------------------------------------------

  async convert(args: ConvertArgs, options: CallOptions = {}): Promise<string> {
    const inputPath = this.fromWorkspace(args.input_path);
    const outputPath = computeOutputPath(inputPath, args.output_dir, args.output_format, this.workspace);
    const argv = ["pandoc"];
    if (args.from_format) argv.push("-f", args.from_format);
    argv.push(`--resource-path=${dirname(inputPath)}`, inputPath, "-o", outputPath);
    await this.runPandocDirect(argv, outputPath, "Pandoc failed", options);
    return `Converted ${args.input_path} to ${outputPath}`;
  }

  // docs_compile_latex -------------------------------------------------------------------

  async compileLatex(args: CompileLatexArgs, options: CallOptions = {}): Promise<string> {
    if (!args.file_path.endsWith(".tex")) throw new Error("Input file must be a .tex file");
    const filePath = this.fromWorkspace(args.file_path);
    const outDir = args.output_dir ? this.fromWorkspace(args.output_dir) : dirname(filePath);
    mkdirSync(outDir, { recursive: true });
    const result = await this.deps.run(["pdflatex", "-output-directory", outDir, "-interaction=nonstopmode", filePath], {
      cwd: dirname(filePath),
      ...signalOption(options),
    });
    if (result.code !== 0) throw new Error(`Compilation failed:\n${failureText(result)}`);
    return `Compiled ${args.file_path} to PDF.`;
  }

  // docs_presets_list / docs_presets_show -----------------------------------------------

  presetsList(): string {
    const all = this.presets.list();
    if (all.length === 0) return "No presets available.";
    const lines = ["Available presets:", ""];
    const builtins = all.filter((preset) => preset.source === "builtin");
    const users = all.filter((preset) => preset.source === "user");
    const projects = all.filter((preset) => preset.source === "project");
    if (builtins.length > 0) {
      lines.push("Built-in:");
      for (const preset of builtins) lines.push(preset.description ? `  - ${preset.name}: ${preset.description}` : `  - ${preset.name}`);
      lines.push("");
    }
    if (users.length > 0) {
      lines.push("User:");
      for (const preset of users) lines.push(`  - ${preset.name}`);
    }
    if (projects.length > 0) {
      lines.push("Project:");
      for (const preset of projects) lines.push(`  - ${preset.name}`);
    }
    return lines.join("\n");
  }

  presetsShow(args: { preset_name: string }): string {
    const info = this.presets.info(args.preset_name);
    const config = info.config;
    const lines = [`Preset: ${config.name}`];
    if (config.description) lines.push(`Description: ${config.description}`);
    lines.push(`Source: ${info.source}`);
    if (config.template) lines.push(`Template: ${config.template.path} (${info.template_available ? "installed" : "not installed"})`);
    if (config.citation?.style || config.citation?.backend) lines.push(`Citations: ${config.citation.style ?? "default"} via ${config.citation.backend ?? "citeproc"}`);
    if (config.logos) lines.push(`Logos: ${Object.keys(config.logos).join(", ")}`);
    if (config.required_fields && config.required_fields.length > 0) lines.push(`Required: ${config.required_fields.join(", ")}`);
    return lines.join("\n");
  }

  // docs_templates_list / docs_templates_install ----------------------------------------

  templatesList(): string {
    const templates = listTemplates(this.roots);
    const csl = listCsl(this.roots);
    const lines = ["Templates:"];
    if (templates.length === 0) lines.push("  (none - use docs_templates_install)");
    for (const template of templates) lines.push(`  - ${template.name} (${template.source})`);
    lines.push("", "CSL Styles:");
    if (csl.length === 0) lines.push("  (none)");
    for (const style of csl) lines.push(`  - ${style.name}`);
    return lines.join("\n");
  }

  async templatesInstall(args: { source: InstallSource; force?: boolean }, options: CallOptions = {}): Promise<string> {
    const message = await installTemplate(this.roots, args.source, args.force === true, {
      fetch: this.deps.fetch,
      run: this.deps.run,
      tmpdir: this.deps.tmpdir,
      ...signalOption(options),
    });
    this.presets.clearCache();
    return message;
  }
}
