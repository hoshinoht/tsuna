/**
 * Test isolation: every test process gets a throwaway HOME/XDG/Pi location so
 * nothing can read or write the user's real profiles, caches or credentials.
 */
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const sandbox = mkdtempSync(join(tmpdir(), "tsuna-test-home-"));
process.env.HOME = sandbox;
process.env.XDG_CONFIG_HOME = join(sandbox, ".config");
process.env.XDG_DATA_HOME = join(sandbox, ".local/share");
process.env.XDG_CACHE_HOME = join(sandbox, ".cache");
process.env.PI_CODING_AGENT_DIR = join(sandbox, "pi-agent-must-not-be-used");
process.env.PI_OFFLINE = "1";
delete process.env.TSUNA_ROOT;
delete process.env.TSUNA_EXPERIMENT_CONFIG;
delete process.env.TSUNA_EXPERIMENT_STATE;
(globalThis as Record<string, unknown>).__TSUNA_TEST_HOME__ = sandbox;
