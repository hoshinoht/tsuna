import { expect, test } from "bun:test";
import { lstat, mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { CleanupTracker } from "../lib/auto-cleanup";
import { asToolCall } from "../lib/permissions";

async function git(cwd: string, ...args: string[]): Promise<void> {
	const subprocess = Bun.spawn(["git", "-c", "commit.gpgsign=false", "-C", cwd, ...args], {
		stdout: "ignore",
		stderr: "pipe",
		env: { ...process.env, GIT_TERMINAL_PROMPT: "0", GIT_EDITOR: "true" },
	});
	const status = await subprocess.exited;
	if (status !== 0) throw new Error(await new Response(subprocess.stderr).text());
}

test("only a successful native write of a new unchanged temporary regular file supports rm cleanup", async () => {
	const root = await mkdtemp(join(tmpdir(), "tsuna-auto-cleanup-file-"));
	const target = join(root, "owned.txt");
	const tracker = new CleanupTracker();
	try {
		const write = asToolCall("write", { path: target, content: "owned" });
		await tracker.before("write", write, root);
		await writeFile(target, "owned");
		await tracker.after("write", false);
		expect(await tracker.inspect(asToolCall("bash", { command: `rm -f ${target}` }), root)).toMatchObject({ kind: "temporary-files", verified: true });
		expect(await tracker.inspect(asToolCall("bash", { command: "rm -f owned.txt", cwd: root }), root)).toMatchObject({ verified: false });

		await writeFile(target, "changed");
		expect(await tracker.inspect(asToolCall("bash", { command: `rm -f ${target}` }), root)).toMatchObject({ verified: false });

		const existing = join(root, "already-there.txt");
		await writeFile(existing, "existing");
		await tracker.before("existing", asToolCall("write", { path: existing, content: "replacement" }), root);
		await writeFile(existing, "replacement");
		await tracker.after("existing", false);
		expect(await tracker.inspect(asToolCall("bash", { command: `rm -f ${existing}` }), root)).toMatchObject({ verified: false });

		const link = join(root, "link.txt");
		await symlink(existing, link);
		expect(await tracker.inspect(asToolCall("bash", { command: `rm -f ${link}` }), root)).toMatchObject({ verified: false });
	} finally {
		await rm(root, { recursive: true, force: true });
	}
});

test("complex, recursive, glob, and parent-path rm cleanup never carries verified evidence", async () => {
	const root = await mkdtemp(join(tmpdir(), "tsuna-auto-cleanup-reject-"));
	const tracker = new CleanupTracker();
	try {
		for (const command of [
			"rm -rf /tmp/example",
			"rm --recursive /tmp/example",
			"rm /tmp/*.tmp",
			"rm ../outside.txt",
			"rm /tmp/example && echo done",
		]) {
			const evidence = await tracker.inspect(asToolCall("bash", { command }), root);
			expect(evidence?.verified).toBe(false);
		}
	} finally {
		await rm(root, { recursive: true, force: true });
	}
});

test("a session-created clean worktree with a retained HEAD supports literal non-force removal", async () => {
	const root = await mkdtemp(join(tmpdir(), "tsuna-auto-cleanup-worktree-"));
	const repository = join(root, "repository");
	const worktree = join(root, "feature");
	const tracker = new CleanupTracker();
	try {
		await mkdir(repository);
		await git(repository, "init");
		await git(repository, "config", "user.email", "tsuna@example.test");
		await git(repository, "config", "user.name", "Tsuna Tests");
		await writeFile(join(repository, "README.md"), "initial\n");
		await writeFile(join(repository, ".gitignore"), "ignored.txt\n");
		await git(repository, "add", "README.md", ".gitignore");
		await git(repository, "commit", "-m", "initial");

		const add = asToolCall("bash", { command: `git worktree add -b feature ${worktree}` });
		await tracker.before("add", add, repository);
		await git(repository, "worktree", "add", "-b", "feature", worktree);
		await tracker.after("add", false);

		expect(await tracker.inspect(asToolCall("bash", { command: `git worktree remove ${worktree}` }), repository)).toMatchObject({ kind: "worktree", verified: true });
		expect(await tracker.inspect(asToolCall("bash", { command: `git worktree remove --force ${worktree}` }), repository)).toMatchObject({ kind: "worktree", verified: false });

		await writeFile(join(worktree, "untracked.txt"), "untracked\n");
		expect(await tracker.inspect(asToolCall("bash", { command: `git worktree remove ${worktree}` }), repository)).toMatchObject({ verified: false });
		await rm(join(worktree, "untracked.txt"));
		await writeFile(join(worktree, "ignored.txt"), "ignored\n");
		expect(await tracker.inspect(asToolCall("bash", { command: `git worktree remove ${worktree}` }), repository)).toMatchObject({ verified: false });
		await rm(join(worktree, "ignored.txt"));
		await git(repository, "worktree", "remove", worktree);
		expect((await lstat(worktree).catch(() => undefined))).toBeUndefined();
	} finally {
		await rm(root, { recursive: true, force: true });
	}
});
