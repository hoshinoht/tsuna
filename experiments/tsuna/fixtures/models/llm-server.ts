/**
 * Local fixture LLM server speaking two wire formats, used to exercise OMP's
 * real provider clients offline:
 *   POST <any>/v1/messages        Anthropic Messages API (SSE)
 *   POST <any>/chat/completions   OpenAI Chat Completions API (SSE)
 * A `decide` callback chooses each reply from the parsed request.
 */
export interface FixtureLlmRequest {
	wire: "anthropic" | "openai";
	path: string;
	headers: Record<string, string>;
	body: Record<string, unknown>;
	/** Text of the last user/tool message, normalised across wires. */
	lastText: string;
	/** Names of tools offered in the request. */
	tools: string[];
}

export interface FixtureLlmReply {
	text?: string;
	thinking?: string;
	toolCall?: { name: string; arguments: Record<string, unknown> };
	/** Delay before the first byte (abort tests). */
	delayMs?: number;
}

function sse(events: unknown[], delayMs = 0, signal?: AbortSignal): Response {
	const encoder = new TextEncoder();
	const body = new ReadableStream({
		async start(controller) {
			if (delayMs) await Bun.sleep(delayMs);
			for (const e of events) {
				if (signal?.aborted) break;
				const line = typeof e === "string" ? `data: ${e}\n\n` : `${(e as { event?: string }).event ? `event: ${(e as { event: string }).event}\n` : ""}data: ${JSON.stringify((e as { data?: unknown }).data ?? e)}\n\n`;
				controller.enqueue(encoder.encode(line));
			}
			controller.close();
		},
	});
	return new Response(body, { headers: { "content-type": "text/event-stream", "cache-control": "no-cache" } });
}

function textOf(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.map(b => {
			const block = b as { type?: string; text?: string; content?: unknown };
			if (block.type === "text") return block.text ?? "";
			if (block.type === "tool_result") return textOf(block.content);
			return "";
		})
		.join("");
}

function anthropicReply(reply: FixtureLlmReply, model: string) {
	const events: unknown[] = [
		{ event: "message_start", data: { type: "message_start", message: { id: "msg_fixture", type: "message", role: "assistant", model, content: [], stop_reason: null, usage: { input_tokens: 10, output_tokens: 0 } } } },
	];
	let index = 0;
	if (reply.thinking) {
		events.push({ event: "content_block_start", data: { type: "content_block_start", index, content_block: { type: "thinking", thinking: "", signature: "" } } });
		events.push({ event: "content_block_delta", data: { type: "content_block_delta", index, delta: { type: "thinking_delta", thinking: reply.thinking } } });
		events.push({ event: "content_block_delta", data: { type: "content_block_delta", index, delta: { type: "signature_delta", signature: "fixture-signature" } } });
		events.push({ event: "content_block_stop", data: { type: "content_block_stop", index } });
		index++;
	}
	if (reply.text) {
		events.push({ event: "content_block_start", data: { type: "content_block_start", index, content_block: { type: "text", text: "" } } });
		events.push({ event: "content_block_delta", data: { type: "content_block_delta", index, delta: { type: "text_delta", text: reply.text } } });
		events.push({ event: "content_block_stop", data: { type: "content_block_stop", index } });
		index++;
	}
	if (reply.toolCall) {
		events.push({ event: "content_block_start", data: { type: "content_block_start", index, content_block: { type: "tool_use", id: `toolu_fixture_${Date.now()}`, name: reply.toolCall.name, input: {} } } });
		events.push({ event: "content_block_delta", data: { type: "content_block_delta", index, delta: { type: "input_json_delta", partial_json: JSON.stringify(reply.toolCall.arguments) } } });
		events.push({ event: "content_block_stop", data: { type: "content_block_stop", index } });
	}
	events.push({ event: "message_delta", data: { type: "message_delta", delta: { stop_reason: reply.toolCall ? "tool_use" : "end_turn", stop_sequence: null }, usage: { output_tokens: 7 } } });
	events.push({ event: "message_stop", data: { type: "message_stop" } });
	return events;
}

function openaiReply(reply: FixtureLlmReply, model: string) {
	const base = { id: "chatcmpl-fixture", object: "chat.completion.chunk", created: 1, model };
	const events: unknown[] = [{ data: { ...base, choices: [{ index: 0, delta: { role: "assistant", content: "" }, finish_reason: null }] } }];
	if (reply.thinking) events.push({ data: { ...base, choices: [{ index: 0, delta: { reasoning_content: reply.thinking }, finish_reason: null }] } });
	if (reply.text) events.push({ data: { ...base, choices: [{ index: 0, delta: { content: reply.text }, finish_reason: null }] } });
	if (reply.toolCall) {
		events.push({ data: { ...base, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: `call_fixture_${Date.now()}`, type: "function", function: { name: reply.toolCall.name, arguments: JSON.stringify(reply.toolCall.arguments) } }] }, finish_reason: null }] } });
	}
	events.push({ data: { ...base, choices: [{ index: 0, delta: {}, finish_reason: reply.toolCall ? "tool_calls" : "stop" }], usage: { prompt_tokens: 10, completion_tokens: 7, total_tokens: 17 } } });
	events.push("[DONE]");
	return events;
}

export function startLlmFixture(decide: (req: FixtureLlmRequest) => FixtureLlmReply) {
	const requests: FixtureLlmRequest[] = [];
	const server = Bun.serve({
		port: 0,
		hostname: "127.0.0.1",
		async fetch(req) {
			const url = new URL(req.url);
			if (req.method !== "POST") return new Response("not found", { status: 404 });
			const body = (await req.json()) as Record<string, unknown>;
			const headers = Object.fromEntries(req.headers.entries());
			const messages = (body.messages as { role: string; content: unknown }[]) ?? [];
			const last = messages.at(-1);
			if (url.pathname.endsWith("/v1/messages")) {
				const tools = ((body.tools as { name: string }[]) ?? []).map(t => t.name);
				const r: FixtureLlmRequest = { wire: "anthropic", path: url.pathname, headers, body, lastText: textOf(last?.content), tools };
				requests.push(r);
				const reply = decide(r);
				return sse(anthropicReply(reply, String(body.model)), reply.delayMs, req.signal);
			}
			if (url.pathname.endsWith("/chat/completions")) {
				const tools = ((body.tools as { function: { name: string } }[]) ?? []).map(t => t.function.name);
				const r: FixtureLlmRequest = { wire: "openai", path: url.pathname, headers, body, lastText: typeof last?.content === "string" ? last.content : textOf(last?.content), tools };
				requests.push(r);
				const reply = decide(r);
				return sse(openaiReply(reply, String(body.model)), reply.delayMs, req.signal);
			}
			return new Response(JSON.stringify({ error: { message: `fixture: unknown path ${url.pathname}` } }), { status: 404, headers: { "content-type": "application/json" } });
		},
	});
	return { url: `http://127.0.0.1:${server.port}`, requests, stop: () => server.stop(true) };
}
