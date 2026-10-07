import { existsSync, mkdirSync, readdirSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { basename, isAbsolute, join } from "node:path";
import { fileURLToPath } from "node:url";

export type RootSource = "project" | "user" | "bundled";

/** Where templates, presets, CSL files and assets are looked up. */
export interface SearchRoots {
  /** Workspace root W. */
  workspace: string;
  /** Project pandoc dir P = W/.opencode/pandoc. */
  project: string;
  /** User pandoc dir U = $XDG_CONFIG_HOME/opencode/pandoc. */
  user: string;
  /** Bundled pandoc dir B = <package>/pandoc. */
  bundled: string;
}

export interface Resolved {
  path: string;
  source: RootSource;
}

export interface Listed extends Resolved {
  name: string;
}

export function userPandocDir(env: NodeJS.ProcessEnv = process.env): string {
  const configHome = env.XDG_CONFIG_HOME && env.XDG_CONFIG_HOME.length > 0 ? env.XDG_CONFIG_HOME : join(homedir(), ".config");
  return join(configHome, "opencode", "pandoc");
}

/** Computed once when the module loads. */
export const USER_PANDOC_DIR = userPandocDir();
export const BUNDLED_PANDOC_DIR = fileURLToPath(new URL("../pandoc", import.meta.url));

export function makeRoots(workspace: string, overrides: Partial<Omit<SearchRoots, "workspace">> = {}): SearchRoots {
  return {
    workspace,
    project: overrides.project ?? join(workspace, ".opencode", "pandoc"),
    user: overrides.user ?? USER_PANDOC_DIR,
    bundled: overrides.bundled ?? BUNDLED_PANDOC_DIR,
  };
}

export function isFile(path: string): boolean {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

export function isDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

function orderedRoots(roots: SearchRoots): Resolved[] {
  return [
    { path: roots.project, source: "project" },
    { path: roots.user, source: "user" },
    { path: roots.bundled, source: "bundled" },
  ];
}

function absoluteLookup(name: string): Resolved | undefined | null {
  if (!isAbsolute(name)) return null;
  return existsSync(name) ? { path: name, source: "user" } : undefined;
}

export function resolveTemplate(roots: SearchRoots, name: string): Resolved | undefined {
  const absolute = absoluteLookup(name);
  if (absolute !== null) return absolute;
  for (const root of orderedRoots(roots)) {
    const base = join(root.path, "templates", name);
    if (isFile(base)) return { path: base, source: root.source };
    if (!name.includes(".") && isFile(`${base}.latex`)) return { path: `${base}.latex`, source: root.source };
    const nested = join(base, "template.latex");
    if (isFile(nested)) return { path: nested, source: root.source };
  }
  return undefined;
}

export function resolvePreset(roots: SearchRoots, name: string): Resolved | undefined {
  const absolute = absoluteLookup(name);
  if (absolute !== null) return absolute;
  const file = name.endsWith(".yaml") ? name : `${name}.yaml`;
  const candidates: Resolved[] = [
    { path: join(roots.project, "presets", file), source: "project" },
    { path: join(roots.project, "presets", "organizations", file), source: "project" },
    { path: join(roots.user, "presets", file), source: "user" },
    { path: join(roots.user, "presets", "organizations", file), source: "user" },
  ];
  return candidates.find((candidate) => isFile(candidate.path));
}

export function resolveCsl(roots: SearchRoots, name: string): Resolved | undefined {
  const absolute = absoluteLookup(name);
  if (absolute !== null) return absolute;
  const file = name.endsWith(".csl") ? name : `${name}.csl`;
  const candidates: Resolved[] = [
    { path: join(roots.project, "csl", file), source: "project" },
    { path: join(roots.user, "csl", file), source: "user" },
  ];
  return candidates.find((candidate) => isFile(candidate.path));
}

export function resolveAsset(roots: SearchRoots, relPath: string): Resolved | undefined {
  const absolute = absoluteLookup(relPath);
  if (absolute !== null) return absolute;
  const candidates: Resolved[] = [
    { path: join(roots.project, relPath), source: "project" },
    { path: join(roots.user, relPath), source: "user" },
    { path: join(roots.workspace, relPath), source: "project" },
    { path: join(roots.bundled, relPath), source: "bundled" },
  ];
  return candidates.find((candidate) => isFile(candidate.path));
}

function entriesOf(dir: string): string[] {
  try {
    return readdirSync(dir);
  } catch {
    return [];
  }
}

/**
 * Lists templates across P, U and B. A name is marked as seen before the entry is checked,
 * so an unusable entry in an earlier root hides a real template of the same name later on.
 */
export function listTemplates(roots: SearchRoots): Listed[] {
  const seen = new Set<string>();
  const out: Listed[] = [];
  for (const root of orderedRoots(roots)) {
    const dir = join(root.path, "templates");
    for (const entry of entriesOf(dir)) {
      const name = entry.endsWith(".latex") ? entry.slice(0, -".latex".length) : entry;
      if (seen.has(name)) continue;
      seen.add(name);
      const full = join(dir, entry);
      if (isDirectory(full)) {
        const nested = join(full, "template.latex");
        if (isFile(nested)) out.push({ name, path: nested, source: root.source });
      } else if (entry.endsWith(".latex") && isFile(full)) {
        out.push({ name, path: full, source: root.source });
      }
    }
  }
  return out;
}

function walkYaml(dir: string, found: string[]): void {
  for (const entry of entriesOf(dir)) {
    const full = join(dir, entry);
    if (isDirectory(full)) walkYaml(full, found);
    else if (entry.endsWith(".yaml") && isFile(full)) found.push(full);
  }
}

export function listPresetFiles(roots: SearchRoots): Listed[] {
  const seen = new Set<string>();
  const out: Listed[] = [];
  for (const root of [{ path: roots.project, source: "project" as const }, { path: roots.user, source: "user" as const }]) {
    const files: string[] = [];
    walkYaml(join(root.path, "presets"), files);
    for (const file of files) {
      const name = basename(file, ".yaml");
      if (seen.has(name)) continue;
      seen.add(name);
      out.push({ name, path: file, source: root.source });
    }
  }
  return out;
}

export function listCsl(roots: SearchRoots): Listed[] {
  const seen = new Set<string>();
  const out: Listed[] = [];
  for (const root of [{ path: roots.project, source: "project" as const }, { path: roots.user, source: "user" as const }]) {
    const dir = join(root.path, "csl");
    for (const entry of entriesOf(dir)) {
      if (!entry.endsWith(".csl")) continue;
      const full = join(dir, entry);
      if (!isFile(full)) continue;
      const name = entry.slice(0, -".csl".length);
      if (seen.has(name)) continue;
      seen.add(name);
      out.push({ name, path: full, source: root.source });
    }
  }
  return out;
}

/**
 * Creates the user pandoc directory skeleton. Per-template subdirectories are deliberately
 * not created: an empty `U/templates/<name>` directory would hide a bundled template of the
 * same name from `listTemplates` because of the seen-before-check rule.
 */
export const USER_CONFIG_SUBDIRS = ["templates", join("templates", "custom"), "csl", "presets", join("presets", "organizations"), "assets"];

export function ensureUserConfigDirs(userDir: string): string[] {
  const created = [userDir, ...USER_CONFIG_SUBDIRS.map((sub) => join(userDir, sub))];
  for (const dir of created) mkdirSync(dir, { recursive: true });
  return created;
}
