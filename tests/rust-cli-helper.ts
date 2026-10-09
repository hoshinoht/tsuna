import { resolve } from "node:path";

export const nativeBinary = resolve(import.meta.dir, "../native/tsuna/target/debug/tsuna");

export function rustCli(root: string, args: string[], env: Record<string, string> = {}) {
  const result = Bun.spawnSync([nativeBinary, "--root", root, ...args], {
    env: { ...process.env, ...env }, stdin: "ignore", stdout: "pipe", stderr: "pipe",
  });
  return { code: result.exitCode, stdout: result.stdout.toString(), stderr: result.stderr.toString() };
}
