#!/usr/bin/env bun
/**
 * `tsuna` (experiment): standalone entry point executing the Tsuna runtime.
 *
 *   tsuna [--config FILE] [--state DIR] [--cwd DIR] [--role ROLE] [--resume ROOT]
 *         [--headless] [--prompt TEXT] [--script FILE] [--format text|jsonl]
 *   tsuna roots [--state DIR]        list root sessions in the state directory
 *
 * Interactive mode (default when stdin is a TTY and not --headless) prompts for
 * approvals. Headless mode never prompts: asks fail closed. Events are
 * printed as text or JSON lines.
 */
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { Harness } from "./harness.ts";
import type { ApprovalRequest } from "./policy/gate.ts";
import { resolvePaths } from "./paths.ts";
import type { RuntimeEvent } from "./orchestration/types.ts";
import { HELP, runCommand } from "./ui/commands.ts";

interface Args {
	config?: string;
	state?: string;
	cwd: string;
	role?: string;
	resume?: string;
	headless: boolean;
	prompt?: string;
	script?: string;
	format: "text" | "jsonl";
	command?: string;
}

function parseArgs(argv: string[]): Args {
	const args: Args = { cwd: process.env.TSUNA_LAUNCH_CWD || process.cwd(), headless: false, format: "text" };
	for (let i = 0; i < argv.length; i++) {
		const a = argv[i]!;
		const value = () => {
			const v = argv[++i];
			if (v === undefined) throw new Error(`${a} needs a value`);
			return v;
		};
		switch (a) {
			case "--config": args.config = value(); break;
			case "--state": args.state = value(); break;
			case "--cwd": args.cwd = value(); break;
			case "--role": args.role = value(); break;
			case "--resume": args.resume = value(); break;
			case "--headless": args.headless = true; break;
			case "--prompt": args.prompt = value(); break;
			case "--script": args.script = value(); break;
			case "--format": {
				const f = value();
				if (f !== "text" && f !== "jsonl") throw new Error("--format is text or jsonl");
				args.format = f;
				break;
			}
			case "-h":
			case "--help":
				args.command = "help";
				break;
			default:
				if (!a.startsWith("-") && !args.command) args.command = a;
				else throw new Error(`unknown argument ${a}`);
		}
	}
	return args;
}

const USAGE = `tsuna (experimental harness)
  tsuna [--config FILE] [--state DIR] [--cwd DIR] [--role ROLE] [--resume ROOT]
        [--headless] [--prompt TEXT] [--script FILE] [--format text|jsonl]
  tsuna roots [--state DIR]

${HELP}`;

function eventText(e: RuntimeEvent): string | undefined {
	switch (e.type) {
		case "agent_spawned": return `+ ${e.agentId} (${e.role}) spawned by ${e.parentId}`;
		case "agent_status": return `· ${e.agentId}: ${e.prior} → ${e.status}`;
		case "agent_result": {
			const r = e.result as { resultId: string; status: string; data?: unknown; error?: string };
			return `★ result ${r.resultId} ${r.status}: ${JSON.stringify(r.data ?? r.error ?? null).slice(0, 300)}`;
		}
		case "agent_output": return `… ${e.agentId} output: ${JSON.stringify((e.output as { data: unknown }).data).slice(0, 200)}`;
		case "assistant_text": return `${e.agentId}: ${String(e.text).slice(0, 400)}`;
		case "permission": return e.allowed ? undefined : `⛔ ${e.agentId} ${e.tool} denied: ${e.reason}`;
		case "message": return `✉ ${e.from} → ${e.to} (${e.mode}): ${String(e.message).slice(0, 200)}`;
		case "warning": return `! ${e.agentId ? `${e.agentId}: ` : ""}${e.message}`;
		default: return undefined;
	}
}

