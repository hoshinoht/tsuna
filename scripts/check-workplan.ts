import { runNative } from "./rust-cli";
process.exit(await runNative(["check-workplan", ...process.argv.slice(2)]));
