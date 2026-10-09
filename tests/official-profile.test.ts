import { afterEach, expect, test } from "bun:test";
import { mkdtemp, mkdir, readFile, readlink, readdir, realpath, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { rustCli } from "./rust-cli-helper";

const cleanup: string[] = [];
afterEach(async () => { for (const path of cleanup.splice(0)) await rm(path, { recursive: true, force: true }); });

test("official profile connection preserves prior files and reuses the existing Tsuna session directory", async () => {
	const home = await realpath(await mkdtemp(join(tmpdir(), "tsuna-official-home-"))); cleanup.push(home);
	const root = join(home, "repo");
	await mkdir(join(root, ".runtime/omp/agent"), { recursive: true });
	await mkdir(join(home, ".omp/agent/custom-session-files"), { recursive: true });
	await writeFile(join(home, ".omp/agent/custom-session-files/old"), "old session pointer");
	await writeFile(join(home, ".omp/agent/keep.txt"), "existing state");
	expect(rustCli(root, ["connect-official"], { HOME: home }).code).toBe(0);
	const backups = (await readdir(join(home, ".omp"))).filter(name => name.startsWith("agent-before-tsuna-"));
	expect(backups).toHaveLength(1);
	const backup = join(home, ".omp", backups[0]);
	expect(await readlink(join(home, ".omp/agent"))).toBe(join(root, ".runtime/omp/agent"));
	expect(await readFile(join(backup!, "keep.txt"), "utf8")).toBe("existing state");
	expect(await readFile(join(root, ".runtime/omp/agent/custom-session-files/old"), "utf8")).toBe("old session pointer");
	expect(rustCli(root, ["connect-official"], { HOME: home }).code).toBe(0);
	expect((await readdir(join(home, ".omp"))).filter(name => name.startsWith("agent-before-tsuna-"))).toEqual(backups);
});
