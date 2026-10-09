import { runNative } from "./rust-cli";
export { root, runtimeAgentDir } from "./rust-cli";

export async function prepare(): Promise<void> {
  const code = await runNative(["setup"]);
  if (code !== 0) throw new Error(`Tsuna setup exited with status ${code}`);
}
if (import.meta.main) process.exit(await runNative(["setup", ...process.argv.slice(2)]));
