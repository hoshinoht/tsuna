import { runNative } from "./rust-cli";
process.exit(await runNative(["mcp-smoke", ...process.argv.slice(2)]));