async function main() {
	const args = parseArgs(process.argv.slice(2));
	if (args.command === "help") {
		console.log(USAGE);
		return 0;
	}
	if (args.command === "roots") {
		const paths = resolvePaths(args.state);
		const dir = paths.agents;
		const roots = existsSync(dir) ? readdirSync(dir) : [];
		for (const root of roots) console.log(root);
		return 0;
	}
	if (args.command) throw new Error(`unknown command ${args.command}`);
	const interactive = !args.headless && process.stdin.isTTY === true;
	const out = (line: string) => process.stdout.write(`${line}\n`);
	const emit = (e: RuntimeEvent) => {
		if (args.format === "jsonl") out(JSON.stringify({ ts: Date.now(), ...e }));
		else {
			const text = eventText(e);
			if (text) out(text);
		}
	};
	const rl = interactive ? createInterface({ input: process.stdin, output: process.stdout, prompt: "tsuna> " }) : undefined;
	// Start iterating immediately so lines typed during startup are buffered, not dropped.
	const lines = rl?.[Symbol.asyncIterator]();
	let pendingApproval: ((answer: boolean) => void) | undefined;
	const approver = rl
		? (request: ApprovalRequest) =>
				new Promise<boolean>(resolve => {
					const d = request.decision;
					out(`\n⚠ Permission required: ${request.agentId} (${request.role}) wants ${request.toolName} → ${d.action} ${d.resource}`);
					out(`  reason: ${d.reason ?? (d.rule ? `rule ${d.rule.effect} ${d.rule.action} ${d.rule.resource}` : "ask")}`);
					out("  allow? [y/N]");
					pendingApproval = resolve;
				})
		: undefined;
	const harness = await Harness.start({
		config: args.config,
		state: args.state,
		cwd: args.cwd,
		interactive,
		role: args.role,
		resume: args.resume,
		approver,
		onEvent: emit,
	});
	const rt = harness.runtime;
	const report = (text: string, kind = "info", data?: unknown) => {
		if (args.format === "jsonl") out(JSON.stringify({ ts: Date.now(), type: "command", kind, text, data }));
		else if (text) out(text);
	};
	report(`root ${rt.rootId} · primary ${rt.primaryId} (${rt.record(rt.primaryId)!.role}) · state ${harness.paths.state}${interactive ? "" : " · headless (approvals fail closed)"}`, "start", { rootId: rt.rootId, primaryId: rt.primaryId, state: harness.paths.state });
	for (const w of rt.warnings) report(`! ${w}`, "warning");
	let code = 0;
	const shutdown = async () => {
		await harness.shutdown();
		report(`shutdown complete · root ${rt.rootId} (resume with --resume ${rt.rootId})`, "shutdown", { rootId: rt.rootId, liveSessions: rt.diagnostics().liveSessions, mcpConnected: harness.mcp.connectedCount() });
	};
	process.once("SIGINT", () => {
		void shutdown().then(() => process.exit(130));
	});
	try {
		if (args.prompt) {
			await rt.promptPrimary(args.prompt);
			await rt.quiesce(600_000);
		}
		if (args.script) {
			const lines = readFileSync(args.script, "utf8").split("\n");
			for (const raw of lines) {
				const line = raw.trim();
				if (!line || line.startsWith("#")) continue;
				report(`tsuna> ${line}`, "input");
				const sleep = /^\/sleep\s+(\d+)$/.exec(line);
				if (sleep) {
					await Bun.sleep(Number(sleep[1]));
					continue;
				}
				const result = await runCommand(harness, line);
				report(result.text, result.kind, result.data);
				if (result.kind === "error") code = 1;
				if (result.kind === "quit") break;
			}
		}
		if (rl) {
			out("Type /help for commands.");
			rl.prompt();
			for (let next = await lines!.next(); !next.done; next = await lines!.next()) {
				const line = next.value;
				if (pendingApproval) {
					const resolve = pendingApproval;
					pendingApproval = undefined;
					resolve(/^y(es)?$/i.test(line.trim()));
					rl.prompt();
					continue;
				}
				const result = await runCommand(harness, line);
				if (result.text) out(result.text);
				if (result.kind === "quit") break;
				rl.prompt();
			}
			rl.close();
		}
	} finally {
		await shutdown();
	}
	return code;
}

main().then(
	code => process.exit(code),
	error => {
		process.stderr.write(`tsuna: ${(error as Error).message}\n`);
		process.exit(2);
	},
);
