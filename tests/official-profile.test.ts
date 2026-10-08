import { afterEach, expect, test } from "bun:test";
import { mkdtemp, mkdir, readFile, readlink, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { connectOfficialProfile } from "../lib/official-profile";
import { gatewayKeySetting } from "../lib/profile";
import { resolveConfigValue } from "@oh-my-pi/pi-coding-agent/config/resolve-config-value";

const cleanup: string[] = [];
afterEach(async () => { for (const path of cleanup.splice(0)) await rm(path, { recursive: true, force: true }); });

test("official profile connection preserves prior files and reuses the existing Hoshi session directory", async () => {
	const home = await mkdtemp(join(tmpdir(), "hoshi-official-home-")); cleanup.push(home);
	const root = join(home, "repo");
	await mkdir(join(root, ".runtime/omp/agent"), { recursive: true });
	await mkdir(join(home, ".omp/agent/custom-session-files"), { recursive: true });
	await writeFile(join(home, ".omp/agent/custom-session-files/old"), "old session pointer");
	await writeFile(join(home, ".omp/agent/keep.txt"), "existing state");
	const backup = await connectOfficialProfile(root, home);
	expect(await readlink(join(home, ".omp/agent"))).toBe(join(root, ".runtime/omp/agent"));
	expect(await readFile(join(backup!, "keep.txt"), "utf8")).toBe("existing state");
	expect(await readFile(join(root, ".runtime/omp/agent/custom-session-files/old"), "utf8")).toBe("old session pointer");
	expect(await connectOfficialProfile(root, home)).toBeUndefined();
});

test("command-backed gateway keys handle quoted paths without persisting the secret in config", async () => {
	const base = await mkdtemp(join(tmpdir(), "hoshi-key-setting-")); cleanup.push(base);
	const root = join(base, "folder with ' quotes");
	await mkdir(join(root, ".runtime/proxy"), { recursive: true });
	await writeFile(join(root, ".runtime/proxy/client-key"), "fake-fixture-key");
	const setting = gatewayKeySetting(root);
	expect(setting).not.toContain("fake-fixture-key");
	expect(await resolveConfigValue(setting)).toBe("fake-fixture-key");
});
