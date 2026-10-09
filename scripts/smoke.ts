import { runNative } from "./rust-cli";
process.exit(await runNative(["smoke", ...process.argv.slice(2)]));
