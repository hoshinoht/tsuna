/**
 * Tsuna-owned locations and the isolation guard.
 *
 * The experiment may live inside a checkout of the reference Tsuna project.
 * Every write location is resolved through realpath and must stay outside the
 * installed reference (its `.runtime/`, OMP/Pi user directories, the shared
 * secrets directory, and anything an inherited `TSUNA_ROOT` points at), unless
 * it is inside this experiment's own directory.
 */
import { existsSync, mkdirSync, realpathSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";

/** Directory containing this experiment (`experiments/tsuna`). */
export const EXPERIMENT_DIR = resolve(import.meta.dir, "..");

/** Resolve a path through the nearest existing ancestor's realpath. */
export function realish(path: string): string {
	const absolute = resolve(path);
	const tail: string[] = [];
	for (let current = absolute; ; current = dirname(current)) {
		try {
			return join(realpathSync.native(current), ...tail.reverse());
		} catch {
			if (dirname(current) === current) return absolute;
			tail.push(basename(current));
		}
	}
}

export function isInside(child: string, parent: string): boolean {
	const delta = relative(parent, child);
	return delta === "" || (!delta.startsWith(`..${sep}`) && delta !== ".." && !isAbsolute(delta));
}

/** Locations an experiment must never write to. */
export function protectedRoots(env: NodeJS.ProcessEnv = process.env): string[] {
	const home = env.HOME || homedir();
	const roots = [
		join(home, ".omp"),
		join(home, ".pi"),
		join(home, ".config", "tsuna"),
		join(home, ".config", "hoshi-omp"),
		join(home, ".config", "hoshi-secrets"),
	];
	// An inherited TSUNA_ROOT names the installed reference; it is a
	// protected location, never a redirect for experiment state.
	if (env.TSUNA_ROOT) roots.push(env.TSUNA_ROOT);
	// When the experiment sits inside a reference checkout, the checkout
	// (other than this experiment directory) is read-only.
	const enclosing = resolve(EXPERIMENT_DIR, "..", "..");
	if (existsSync(join(enclosing, "native", "tsuna", "Cargo.toml"))) roots.push(enclosing);
	return roots.map(realish);
}

export class IsolationError extends Error {}

/** Throw unless `path` is a permitted experiment write location. */
export function assertWritable(path: string, env: NodeJS.ProcessEnv = process.env): string {
	const real = realish(path);
	if (isInside(real, realish(EXPERIMENT_DIR))) return real;
	for (const root of protectedRoots(env)) {
		if (isInside(real, root)) {
			throw new IsolationError(`refusing to use ${path}: it resolves inside protected location ${root}`);
		}
	}
	return real;
}

export interface TsunaPaths {
	/** Root of all runtime state for this harness instance. */
	state: string;
	sessions: string;
	agents: string;
	jobs: string;
	natives: string;
	logs: string;
}

/**
 * State root precedence: explicit `--state` > `TSUNA_EXPERIMENT_STATE` >
 * `<experiment>/.state`. `TSUNA_ROOT` is deliberately ignored.
 */
export function resolvePaths(explicit?: string, env: NodeJS.ProcessEnv = process.env): TsunaPaths {
	const chosen = explicit ?? env.TSUNA_EXPERIMENT_STATE ?? join(EXPERIMENT_DIR, ".state");
	const state = assertWritable(resolve(chosen), env);
	return {
		state,
		sessions: join(state, "sessions"),
		agents: join(state, "agents"),
		jobs: state, // supervisor uses <root>/jobs
		natives: join(state, "natives"),
		logs: join(state, "logs"),
	};
}

export function ensureDirs(paths: TsunaPaths): void {
	for (const dir of [paths.state, paths.sessions, paths.agents, paths.natives, paths.logs]) {
		mkdirSync(dir, { recursive: true, mode: 0o700 });
	}
}

export function defaultTempRoot(): string {
	return tmpdir();
}
