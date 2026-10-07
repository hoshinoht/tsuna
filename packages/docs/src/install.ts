import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ensureUserConfigDirs, isFile, type SearchRoots } from "./paths";
import { failureText, type Runner } from "./process";

export const INSTALL_SOURCES = ["eisvogel", "csl-ieee", "csl-apa", "csl-acm"] as const;
export type InstallSource = (typeof INSTALL_SOURCES)[number];

export const EISVOGEL_URL = "https://github.com/Wandmalfarbe/pandoc-latex-template/releases/download/v3.3.0/Eisvogel.tar.gz";
export const CSL_BASE_URL = "https://raw.githubusercontent.com/citation-style-language/styles/master/";
export const CSL_FILES: Record<"csl-ieee" | "csl-apa" | "csl-acm", string> = {
  "csl-ieee": "ieee.csl",
  "csl-apa": "apa.csl",
  "csl-acm": "acm-sig-proceedings.csl",
};

export type FetchLike = (url: string, init?: { signal?: AbortSignal; redirect?: "follow" }) => Promise<{ ok: boolean; status: number; arrayBuffer(): Promise<ArrayBuffer> }>;

export interface InstallDeps {
  fetch: FetchLike;
  run: Runner;
  tmpdir?: () => string;
  signal?: AbortSignal;
}

async function download(deps: InstallDeps, url: string): Promise<{ ok: true; body: Uint8Array } | { ok: false; reason: string }> {
  try {
    const response = await deps.fetch(url, { redirect: "follow", ...(deps.signal ? { signal: deps.signal } : {}) });
    if (!response.ok) return { ok: false, reason: `HTTP ${response.status}` };
    const body = new Uint8Array(await response.arrayBuffer());
    if (body.byteLength === 0) return { ok: false, reason: "empty response body" };
    return { ok: true, body };
  } catch (error) {
    return { ok: false, reason: error instanceof Error ? error.message : String(error) };
  }
}

function refuseOverwrite(dest: string): never {
  throw new Error(`Refusing to overwrite existing file ${dest}. Pass force: true to replace it.`);
}

function refuseShadow(dest: string, bundled: string): never {
  throw new Error(
    `Installing to ${dest} would shadow the bundled template ${bundled}, which is already available. Pass force: true to install the third-party copy anyway.`,
  );
}

function guard(dest: string, force: boolean, bundledEquivalent?: string): void {
  if (force) return;
  if (existsSync(dest)) refuseOverwrite(dest);
  if (bundledEquivalent && isFile(bundledEquivalent)) refuseShadow(dest, bundledEquivalent);
}

/**
 * Installs third-party templates / CSL styles into the user pandoc dir. Never overwrites an
 * existing file, and never shadows a bundled template, unless `force` is true.
 */
export async function installTemplate(roots: SearchRoots, source: InstallSource, force: boolean, deps: InstallDeps): Promise<string> {
  ensureUserConfigDirs(roots.user);

  if (source === "eisvogel") {
    const dest = join(roots.user, "templates", "eisvogel.latex");
    guard(dest, force, join(roots.bundled, "templates", "eisvogel.latex"));
    const archive = await download(deps, EISVOGEL_URL);
    if (!archive.ok) throw new Error(`Download failed: ${archive.reason}`);
    const work = mkdtempSync(join((deps.tmpdir ?? tmpdir)(), "eisvogel-install-"));
    try {
      const tarball = join(work, "Eisvogel.tar.gz");
      writeFileSync(tarball, archive.body);
      const extracted = await deps.run(["tar", "-xzf", tarball, "-C", work], { cwd: work, ...(deps.signal ? { signal: deps.signal } : {}) });
      if (extracted.code !== 0) throw new Error(`Extraction failed: ${failureText(extracted)}`);
      const template = join(work, "eisvogel.latex");
      if (!isFile(template)) throw new Error(`Extraction failed: eisvogel.latex not found at the archive root`);
      mkdirSync(join(roots.user, "templates"), { recursive: true });
      writeFileSync(dest, await Bun.file(template).bytes());
    } finally {
      rmSync(work, { recursive: true, force: true });
    }
    return `Installed eisvogel to ${dest}`;
  }

  const file = CSL_FILES[source];
  const dest = join(roots.user, "csl", file);
  guard(dest, force);
  const fetched = await download(deps, `${CSL_BASE_URL}${file}`);
  if (!fetched.ok) throw new Error(fetched.reason.startsWith("HTTP ") ? fetched.reason : `Download failed: ${fetched.reason}`);
  mkdirSync(join(roots.user, "csl"), { recursive: true });
  writeFileSync(dest, fetched.body);
  return `Installed ${source} to ${dest}`;
}
