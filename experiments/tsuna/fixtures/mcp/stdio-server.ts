#!/usr/bin/env bun
/**
 * Minimal stdio MCP server used as a test fixture (newline-delimited JSON-RPC).
 *
 * Profiles (argv[2]):
 *   basic     tools: echo, roots, slow
 *   workplan  Shiori-shaped tool names (create, update, checkpoint, read, list) with
 *             "client" write approval: the server performs writes whenever called,
 *             exactly like `shiori mcp --write-approval client`, so the harness gate
 *             is the only authorization. Writes land in <root>/.tsuna-fixture-workplan.log.
 * Also logs every handled call to $FIXTURE_LOG (if set) so tests can prove denied
 * calls never reached the server.
 */
import { appendFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

const profile = process.argv[2] ?? "basic";
const log = (line: string) => process.env.FIXTURE_LOG && appendFileSync(process.env.FIXTURE_LOG, `${line}\n`);
log(`start ${profile} pid ${process.pid}`);
let roots: { uri: string }[] = [];
let rootsRequest = 0;
const pending = new Map<string, (v: unknown) => void>();
const cancelled = new Set<string | number>();

function send(message: unknown) {
	process.stdout.write(`${JSON.stringify(message)}\n`);
}

function text(t: string, isError = false) {
	return { content: [{ type: "text", text: t }], isError };
}

const tools: Record<string, { description: string; inputSchema: unknown; run: (args: Record<string, unknown>, id: string | number) => Promise<unknown> }> =
	profile === "workplan"
		? Object.fromEntries(
				["create", "update", "patch", "reset", "checkpoint", "compact", "read", "list", "inspect"].map(name => [
					name,
					{
						description: `workplan ${name}`,
						inputSchema: { type: "object", properties: { id: { type: "string" }, recovery: {}, mode: { type: "string" } } },
						run: async (args: Record<string, unknown>) => {
							const root = roots[0] ? fileURLToPath(roots[0].uri) : undefined;
							if (!root || roots.length !== 1) return text("workplan requires exactly one client root", true);
							if (!["read", "list", "inspect"].includes(name)) appendFileSync(join(root, ".tsuna-fixture-workplan.log"), `${name} ${JSON.stringify(args)}\n`);
							return text(`${name} ok in ${root}`);
						},
					},
				]),
			)
		: {
				echo: { description: "Echo text", inputSchema: { type: "object", properties: { text: { type: "string" } }, required: ["text"] }, run: async a => text(`echo: ${a.text}`) },
				roots: { description: "Report client roots", inputSchema: { type: "object", properties: {} }, run: async () => text(JSON.stringify(roots)) },
				slow: {
					description: "Sleep until cancelled or ms elapse",
					inputSchema: { type: "object", properties: { ms: { type: "number" } } },
					run: async (a, id) => {
						const until = Date.now() + Number(a.ms ?? 5000);
						while (Date.now() < until && !cancelled.has(id)) await Bun.sleep(10);
						return text(cancelled.has(id) ? "cancelled" : "slept");
					},
				},
			};

async function handle(msg: { id?: string | number; method?: string; params?: Record<string, unknown>; result?: unknown }) {
	if (msg.method === undefined && msg.id !== undefined) {
		pending.get(String(msg.id))?.(msg.result);
		return;
	}
	switch (msg.method) {
		case "initialize":
			send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: (msg.params?.protocolVersion as string) ?? "2025-06-18", capabilities: { tools: {}, resources: {} }, serverInfo: { name: `fixture-${profile}`, version: "0.0.1" } } });
			return;
		case "notifications/initialized": {
			const id = `roots-${++rootsRequest}`;
			pending.set(id, result => {
				roots = ((result as { roots?: { uri: string }[] })?.roots) ?? [];
			});
			send({ jsonrpc: "2.0", id, method: "roots/list" });
			return;
		}
		case "notifications/cancelled":
			cancelled.add((msg.params?.requestId as string | number) ?? "");
			return;
		case "tools/list":
			send({ jsonrpc: "2.0", id: msg.id, result: { tools: Object.entries(tools).map(([name, t]) => ({ name, description: t.description, inputSchema: t.inputSchema })) } });
			return;
		case "tools/call": {
			const name = String(msg.params?.name);
			log(`call ${name} ${JSON.stringify(msg.params?.arguments ?? {})}`);
			const tool = tools[name];
			const result = tool ? await tool.run((msg.params?.arguments as Record<string, unknown>) ?? {}, msg.id!) : text(`unknown tool ${name}`, true);
			send({ jsonrpc: "2.0", id: msg.id, result });
			return;
		}
		case "resources/list":
			send({ jsonrpc: "2.0", id: msg.id, result: { resources: [{ uri: "fixture://note", name: "note" }] } });
			return;
		case "resources/read":
			log(`resource ${msg.params?.uri}`);
			send({ jsonrpc: "2.0", id: msg.id, result: { contents: [{ uri: msg.params?.uri, text: `resource ${msg.params?.uri} from ${profile}` }] } });
			return;
		case "ping":
			send({ jsonrpc: "2.0", id: msg.id, result: {} });
			return;
		default:
			if (msg.id !== undefined) send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: `no method ${msg.method}` } });
	}
}

let buffer = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", chunk => {
	buffer += chunk;
	let nl: number;
	while ((nl = buffer.indexOf("\n")) >= 0) {
		const line = buffer.slice(0, nl).trim();
		buffer = buffer.slice(nl + 1);
		if (line) void handle(JSON.parse(line));
	}
});
process.stdin.on("end", () => process.exit(0));
