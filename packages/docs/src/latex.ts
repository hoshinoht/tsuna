import { copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { basename, dirname, extname, join } from "node:path";
import { failureText, type RunOptions, type Runner } from "./process";

export interface BibtexPipelineInput {
  run: Runner;
  /**
   * pandoc options (no leading `pandoc`, no input/output). The bibliography must be referenced
   * by its basename (see `natbibBibliographyArg`); the file itself is staged into the build dir.
   */
  pandocOptions: string[];
  inputPath: string;
  outputPath: string;
  baseDir: string;
  name: string;
  bibliography: string;
  templatePath?: string;
  resourceDir?: string;
  signal?: AbortSignal;
  /** Prefix for pandoc failures, e.g. "Compilation failed" or "Failed". */
  pandocFailurePrefix: string;
}

export interface BibtexPipelineResult {
  buildDir: string;
  texPath: string;
  pdfPath: string;
  commands: string[][];
}

/**
 * The .bib is staged next to the .tex and referenced by basename: pandoc escapes `--` in
 * absolute paths inside \bibliography{}, which would break bibtex lookups.
 */
export function natbibBibliographyArg(bibliography: string): string {
  return basename(bibliography);
}

const SUPPORT_EXTENSIONS = new Set([".cls", ".bst", ".sty"]);

export function searchPath(entries: Array<string | undefined>, existing: string | undefined): string {
  const parts = [...entries, existing].filter((entry): entry is string => typeof entry === "string" && entry.length > 0);
  return `${parts.join(":")}:`;
}

export function stageSupportFiles(templatePath: string | undefined, targetDir: string): string[] {
  if (!templatePath) return [];
  const dir = dirname(templatePath);
  const staged: string[] = [];
  let entries: string[] = [];
  try {
    entries = readdirSync(dir);
  } catch {
    return staged;
  }
  for (const entry of entries) {
    if (!SUPPORT_EXTENSIONS.has(extname(entry))) continue;
    const from = join(dir, entry);
    try {
      copyFileSync(from, join(targetDir, entry));
      staged.push(entry);
    } catch {
      // Directories or unreadable files are skipped.
    }
  }
  return staged;
}

/**
 * natbib/BibTeX multi-pass build: pandoc -> .tex in a build dir, then
 * pdflatex, bibtex, pdflatex, pdflatex. The build dir is kept as a cache.
 */
export async function runBibtexPipeline(input: BibtexPipelineInput): Promise<BibtexPipelineResult> {
  const buildDir = join(input.baseDir, ".opencode-build", input.name);
  mkdirSync(buildDir, { recursive: true });
  const texName = `${input.name}.tex`;
  const texPath = join(buildDir, texName);

  copyFileSync(input.bibliography, join(buildDir, natbibBibliographyArg(input.bibliography)));

  const commands: string[][] = [];
  const pandocArgv = ["pandoc", ...input.pandocOptions, input.inputPath, "-o", texPath];
  commands.push(pandocArgv);
  const pandoc = await input.run(pandocArgv, { cwd: buildDir, ...(input.signal ? { signal: input.signal } : {}) });
  if (pandoc.code !== 0) throw new Error(`${input.pandocFailurePrefix}:\n${failureText(pandoc)}`);

  stageSupportFiles(input.templatePath, buildDir);
  const templateDir = input.templatePath ? dirname(input.templatePath) : undefined;
  const resourceDir = input.resourceDir ?? dirname(input.outputPath);
  const env: Record<string, string> = {
    TEXINPUTS: searchPath([buildDir, resourceDir, templateDir], process.env.TEXINPUTS),
    BIBINPUTS: searchPath([resourceDir, buildDir], process.env.BIBINPUTS),
    BSTINPUTS: searchPath([buildDir, templateDir], process.env.BSTINPUTS),
  };
  const options: RunOptions = { cwd: buildDir, env, ...(input.signal ? { signal: input.signal } : {}) };
  const latex = ["pdflatex", "-interaction=nonstopmode", "-halt-on-error", "-file-line-error", texName];

  const step = async (argv: string[], acceptWarnings = false) => {
    commands.push(argv);
    const result = await input.run(argv, options);
    // bibtex exits 1 for warnings only (e.g. a missing field); 2+ means real errors.
    if (result.code === 0 || (acceptWarnings && result.code === 1)) return;
    throw new Error(`Compilation failed:\n${failureText(result)}`);
  };

  const pdfPath = join(buildDir, `${input.name}.pdf`);
  // The build dir is a cache: drop a stale PDF so a failed run cannot look successful.
  rmSync(pdfPath, { force: true });
  await step(latex);
  const auxPath = join(buildDir, `${input.name}.aux`);
  let aux = "";
  try {
    aux = readFileSync(auxPath, "utf8");
  } catch {
    aux = "";
  }
  // bibtex hard-fails when nothing is cited; an uncited bibliography is not an error here.
  if (aux.length === 0 || aux.includes("\\citation{")) await step(["bibtex", input.name], true);
  await step(latex);
  await step(latex);

  if (!existsSync(pdfPath)) throw new Error(`Expected PDF not found after BibTeX build: ${pdfPath}`);
  if (pdfPath !== input.outputPath) {
    mkdirSync(dirname(input.outputPath), { recursive: true });
    copyFileSync(pdfPath, input.outputPath);
  }
  return { buildDir, texPath, pdfPath, commands };
}
