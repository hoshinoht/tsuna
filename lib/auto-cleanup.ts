import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { lstat, realpath } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { tokenizeShellSegments } from "@oh-my-pi/pi-coding-agent/tools/shell-tokenize";
import type { ToolCall } from "./permissions";

export interface CleanupEvidence {
	kind: "temporary-files" | "worktree";
	verified: boolean;
	reason: string;
}

interface FileOwnership {
	path: string;
	device: number;
	ino: number;
	size: number;
	mtimeMs: number;
	ctimeMs: number;
	hash: string;
}

interface WorktreeOwnership {
	commonDir: string;
	device: number;
	ino: number;
}

type Pending =
	| { kind: "file"; path: string }
	| { kind: "worktree"; path: string; commonDir: string };

const GIT_TIMEOUT_MS = 2_000;
const MAX_TEMP_FILE_BYTES = 4 * 1024 * 1024;
const SHELL_METACHARACTER = /[;&|()$`<>\n\r*?\[\]{}~\\]/;

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringInput(input: Record<string, unknown>, key: string): string | undefined {
	const value = input[key];
	return typeof value === "string" && value.length > 0 ? value : undefined;
}

function isEnoent(error: unknown): boolean {
	return isRecord(error) && error.code === "ENOENT";
}

function inside(path: string, root: string): boolean {
	const delta = relative(root, path);
	return delta === "" || (delta !== ".." && !delta.startsWith(`..${sep}`) && !delta.startsWith(sep));
}

function hasParentPath(path: string): boolean {
	return path.split(/[\\/]+/).includes("..");
}

function literalPath(path: string, cwd: string): string | undefined {
	if (!path || path === "/" || path === "." || hasParentPath(path)) return;
	const resolved = resolve(cwd, path);
	return resolved === resolve(sep) ? undefined : resolved;
}

async function existingRegularFile(path: string): Promise<FileOwnership | undefined> {
	let first;
	try { first = await lstat(path); } catch { return; }
	if (!first.isFile() || first.isSymbolicLink()) return;
	if (first.size > MAX_TEMP_FILE_BYTES) return;
	const hash = createHash("sha256");
	try {
		for await (const chunk of createReadStream(path)) hash.update(chunk);
	} catch { return; }
	let second;
	try { second = await lstat(path); } catch { return; }
	if (!second.isFile() || second.isSymbolicLink() ||
		first.dev !== second.dev || first.ino !== second.ino || first.size !== second.size ||
		first.mtimeMs !== second.mtimeMs || first.ctimeMs !== second.ctimeMs) return;
	return { path, device: second.dev, ino: second.ino, size: second.size, mtimeMs: second.mtimeMs, ctimeMs: second.ctimeMs, hash: hash.digest("hex") };
}

function sameFile(left: FileOwnership, right: FileOwnership): boolean {
	return left.path === right.path && left.device === right.device && left.ino === right.ino &&
		left.size === right.size && left.mtimeMs === right.mtimeMs && left.ctimeMs === right.ctimeMs && left.hash === right.hash;
}

async function temporaryFilePath(path: string, cwd: string): Promise<string | undefined> {
	const literal = literalPath(path, cwd);
	if (!literal) return;
	let parent: string;
	try { parent = await realpath(dirname(literal)); } catch { return; }
	const canonical = join(parent, basename(literal));
	const roots = await Promise.all([tmpdir(), "/tmp", "/var/tmp"].map(async root => {
		try { return await realpath(root); } catch { return undefined; }
	}));
	return roots.some(root => root && inside(canonical, root)) ? canonical : undefined;
}

/** Returns a single literal argv form; complex shell is intentionally not cleanup evidence. */
function shellArgv(call: ToolCall): string[] | undefined {
	if (call.toolName !== "bash" || !isRecord(call.input)) return;
	const command = stringInput(call.input, "command");
	if (!command || SHELL_METACHARACTER.test(command)) return;
	const segments = tokenizeShellSegments(command);
	return segments.length === 1 && segments[0]!.length > 0 ? segments[0] : undefined;
}

function recognizedShellCommand(call: ToolCall): "rm" | "worktree" | undefined {
	if (call.toolName !== "bash" || !isRecord(call.input)) return;
	const command = stringInput(call.input, "command")?.trimStart();
	if (!command) return;
	if (/^rm(?:\s|$)/.test(command)) return "rm";
	return /^git\s+worktree(?:\s|$)/.test(command) ? "worktree" : undefined;
}

/** Bash can dispatch through a distinct process context that this tracker cannot prove. */
function bashExecutionOverride(call: ToolCall): boolean {
	if (call.toolName !== "bash" || !isRecord(call.input)) return false;
	return ["cwd", "async", "pty", "name", "ready", "backend", "env", "ssh", "host", "remote", "container"]
		.some(key => call.input[key] !== undefined);
}

function rmPaths(argv: string[]): { paths?: string[]; reason?: string } | undefined {
	if (argv[0] !== "rm") return;
	const paths: string[] = [];
	for (const argument of argv.slice(1)) {
		if (argument.startsWith("-")) {
			if (argument === "-f" || argument === "--force") continue;
			return { reason: "Cleanup uses an rm option outside the verified non-recursive forms." };
		}
		paths.push(argument);
	}
	return paths.length > 0 ? { paths } : { reason: "Cleanup has no literal file paths." };
}

function worktreeOperation(argv: string[]): { operation: "add" | "remove"; path?: string; reason?: string } | undefined {
	if (argv[0] !== "git" || argv[1] !== "worktree") return;
	const operation = argv[2];
	if (operation !== "add" && operation !== "remove") return;
	if (operation === "remove" && (argv.includes("--force") || argv.includes("-f"))) return { operation, reason: "Forced worktree removal always requires human approval." };
	if (operation === "add") {
		const args = argv.slice(3);
		if (args[0] === "-b" && (args.length === 3 || args.length === 4) && !args[1]!.startsWith("-") && !args[2]!.startsWith("-")) return { operation, path: args[2] };
		if (args[0] === "--detach" && (args.length === 2 || args.length === 3) && !args[1]!.startsWith("-")) return { operation, path: args[1] };
		return (args.length === 1 || args.length === 2) && !args[0]!.startsWith("-")
			? { operation, path: args[0] } : { operation, reason: "Worktree creation is not a supported literal git worktree add form." };
	}
	if (argv.slice(3).some(argument => argument.startsWith("-"))) return { operation, reason: "Cleanup uses a git worktree option outside the verified forms." };
	return argv.length === 4 ? { operation, path: argv[3] } : { operation, reason: "Worktree removal is not a literal git worktree remove path." };
}

async function runGit(cwd: string, args: string[]): Promise<string | undefined> {
	try {
		const subprocess = Bun.spawn(["git", "-C", cwd, "-c", "core.fsmonitor=false", ...args], {
			cwd,
			env: { ...process.env, GIT_OPTIONAL_LOCKS: "0" },
			stdout: "pipe",
			stderr: "ignore",
			timeout: GIT_TIMEOUT_MS,
			maxBuffer: 64 * 1024,
		});
		const [status, stdout] = await Promise.all([subprocess.exited, new Response(subprocess.stdout).text()]);
		return status === 0 ? stdout.trimEnd() : undefined;
	} catch { return; }
}

async function commonGitDir(cwd: string): Promise<string | undefined> {
	const result = await runGit(cwd, ["rev-parse", "--git-common-dir"]);
	if (!result || result.includes("\n")) return;
	try { return await realpath(isAbsolute(result) ? result : resolve(cwd, result)); } catch { return; }
}

/** Ownership is in-memory only; tracker/session replacement intentionally loses cleanup eligibility. */
export class CleanupTracker {
	private readonly pending = new Map<string, Pending>();
	private readonly files = new Map<string, FileOwnership>();
	private readonly worktrees = new Map<string, WorktreeOwnership>();

	/** Snapshots only creation candidates whose paths were absent before the harness executes them. */
	async before(toolCallId: string, call: ToolCall, cwd: string): Promise<void> {
		this.pending.delete(toolCallId);
		if (call.toolName === "write" && isRecord(call.input)) {
			const requested = stringInput(call.input, "path");
			if (!requested) return;
			const path = await temporaryFilePath(requested, cwd);
			if (!path || await existingRegularFile(path)) return;
			try { await lstat(path); } catch (error) { if (isEnoent(error)) this.pending.set(toolCallId, { kind: "file", path }); }
			return;
		}
		if (bashExecutionOverride(call)) return;
		const argv = shellArgv(call);
		const operation = argv && worktreeOperation(argv);
		if (operation?.operation !== "add" || !operation.path) return;
		const path = literalPath(operation.path, cwd);
		if (!path) return;
		const commonDir = await commonGitDir(cwd);
		if (!commonDir) return;
		try { await lstat(path); } catch (error) { if (isEnoent(error)) this.pending.set(toolCallId, { kind: "worktree", path, commonDir }); }
	}

	/** Records ownership only after the harness reports success and independent postconditions hold. */
	async after(toolCallId: string, isError: boolean): Promise<void> {
		const pending = this.pending.get(toolCallId);
		this.pending.delete(toolCallId);
		if (!pending || isError) return;
		if (pending.kind === "file") {
			const file = await existingRegularFile(pending.path);
			if (file) this.files.set(file.path, file);
			return;
		}
		let stats;
		try { stats = await lstat(pending.path); } catch { return; }
		if (!stats.isDirectory() || stats.isSymbolicLink()) return;
		let path: string;
		try { path = await realpath(pending.path); } catch { return; }
		const commonDir = await commonGitDir(path);
		if (commonDir && commonDir === pending.commonDir) this.worktrees.set(path, { commonDir, device: stats.dev, ino: stats.ino });
	}

	async inspect(call: ToolCall, cwd: string): Promise<CleanupEvidence | undefined> {
		const recognized = recognizedShellCommand(call);
		if (recognized && bashExecutionOverride(call)) {
			return { kind: recognized === "rm" ? "temporary-files" : "worktree", verified: false, reason: "Cleanup uses a Bash execution context that cannot be verified." };
		}
		const argv = shellArgv(call);
		if (!argv) {
			return recognized ? { kind: recognized === "rm" ? "temporary-files" : "worktree", verified: false, reason: "Cleanup command is not a single literal shell form." } : undefined;
		}
		const rm = rmPaths(argv);
		if (rm) return this.inspectFiles(rm, cwd);
		const worktree = worktreeOperation(argv);
		if (!worktree || worktree.operation !== "remove") return;
		if (!worktree.path) return { kind: "worktree", verified: false, reason: worktree.reason ?? "Worktree removal was not recognized." };
		return this.inspectWorktree(worktree.path, cwd);
	}

	private async inspectFiles(candidate: { paths?: string[]; reason?: string }, cwd: string): Promise<CleanupEvidence> {
		if (!candidate.paths) return { kind: "temporary-files", verified: false, reason: candidate.reason ?? "Temporary cleanup was not recognized." };
		for (const requested of candidate.paths) {
			const path = await temporaryFilePath(requested, cwd);
			if (!path) return { kind: "temporary-files", verified: false, reason: "Cleanup path is not a literal temporary file path." };
			const owned = this.files.get(path);
			if (!owned) return { kind: "temporary-files", verified: false, reason: "Temporary file was not created and verified in this session." };
			const current = await existingRegularFile(path);
			if (!current || !sameFile(owned, current)) return { kind: "temporary-files", verified: false, reason: "Temporary file changed, was replaced, or is no longer a regular file." };
		}
		return { kind: "temporary-files", verified: true, reason: "Every literal temporary file was created and is unchanged in this session." };
	}

	private async inspectWorktree(requested: string, cwd: string): Promise<CleanupEvidence> {
		const path = literalPath(requested, cwd);
		if (!path) return { kind: "worktree", verified: false, reason: "Worktree path is unsafe or contains a parent path." };
		let stats;
		try { stats = await lstat(path); } catch { return { kind: "worktree", verified: false, reason: "Worktree path no longer exists." }; }
		if (!stats.isDirectory() || stats.isSymbolicLink()) return { kind: "worktree", verified: false, reason: "Worktree path is not a regular directory." };
		let canonical: string;
		try { canonical = await realpath(path); } catch { return { kind: "worktree", verified: false, reason: "Worktree path could not be verified." }; }
		const owned = this.worktrees.get(canonical);
		if (!owned) return { kind: "worktree", verified: false, reason: "Worktree was not created and verified in this session." };
		if (stats.dev !== owned.device || stats.ino !== owned.ino) return { kind: "worktree", verified: false, reason: "Worktree directory was replaced after session creation." };
		const worktreeCommonDir = await commonGitDir(canonical);
		if (!worktreeCommonDir || worktreeCommonDir !== owned.commonDir) return { kind: "worktree", verified: false, reason: "Worktree no longer has its verified git common directory." };
		const currentCommonDir = await commonGitDir(cwd);
		if (!currentCommonDir || currentCommonDir !== owned.commonDir) return { kind: "worktree", verified: false, reason: "Worktree does not share the current repository's git common directory." };
		const status = await runGit(canonical, ["status", "--porcelain=v1", "--untracked-files=all", "--ignored"]);
		if (status === undefined) return { kind: "worktree", verified: false, reason: "Could not verify worktree cleanliness." };
		if (status.length > 0) return { kind: "worktree", verified: false, reason: "Worktree has tracked, untracked, or ignored changes." };
		const head = await runGit(canonical, ["rev-parse", "HEAD"]);
		if (!head || /\s/.test(head)) return { kind: "worktree", verified: false, reason: "Could not verify the worktree HEAD." };
		const retained = await runGit(cwd, ["for-each-ref", `--contains=${head}`, "--format=%(refname)", "refs/heads", "refs/tags", "refs/remotes"]);
		if (!retained) return { kind: "worktree", verified: false, reason: "No non-worktree reference retains the worktree HEAD." };
		return { kind: "worktree", verified: true, reason: "Session-created worktree is clean and its HEAD is retained by a repository ref." };
	}
}
