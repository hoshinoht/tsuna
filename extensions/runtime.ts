import type { ExtensionFactory } from "@oh-my-pi/pi-coding-agent";
import { ThinkingLevel } from "@oh-my-pi/pi-agent-core";
import { backgroundJobViolation } from "../lib/background-jobs.ts";
import {
	admitCacheRisk,
	cacheRisk,
	formatCacheWarning,
	pruneContextImages,
	routeReasoning,
	validateRuntimeOptions,
	type CacheGuardState,
	type ImagePruneState,
	type ReasoningState,
} from "../lib/runtime.ts";

const optionsFile = new URL("../config/plugin-options.json", import.meta.url);
const thinkingLevelFor = {
	low: ThinkingLevel.Low,
	medium: ThinkingLevel.Medium,
	high: ThinkingLevel.High,
	xhigh: ThinkingLevel.XHigh,
	max: ThinkingLevel.Max,
} as const;

async function loadOptions(): Promise<ReturnType<typeof validateRuntimeOptions>> {
	try {
		const imported = await Bun.file(optionsFile).json();
		if (typeof imported !== "object" || imported === null || Array.isArray(imported)) {
			throw new Error("plugin options must be an object");
		}
		const source = imported as Record<string, unknown>;
		return validateRuntimeOptions({
			imageBudget: source["image-budget"],
			cacheGuard: source["cache-guard"],
			reasoningRouter: source["reasoning-router"],
		});
	} catch (error) {
		if (error instanceof SyntaxError || (error instanceof Error && error.message.includes("ENOENT"))) {
			throw new Error(`[runtime] cannot load ${optionsFile.pathname}: ${String(error)}`);
		}
		throw error;
	}
}

const extension: ExtensionFactory = async pi => {
	pi.setLabel("Tsuna runtime policies");
	const options = await loadOptions();
	const imageState: ImagePruneState = { sessions: new Map() };
	const cacheState: CacheGuardState = { warned: new Set(), diagnostics: [] };
	const reasoningState: ReasoningState = { sessions: new Map(), diagnostics: [] };

	pi.on("tool_call", event => {
		if (event.toolName !== "bash") return;
		const reason = backgroundJobViolation({ ...event.input });
		if (reason) return { block: true, reason };
	});

	pi.on("context", async (event, ctx) => {
		const sessionID = ctx.sessionManager.getSessionId();
		const identity = ctx.agent.kind === "main" ? process.env.TSUNA_AGENT ?? ctx.agent.name : ctx.agent.name;
		const routed = routeReasoning(reasoningState, sessionID, identity, ctx.model, event.messages, options.reasoningRouter);
		if (routed.decision?.effort) pi.setThinkingLevel(thinkingLevelFor[routed.decision.effort]);

		const imageResult = pruneContextImages(imageState, sessionID, routed.messages as unknown[], options.imageBudget, ctx.model?.provider);
		if (imageResult.changed) {
			pi.logger.warn("runtime image budget pruned outbound context", { sessionID, agent: identity, images: imageResult.images, bytes: imageResult.bytes });
		}

		const risk = cacheRisk(imageResult.messages, options.cacheGuard, ctx.model);
		if (risk) {
			const diagnostic = admitCacheRisk(cacheState, sessionID, risk, options.cacheGuard.diagnosticsLimit);
			if (diagnostic) {
				const message = formatCacheWarning(diagnostic);
				pi.logger.warn("runtime cache advisory", { sessionID, provider: diagnostic.provider, model: diagnostic.model, cacheReadTokens: diagnostic.cacheReadTokens });
				ctx.ui.notify(message, "warning");
			}
		}
		return { messages: imageResult.messages as typeof event.messages };
	});

	pi.registerTool({
			name: "cache_guard_status",
			label: "Cache guard status",
			description: "Show recent cache-reuse risk advisories. Prompt text and credentials are never stored.",
			parameters: pi.typebox.Type.Object({ limit: pi.typebox.Type.Optional(pi.typebox.Type.Integer({ minimum: 1 })) }),
			execute: async (_id, input, _signal, _update, ctx) => {
				const sessionID = ctx.sessionManager.getSessionId();
				const limit = Math.min(input.limit ?? options.cacheGuard.diagnosticsLimit, options.cacheGuard.diagnosticsLimit);
				const entries = cacheState.diagnostics.filter(entry => entry.sessionID === sessionID).slice(-limit);
				return { content: [{ type: "text", text: JSON.stringify({ plugin: "cache-guard", entries }, null, 2) }], details: { entries: entries.length } };
			},
	});

	pi.registerTool({
			name: "reasoning_router_status",
			label: "Reasoning router status",
			description: "Show recent per-agent reasoning decisions. Prompt text is never stored.",
			parameters: pi.typebox.Type.Object({ limit: pi.typebox.Type.Optional(pi.typebox.Type.Integer({ minimum: 1 })) }),
			execute: async (_id, input) => {
				const limit = Math.min(input.limit ?? options.reasoningRouter.diagnosticsLimit, options.reasoningRouter.diagnosticsLimit);
				const entries = reasoningState.diagnostics.slice(-limit);
				return { content: [{ type: "text", text: JSON.stringify({ plugin: "reasoning-router", entries }, null, 2) }], details: { entries: entries.length } };
			},
	});
};

export default extension;
