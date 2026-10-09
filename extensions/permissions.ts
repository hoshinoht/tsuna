import type { ExtensionAPI, ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import policies from "../config/permissions.json" with { type: "json" };
import preference from "../config/permission-mode.json" with { type: "json" };
import reviewConfig from "../config/auto-review.json" with { type: "json" };
import {
	asToolCall,
	decidePermission,
	intentForToolCall,
	roleForAgent,
	surfaceTools,
	type PermissionPolicies,
	type ToolCall,
} from "../lib/permissions.ts";
import { currentCoordinationScope, registerScopedCoordinationReads } from "../lib/coordination.ts";
import { approvalHash, ReviewCircuit, routeAutoPermission } from "../lib/auto-permissions.ts";
import { AUTO_REVIEW_POLICY_VERSION, redactAutoReviewText, reviewAction } from "../lib/auto-review.ts";
import { buildAutoReviewEvidence } from "../lib/auto-review-context.ts";
import { CleanupTracker } from "../lib/auto-cleanup.ts";

const permissionPolicies = policies as PermissionPolicies;

interface Denial { hash: string; call: ToolCall; reason: string }
interface ReviewState {
	controller: AbortController;
	circuit: ReviewCircuit;
	queue: Promise<unknown>;
	denials: Denial[];
	overrides: Map<string, string>;
	cleanup: CleanupTracker;
}

export default function permissionsExtension(pi: ExtensionAPI, dependencies: {
	review?: typeof reviewAction;
	evidence?: typeof buildAutoReviewEvidence;
	initialMode?: "auto" | "source" | "off";
} = {}): void {
	const review = dependencies.review ?? reviewAction;
	const evidenceFor = dependencies.evidence ?? buildAutoReviewEvidence;
	const sessions = new Map<string, ReviewState>();
	const modes = new Map<string, string>();
	const key = (ctx: ExtensionContext) => `${ctx.sessionManager.getSessionId()}:${ctx.agent.id}`;
	const stateFor = (ctx: ExtensionContext) => {
		const id = key(ctx);
		let state = sessions.get(id);
		if (!state) {
			state = { controller: new AbortController(), circuit: new ReviewCircuit(), queue: Promise.resolve(), denials: [], overrides: new Map(), cleanup: new CleanupTracker() };
			sessions.set(id, state);
		}
		return state;
	};
	const modeFor = (ctx: ExtensionContext) => modes.get(key(ctx)) ?? dependencies.initialMode ?? process.env.TSUNA_PERMISSION_MODE ?? preference.mode;
	const block = (reason: string) => ({ block: true as const, reason });
	const cancel = (ctx: ExtensionContext) => sessions.get(key(ctx))?.controller.abort();
	const filterToolsForRole = async (ctx: ExtensionContext) => {
		if (typeof pi.getActiveTools !== "function" || typeof pi.setActiveTools !== "function") return;
		const role = roleForAgent(ctx.agent);
		const current = pi.getActiveTools();
		const allowed = surfaceTools(permissionPolicies, role, current);
		if (allowed.length !== current.length) {
			await pi.setActiveTools(allowed);
		}
	};
	pi.on("session_start", async (_event, ctx) => {
		await filterToolsForRole(ctx);
	});
	pi.on("agent_start", async (_event, ctx) => {
		const state = stateFor(ctx);
		state.controller.abort();
		state.controller = new AbortController();
		state.circuit = new ReviewCircuit();
		await filterToolsForRole(ctx);
	});
	pi.on("agent_end", (_event, ctx) => cancel(ctx));
	pi.on("session_shutdown", () => { for (const state of sessions.values()) state.controller.abort(); sessions.clear(); });
	pi.on("session_switch", async (_event, ctx) => {
		for (const state of sessions.values()) state.controller.abort();
		sessions.clear();
		modes.clear();
		await filterToolsForRole(ctx);
	});
	pi.on("session_tree", (_event, ctx) => { cancel(ctx); sessions.delete(key(ctx)); });
	pi.on("tool_result", async (event, ctx) => {
		await stateFor(ctx).cleanup.after(event.toolCallId, event.isError);
	});
	registerScopedCoordinationReads();
	pi.registerCommand("tsuna-permissions", {
		description: "Select auto review, source prompts, or off (auto-approve permission requests).",
		async handler(args, ctx) {
			const mode = args.trim();
			if (mode && mode !== "auto" && mode !== "source" && mode !== "off") throw new Error("Use /tsuna-permissions auto, source, or off");
			if (mode) { cancel(ctx); sessions.delete(key(ctx)); modes.set(key(ctx), mode); }
			ctx.ui.notify(`Tsuna permission mode: ${modeFor(ctx)}`, "info");
		},
	});
	pi.registerCommand("tsuna-approve", {
		description: "Approve one exact recently denied action for one reviewed retry.",
		async handler(args, ctx) {
			if (ctx.agent.kind !== "main" || !ctx.hasUI) throw new Error("Approval requires an interactive primary session");
			const state = stateFor(ctx);
			const labels = state.denials.map(item => `${item.hash.slice(0, 12)} ${item.call.toolName}: ${item.reason}`);
			const selected = args.trim() || await ctx.ui.select("Auto-review denials", labels);
			const matches = selected ? state.denials.filter(item => selected.startsWith(item.hash.slice(0, 12))) : [];
			if (matches.length !== 1) { ctx.ui.notify("No matching recent denial", "info"); return; }
			const denied = matches[0]!;
			if (!await ctx.ui.confirm("Approve one reviewed retry?", redactAutoReviewText(`${denied.call.toolName}\n${JSON.stringify(denied.call.input, null, 2)}\n\n${denied.reason}`))) return;
			const evidence = await evidenceFor(denied.call, roleForAgent(ctx.agent), ctx);
			if (!evidence.contextComplete) { ctx.ui.notify("Cannot verify authorization context; retry from a complete session", "warning"); return; }
			state.overrides.set(denied.hash, approvalHash(evidence.humanMessages));
			pi.appendEntry("tsuna-auto-review-override", { actionHash: denied.hash, scope: "one-retry" });
			pi.sendMessage({ customType: "tsuna-auto-review-retry", content: redactAutoReviewText(`The user approved one reviewed retry of this exact action: ${JSON.stringify(denied.call)}. Retry it if still needed. This grants no permission for similar actions or policy circumvention.`), display: true }, { triggerTurn: true });
		},
	});
	pi.on("tool_call", async (event, ctx) => {
		const role = roleForAgent(ctx.agent);
		const mode = modeFor(ctx);
		const automaticReview = mode === "auto";
		// Snapshot arguments so an asynchronous verdict cannot authorize a different payload.
		const call = asToolCall(event.toolName, structuredClone(event.input));
		const target = intentForToolCall(call).coordinationTarget;
		const coordinationScope = target ? await currentCoordinationScope(ctx) : undefined;
		const decision = decidePermission(permissionPolicies, role, call, { cwd: ctx.cwd, approvalMode: "source", coordinationScope });
		const revisedPath = target?.scheme === "agent" ? `tsuna-agent://${target.id}${target.pathname}` :
			target?.scheme === "history" && target.id === "" ? "tsuna-history://" : undefined;
		const revisedInput = revisedPath && target && !target.invalid && event.toolName === "read"
			? { ...call.input, path: `${revisedPath}${target.selector ? `:${target.selector}` : ""}` } : undefined;
		if (mode === "off") {
			if (decision.effect === "deny") return block(decision.reason ?? "Denied by Tsuna policy");
			return revisedInput ? { input: revisedInput } : undefined;
		}
		const route = automaticReview ? routeAutoPermission(call, decision, ctx.cwd) : decision.effect === "ask" ? "ask-human" : decision.effect;
		if (route === "deny") return block(decision.reason ?? "Denied by Tsuna policy");
		const state = stateFor(ctx);
		const signal = state.controller.signal;
		const accepted = async () => {
			if (signal.aborted || state.circuit.halted || approvalHash(call.input) !== approvalHash(event.input)) return block("Approval context changed; propose the action again");
			if (automaticReview) await state.cleanup.before(event.toolCallId, call, ctx.cwd);
			return revisedInput ? { input: revisedInput } : undefined;
		};
		const askHuman = async (reason?: string) => {
			if (ctx.agent.kind !== "main" || !ctx.hasUI) return block("This permission requires an interactive primary session. Return the exact action and reason to the parent; do not bypass it.");
			const allowed = await ctx.ui.confirm("Permission required", redactAutoReviewText(`${reason ?? decision.reason ?? ""}\n\nAllow ${decision.action} on ${decision.resource} for ${role}?\n\nTool: ${call.toolName}\nInput:\n${JSON.stringify(call.input, null, 2)}`));
			pi.appendEntry("tsuna-permission-decision", { toolCallId: event.toolCallId, source: "human", approved: allowed });
			return allowed ? accepted() : block("Permission was not approved");
		};
		if (state.circuit.halted) return block("Automatic review stopped this run. Human input is required before continuing.");
		if (route === "allow") return accepted();
		if (route === "ask-human") return askHuman();

		// Serialize decisions for a session so concurrent requests share one circuit
		// and one-use grants are consumed exactly once.
		const operation = state.queue.then(async () => {
			if (signal.aborted || state.circuit.halted) return block("Automatic review was cancelled or stopped; wait for human input");
			const started = Date.now();
			const evidence = await evidenceFor(call, role, ctx);
			evidence.cleanup = await state.cleanup.inspect(call, ctx.cwd);
			const hash = approvalHash({ call, cwd: ctx.cwd, role });
			const override = state.overrides.get(hash);
			state.overrides.delete(hash);
			if (override === approvalHash(evidence.humanMessages)) evidence.override = { actionHash: hash };
			const result = await review(evidence, ctx, reviewConfig, signal);
			if (signal.aborted) return block("Automatic review cancelled");
			const fresh = await evidenceFor(call, role, ctx);
			fresh.cleanup = await state.cleanup.inspect(call, ctx.cwd);
			fresh.override = evidence.override;
			if (approvalHash(fresh) !== approvalHash(evidence)) return block("Authorization or action context changed during review; propose it again");
			const verdict = result.status === "reviewed" ? result.verdict : "unavailable";
			pi.appendEntry("tsuna-auto-review-decision", { toolCallId: event.toolCallId, actionHash: hash, policyVersion: AUTO_REVIEW_POLICY_VERSION, model: result.model, verdict, reason: result.reason, elapsedMs: Date.now() - started,
				...(result.status === "reviewed" ? { authorizationIds: result.authorizationIds, usage: result.usage } : {}) });
			if (result.status === "reviewed" && result.verdict === "deny") state.denials = [...state.denials.filter(item => item.hash !== hash), { hash, call, reason: result.reason }].slice(-10);
			if (state.circuit.record(verdict)) {
				ctx.ui.notify("Automatic review stopped repeated safety denials. Inspect the blocked action before continuing.", "warning");
				ctx.abort();
				return block("Automatic review circuit breaker stopped this run; human input is required");
			}
			if (result.status === "unavailable") return askHuman(`${result.reason}\nThis is not an unsafe verdict.`);
			if (result.verdict === "allow") return accepted();
			return block(`Denied by automatic review: ${result.reason}\nFind a materially safer approach. Do not retry the same outcome through another tool or disguise it. If the action is necessary, ask the user for the exact missing authorization. The user can use /tsuna-approve ${hash.slice(0, 12)} for one reviewed retry.`);
		});
		state.queue = operation.catch(() => undefined);
		return operation;
	});
}
