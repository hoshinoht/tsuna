/**
 * MCP client manager. Servers are independent programs or remote services
 * configured in a separate `mcp.json`; the harness only connects to entries
 * that are present and enabled (no discovery). Every tool call and resource
 * read is a Tsuna tool invocation and therefore passes the permission gate
 * before reaching this module.
 */
import { pathToFileURL } from "node:url";
import { McpClient, StdioTransport, StreamableHttpTransport, type Tool as McpTool } from "@earendil-works/pi-mcp";
import { Type, type TSchema } from "typebox";
import type { McpConfig, McpServerConfig } from "../config.ts";
import { mcpToolName } from "../policy/engine.ts";
import { fail, ok, type ToolImpl } from "../tools/types.ts";

export interface McpServerState {
	name: string;
	enabled: boolean;
	type: "stdio" | "http";
	status: "disabled" | "connected" | "failed" | "closed";
	error?: string;
	tools: string[];
}

function resolveEnv(server: McpServerConfig, env: NodeJS.ProcessEnv): Record<string, string> {
	const allowed = new Set(server.envAllow ?? []);
	const out: Record<string, string> = {};
	if (env.PATH) out.PATH = env.PATH;
	for (const [key, raw] of Object.entries(server.env ?? {})) {
		out[key] = raw.replace(/\$\{([A-Z0-9_]+)\}/g, (_, name: string) => {
			if (!allowed.has(name)) throw new Error(`env placeholder \${${name}} is not in envAllow`);
			return env[name] ?? "";
		});
	}
	return out;
}

function resolveHeaders(server: McpServerConfig, env: NodeJS.ProcessEnv): Record<string, string> | undefined {
	if (!server.headers) return undefined;
	const allowed = new Set(server.envAllow ?? []);
	const out: Record<string, string> = {};
	for (const [key, raw] of Object.entries(server.headers)) {
		out[key] = raw.replace(/\$\{([A-Z0-9_]+)\}/g, (_, name: string) => {
			if (!allowed.has(name)) throw new Error(`header placeholder \${${name}} is not in envAllow`);
			return env[name] ?? "";
		});
	}
	return out;
}

function contentToText(result: { content?: unknown[]; structuredContent?: unknown }): string {
	const parts: string[] = [];
	for (const block of result.content ?? []) {
		const b = block as { type?: string; text?: string; resource?: { uri?: string; text?: string } };
		if (b.type === "text") parts.push(b.text ?? "");
		else if (b.type === "resource") parts.push(b.resource?.text ?? `[resource ${b.resource?.uri}]`);
		else parts.push(`[${b.type} content]`);
	}
	if (parts.length === 0 && result.structuredContent !== undefined) parts.push(JSON.stringify(result.structuredContent));
	return parts.join("\n");
}

export class McpManager {
	private readonly clients = new Map<string, McpClient>();
	readonly states = new Map<string, McpServerState>();
	private readonly tools = new Map<string, ToolImpl>();

	constructor(
		private readonly config: McpConfig,
		/** The single project root advertised to every server (Shiori requires exactly one). */
		private readonly workspace: string,
		private readonly env: NodeJS.ProcessEnv = process.env,
	) {
		for (const [name, server] of Object.entries(config.servers)) {
			this.states.set(name, { name, enabled: server.enabled, type: server.type, status: server.enabled ? "closed" : "disabled", tools: [] });
		}
	}

	/** Names of configured *and enabled* servers (policy needs these to map tool names). */
	enabledServers(): Set<string> {
		return new Set([...this.states.values()].filter(s => s.enabled).map(s => s.name));
	}

	async connectAll(timeoutMs = 15_000): Promise<void> {
		await Promise.all([...this.states.values()].filter(s => s.enabled).map(s => this.connect(s.name, timeoutMs)));
	}

	private async connect(name: string, timeoutMs: number): Promise<void> {
		const server = this.config.servers[name]!;
		const state = this.states.get(name)!;
		const root = { uri: pathToFileURL(this.workspace).href, name: "workspace" };
		const client = new McpClient({ name: "tsuna-experiment", version: "0.0.1", requestTimeoutMs: server.timeoutMs ?? 60_000, roots: [root] });
		try {
			const transport = server.type === "stdio"
				? new StdioTransport({ command: server.command!, args: server.args ?? [], cwd: this.workspace, env: resolveEnv(server, this.env), inheritEnv: false, stderr: "pipe" })
				: new StreamableHttpTransport({ url: server.url!, headers: resolveHeaders(server, this.env), openGetStream: false });
			await withTimeout(client.connect(transport), timeoutMs, `connect ${name}`);
			this.clients.set(name, client);
			state.status = "connected";
			client.onClose(() => {
				if (state.status === "connected") state.status = "closed";
			});
			const tools = await withTimeout(client.listTools(), timeoutMs, `list tools ${name}`);
			for (const tool of tools) this.register(name, tool);
		} catch (error) {
			state.status = "failed";
			state.error = (error as Error).message;
			await client.close().catch(() => undefined);
		}
	}

	private register(server: string, tool: McpTool) {
		const safe = tool.name.replace(/[^A-Za-z0-9_-]/g, "_");
		const name = mcpToolName(server, safe);
		if (this.tools.has(name) || name.length > 128) return;
		const client = () => this.clients.get(server);
		this.states.get(server)!.tools.push(name);
		this.tools.set(name, {
			name,
			description: `[${server}] ${tool.description ?? tool.name}`,
			parameters: (tool.inputSchema ?? Type.Object({})) as TSchema,
			async execute(ctx, input) {
				const c = client();
				if (!c) return fail(`MCP server ${server} is not connected`);
				const result = await c.callTool(tool.name, input, { signal: ctx.signal });
				const text = contentToText(result as never);
				return (result as { isError?: boolean }).isError ? fail(text) : ok(text, { structured: (result as { structuredContent?: unknown }).structuredContent });
			},
		});
	}

	/** MCP tools plus the generic resource reader. */
	toolImpls(): Map<string, ToolImpl> {
		const map = new Map(this.tools);
		if (this.enabledServers().size > 0) {
			map.set("mcp_resource", {
				name: "mcp_resource",
				description: "Read a resource from a configured MCP server.",
				parameters: Type.Object({ server: Type.String(), uri: Type.String() }),
				execute: async (ctx, input) => {
					const c = this.clients.get(String(input.server));
					if (!c) return fail(`MCP server ${input.server} is not connected`);
					const result = await c.readResource(String(input.uri), { signal: ctx.signal });
					const text = result.contents.map(item => ("text" in item ? item.text : `[blob ${item.uri}]`)).join("\n");
					return ok(text);
				},
			});
		}
		return map;
	}

	connectedCount(): number {
		return this.clients.size;
	}

	/** Close every connection; awaits transport shutdown (stdio children exit). */
	async shutdown(): Promise<void> {
		const closing = [...this.clients.entries()].map(async ([name, client]) => {
			await client.close().catch(() => undefined);
			const state = this.states.get(name);
			if (state) state.status = "closed";
		});
		await Promise.allSettled(closing);
		this.clients.clear();
	}
}

async function withTimeout<T>(promise: Promise<T>, ms: number, what: string): Promise<T> {
	let timer: ReturnType<typeof setTimeout> | undefined;
	try {
		return await Promise.race([
			promise,
			new Promise<never>((_, reject) => {
				timer = setTimeout(() => reject(new Error(`${what} timed out after ${ms}ms`)), ms);
			}),
		]);
	} finally {
		clearTimeout(timer);
	}
}
