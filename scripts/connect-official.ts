import { runNative } from "./rust-cli";
process.exit(await runNative(["connect-official", ...process.argv.slice(2)]));
