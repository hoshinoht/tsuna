import { runNative } from "./rust-cli";
process.exit(await runNative(["import-opencode", ...process.argv.slice(2)]));
