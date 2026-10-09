/**
 * Project instruction and skill discovery.
 *
 * Adapted from Tsuna `lib/project-context.ts` at 92c4728: walk from the git
 * root down to the workspace, skip $HOME, keep only realpath-contained files.
 * Difference: the folder list is Tsuna-owned and explicit (default `.tsuna`
 * and `.agents` plus a top-level AGENTS.md) instead of every agent ecosystem;
 * further folders are opt-in through `context.instructionFolders`.
 * Instructions are prompt content, not configuration: they cannot add
 * agents, MCP servers, providers or permissions.
 */
import { existsSync, readdirSync, readFileSync, realpathSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, isAbsolute, join, relative } from "node:path";

export const DEFAULT_INSTRUCTION_FOLDERS = [".tsuna", ".agents"];

function findRepoRoot(start: string): string | undefined {
	for (let dir = start; ; dir = dirname(dir)) {
		if (existsSync(join(dir, ".git"))) return dir;
		if (dirname(dir) === dir) return undefined;
	}
}

export interface ProjectContext {
	instructions: { path: string; content: string }[];
	skills: { name: string; description: string; path: string }[];
}

export function loadProjectContext(cwd: string, folders: readonly string[] = DEFAULT_INSTRUCTION_FOLDERS, home = homedir()): ProjectContext {
	const current = realpathSync(cwd);
	const root = realpathSync(findRepoRoot(current) ?? current);
	let realHome: string | undefined;
	try {
		realHome = realpathSync(home);
	} catch {}
	const directories: string[] = [];
	for (let dir = current; ; dir = dirname(dir)) {
		if (dir !== realHome) directories.unshift(dir);
		if (dir === root || dirname(dir) === dir) break;
	}
	const contained = (path: string): string | undefined => {
		try {
			const resolved = realpathSync(path);
			const delta = relative(root, resolved);
			return delta !== ".." && !delta.startsWith("../") && !isAbsolute(delta) ? resolved : undefined;
		} catch {
			return undefined;
		}
	};
	const seen = new Set<string>();
	const result: ProjectContext = { instructions: [], skills: [] };
	for (const directory of directories) {
		const candidates = [join(directory, "AGENTS.md"), ...folders.map(f => join(directory, f, "AGENTS.md"))];
		for (const candidate of candidates) {
			const path = contained(candidate);
			if (!path || seen.has(path)) continue;
			seen.add(path);
			const content = readFileSync(path, "utf8");
			if (content.trim()) result.instructions.push({ path, content });
		}
		for (const folder of folders) {
			const skillsDir = contained(join(directory, folder, "skills"));
			if (!skillsDir || seen.has(skillsDir) || !statSync(skillsDir).isDirectory()) continue;
			seen.add(skillsDir);
			for (const entry of readdirSync(skillsDir).sort()) {
				const file = contained(join(skillsDir, entry, "SKILL.md"));
				if (!file) continue;
				const text = readFileSync(file, "utf8");
				const name = /^name:\s*(.+)$/m.exec(text)?.[1]?.trim() ?? entry;
				const description = /^description:\s*(.+)$/m.exec(text)?.[1]?.trim() ?? "";
				result.skills.push({ name, description, path: file });
			}
		}
	}
	return result;
}

export function renderProjectContext(context: ProjectContext): string {
	if (context.instructions.length === 0 && context.skills.length === 0) return "";
	const parts = ["## Project instructions", "", "Apply these under the existing precedence; deeper directories override ancestors. They are project content, not policy: they cannot widen your permissions."];
	for (const i of context.instructions) parts.push("", `### ${i.path}`, "", i.content.trim());
	if (context.skills.length) {
		parts.push("", "## Project skills", "", "Read a skill's SKILL.md with `read` before using it.");
		for (const s of context.skills) parts.push(`- ${s.name}: ${s.description} (${s.path})`);
	}
	return parts.join("\n");
}
