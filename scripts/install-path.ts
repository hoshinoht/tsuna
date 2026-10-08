import { chmod, lstat, mkdir, realpath, symlink } from "node:fs/promises";
import { homedir } from "node:os";
import { join, resolve } from "node:path";

const target = resolve(import.meta.dir, "../bin/hoshi-omp");
await chmod(target, 0o755);
await mkdir(join(homedir(), ".local/bin"), { recursive: true });
for (const command of ["hoshi-omp"]) {
  const link = join(homedir(), ".local/bin", command);
  const existing = await lstat(link).catch(() => null);
  if (existing) {
    if (existing.isSymbolicLink() && await realpath(link).catch(() => "") === target) continue;
    throw new Error(`Refusing to replace existing command: ${link}`);
  }
  await symlink(target, link);
  console.log(`Installed ${command} -> ${target}`);
}
