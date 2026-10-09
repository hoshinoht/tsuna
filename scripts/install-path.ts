import { runNative } from "./rust-cli";
process.exit(await runNative(["install-path", ...process.argv.slice(2)]));
