import { readFile, realpath, stat } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, isAbsolute, join, relative } from "node:path";
import { findRepoRoot } from "@oh-my-pi/pi-coding-agent/capability/fs";

const sources = [
  [".opencode", ["AGENTS.md"]],
  [".codex", ["AGENTS.md"]],
  [".claude", ["AGENTS.md", "CLAUDE.md"]],
  [".agents", ["AGENTS.md"]],
  [".agent", ["AGENTS.md"]],
  [".gemini", ["AGENTS.md", "GEMINI.md"]],
] as const;

export async function loadProjectContext(cwd: string) {
  const current = await realpath(cwd);
  const root = await realpath(await findRepoRoot(current) ?? current);
  // A session opened in HOME must not turn user configuration into project input.
  const home = await realpath(homedir());
  const directories: string[] = [];
  for (let dir = current; ; dir = dirname(dir)) {
    if (dir !== home) directories.unshift(dir);
    if (dir === root) break;
  }
  const instructions: { path: string; content: string }[] = [];
  const skillPaths: string[] = [];
  const seen = new Set<string>();
  async function contained(path: string) {
    try {
      const resolved = await realpath(path);
      const delta = relative(root, resolved);
      return delta !== ".." && !delta.startsWith("../") && !isAbsolute(delta) ? resolved : undefined;
    } catch (error) {
      if (["ENOENT", "ENOTDIR"].includes((error as NodeJS.ErrnoException).code ?? "")) return undefined;
      throw error;
    }
  }
  for (const directory of directories) {
    for (const [folder, filenames] of sources) {
      for (const filename of filenames) {
        const path = await contained(join(directory, folder, filename));
        if (!path || seen.has(path)) continue;
        const content = await readFile(path, "utf8");
        seen.add(path);
        if (content.trim()) instructions.push({ path, content });
      }
      const path = await contained(join(directory, folder, "skills"));
      if (path && !seen.has(path) && (await stat(path)).isDirectory()) {
        seen.add(path);
        skillPaths.push(path);
      }
    }
  }
  return { instructions, skillPaths };
}
