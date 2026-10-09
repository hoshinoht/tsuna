import { resolve, join } from "node:path";

export const root = resolve(process.env.TSUNA_ROOT ?? resolve(import.meta.dir, ".."));
export const runtimeAgentDir = join(root, ".runtime/omp/agent");

/** Compatibility entry points delegate all standalone operations to Rust. */
export async function runNative(args: string[]): Promise<number> {
  const executable = process.env.TSUNA_NATIVE_BIN;
  const command = executable ? [executable, "--root", root, ...args] : [join(root, "bin/tsuna"), ...args];
  const child = Bun.spawn(command, { stdin: "inherit", stdout: "inherit", stderr: "inherit" });
  const interrupt = () => child.kill("SIGINT");
  const terminate = () => child.kill("SIGTERM");
  process.on("SIGINT", interrupt);
  process.on("SIGTERM", terminate);
  try { return await child.exited; }
  finally {
    process.off("SIGINT", interrupt);
    process.off("SIGTERM", terminate);
  }
}
