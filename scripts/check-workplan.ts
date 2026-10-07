import { resolve } from "node:path";

const [workspace, id, ...extra] = process.argv.slice(2);
if (!workspace || !id || extra.length) {
  console.error("Usage: bun scripts/check-workplan.ts <workspace-root> <workplan-id>");
  process.exit(2);
}
const check = Bun.spawn([
  resolve(import.meta.dir, "../vendor/shiori/shiori"),
  "validate", id, "--root", resolve(workspace), "--json",
], { stdin: "ignore", stdout: "inherit", stderr: "inherit" });
process.exit(await check.exited);
