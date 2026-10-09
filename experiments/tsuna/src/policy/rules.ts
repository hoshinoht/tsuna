/**
 * Ordered, last-match-wins permission rules and filesystem boundary checks.
 *
 * Adapted from Tsuna `lib/permissions.ts` at 92c4728 (GPL-3.0-or-later, same
 * project). Changes: no OpenCode skill-path remap, no `TSUNA_ROOT` lookup and
 * no OMP imports; host tool mapping lives in `intents.ts`.
 */
import { readFileSync, realpathSync, statSync } from "node:fs";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export type PermissionEffect = "allow" | "ask" | "deny";

export interface PermissionRule {
	action: string;
	resource: string;
	effect: PermissionEffect;
}

export function globMatches(pattern: string, value: string, home = process.env.HOME): boolean {
	if (pattern.startsWith("~/") && home) pattern = `${home}${pattern.slice(1)}`;
	let source = "^";
	for (const character of pattern) {
		if (character === "*") source += ".*";
		else if (character === "?") source += ".";
		else source += character.replace(/[|\\{}()[\]^$+?.]/g, "\\$&");
	}
	return new RegExp(`${source}$`, "s").test(value);
}

/** The last rule whose action and resource both match wins. */
export function evaluateRules(rules: readonly PermissionRule[], action: string, resource: string): PermissionRule | undefined {
	let matched: PermissionRule | undefined;
	for (const rule of rules) {
		if (globMatches(rule.action, action) && globMatches(rule.resource, resource)) matched = rule;
	}
	return matched;
}

/** Last rule matching on action alone; used for catalog filtering only. */
export function evaluateAction(rules: readonly PermissionRule[], action: string): PermissionRule | undefined {
	let matched: PermissionRule | undefined;
	for (const rule of rules) if (globMatches(rule.action, action)) matched = rule;
	return matched;
}

export function canonicalPath(path: string, cwd: string): string | undefined {
	if (path.startsWith("file://")) {
		try {
			path = fileURLToPath(path);
		} catch {
			return undefined;
		}
	}
	if (path.includes("://")) return undefined;
	const expanded = path.startsWith("~/") && process.env.HOME ? `${process.env.HOME}${path.slice(1)}` : path;
	const absolute = isAbsolute(expanded) ? resolve(expanded) : resolve(cwd, expanded);
	const tail: string[] = [];
	for (let current = absolute; ; current = dirname(current)) {
		try {
			return resolve(realpathSync.native(current), ...tail.reverse());
		} catch {
			if (dirname(current) === current) return undefined;
			tail.push(current.slice(dirname(current).length + 1));
		}
	}
}

function gitBoundary(path: string): { root: string; commonDir: string } | undefined {
	let directory = path;
	try {
		if (!statSync(directory).isDirectory()) directory = dirname(directory);
	} catch {
		directory = dirname(directory);
	}
	for (; ; directory = dirname(directory)) {
		const marker = join(directory, ".git");
		try {
			const info = statSync(marker);
			if (info.isDirectory()) return { root: directory, commonDir: realpathSync.native(marker) };
			if (info.isFile()) {
				const match = /^gitdir:\s*(.+)\s*$/m.exec(readFileSync(marker, "utf8"));
				if (!match) return undefined;
				const gitDir = realpathSync.native(resolve(directory, match[1]!.trim()));
				let commonDir = gitDir;
				try {
					commonDir = realpathSync.native(resolve(gitDir, readFileSync(join(gitDir, "commondir"), "utf8").trim()));
				} catch {}
				// Linked worktree metadata must point back to this checkout.
				if (commonDir !== gitDir) {
					const backPointer = readFileSync(join(gitDir, "gitdir"), "utf8").trim();
					if (realpathSync.native(backPointer) !== realpathSync.native(marker)) return undefined;
				}
				return { root: directory, commonDir };
			}
		} catch {}
		if (dirname(directory) === directory) return undefined;
	}
}

/** Whether `path` (canonical) lies outside the workspace rooted at `cwd`. */
export function isExternal(path: string, cwd: string): boolean {
	const canonicalCwd = canonicalPath(cwd, cwd);
	if (!canonicalCwd) return true;
	const currentRepo = gitBoundary(canonicalCwd);
	const root = currentRepo?.root ?? canonicalCwd;
	const delta = relative(root, path);
	if (delta !== ".." && !delta.startsWith(`..${process.platform === "win32" ? "\\" : "/"}`) && !isAbsolute(delta)) return false;
	const targetRepo = currentRepo ? gitBoundary(path) : undefined;
	return !targetRepo || targetRepo.commonDir !== currentRepo?.commonDir;
}
