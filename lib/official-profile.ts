import { cp, lstat, mkdir, readlink, rename, symlink } from "node:fs/promises";
import { join, resolve } from "node:path";

/** Point the official CLI's default agent directory at the existing Hoshi data, preserving prior state. */
export async function connectOfficialProfile(root: string, home: string): Promise<string | undefined> {
	const target = join(root, ".runtime/omp/agent");
	const base = join(home, ".omp");
	const agentDir = join(base, "agent");
	if (!(await lstat(target)).isDirectory()) throw new Error("Prepare the Hoshi agent profile before connecting the official CLI");
	await mkdir(base, { recursive: true });
	const existing = await lstat(agentDir).catch(() => undefined);
	if (existing?.isSymbolicLink() && resolve(base, await readlink(agentDir)) === resolve(target)) return;
	let backup: string | undefined;
	if (existing) {
		backup = join(base, `agent-before-hoshi-${Date.now()}`);
		await rename(agentDir, backup);
	}
	try {
		if (backup) {
			const markers = join(backup, "custom-session-files");
			if (await lstat(markers).catch(() => undefined)) await cp(markers, join(target, "custom-session-files"), { recursive: true, force: false });
		}
		await symlink(target, agentDir, "dir");
	}
	catch (error) { if (backup) await rename(backup, agentDir); throw error; }
	return backup;
}
