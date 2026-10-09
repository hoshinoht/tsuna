import { runNative } from "./rust-cli";
process.exit(await runNative(["proxy", ...process.argv.slice(2)]));
