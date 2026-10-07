#!/usr/bin/env node
// Durable OpenCode wrapper for lsp-tools-mcp.
// Upstream dist/cli.js self-terminates after 10 min idle (DEFAULT_IDLE_TIMEOUT_MS),
// which OpenCode reports as "Connection closed / mcp died" and does not respawn
// promptly. Disable the idle timer so the stdio server stays alive like the
// other local MCPs (researcher-mcp, gofetch).
import { stderr } from "node:process";

import { disposeDefaultLspManager } from "./dist/lsp/manager.js";
import { installProcessSignalCleanup } from "./dist/lsp/process-signal-cleanup.js";
import { runMcpStdioServer } from "./dist/mcp.js";
import { writeMcpLifecycleLog } from "./dist/mcp-lifecycle-log.js";

async function main() {
	try {
		const removeSignalCleanup = installProcessSignalCleanup(disposeDefaultLspManager, {
			terminateParent: true,
		});
		try {
			await runMcpStdioServer(process.stdin, process.stdout, {
				log: writeMcpLifecycleLog,
				idleTimeoutMs: 0,
			});
		} finally {
			removeSignalCleanup();
		}
	} finally {
		await disposeDefaultLspManager();
	}
}

main().catch(async (error) => {
	stderr.write(`${error instanceof Error ? (error.stack ?? error.message) : String(error)}\n`);
	await disposeDefaultLspManager();
	process.exitCode = 1;
});
