import { resolve } from "node:path";
import { homedir } from "node:os";
import { connectOfficialProfile } from "../lib/official-profile";

const backup = await connectOfficialProfile(resolve(import.meta.dir, ".."), homedir());
console.log("Official OMP now uses the Hoshi agent profile and existing sessions.");
if (backup) console.log(`Previous default profile preserved at ${backup}`);
