/**
 * Minimal streamable-HTTP MCP server fixture (JSON responses, no SSE).
 * `startHttpFixture()` returns its URL, the received request log and a stop().
 */
export function startHttpFixture(opts: { token?: string } = {}) {
	const calls: string[] = [];
	const server = Bun.serve({
		port: 0,
		hostname: "127.0.0.1",
		async fetch(req) {
			if (req.method !== "POST") return new Response(null, { status: 405 });
			if (opts.token && req.headers.get("authorization") !== `Bearer ${opts.token}`) return new Response("unauthorized", { status: 401 });
			const msg = (await req.json()) as { id?: number | string; method: string; params?: Record<string, unknown> };
			const reply = (result: unknown, headers: Record<string, string> = {}) =>
				new Response(JSON.stringify({ jsonrpc: "2.0", id: msg.id, result }), { headers: { "content-type": "application/json", ...headers } });
			switch (msg.method) {
				case "initialize":
					return reply({ protocolVersion: (msg.params?.protocolVersion as string) ?? "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "fixture-http", version: "0.0.1" } }, { "mcp-session-id": "fixture-session" });
				case "tools/list":
					return reply({ tools: [{ name: "lookup", description: "Look up a library id", inputSchema: { type: "object", properties: { q: { type: "string" } } } }] });
				case "tools/call":
					calls.push(JSON.stringify(msg.params));
					return reply({ content: [{ type: "text", text: `lookup:${(msg.params?.arguments as { q?: string })?.q}` }] });
				default:
					if (msg.id === undefined) return new Response(null, { status: 202 });
					return reply({});
			}
		},
	});
	return { url: `http://127.0.0.1:${server.port}/mcp`, calls, stop: () => server.stop(true) };
}
