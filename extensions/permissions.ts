import type { ExtensionAPI, ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import policies from "../config/permissions.json" with { type: "json" };
import preference from "../config/permission-mode.json" with { type: "json" };
import reviewer from "../config/permission-reviewer.json" with { type: "json" };
import { reviewPermission, formatPermissionReview, shouldAutoApprovePermission, type PermissionReview } from "../lib/permission-review.ts";
import {
	asToolCall,
	decidePermission,
	intentForToolCall,
	roleForAgent,
	type PermissionPolicies,
	type ToolCall,
} from "../lib/permissions.ts";
import { currentCoordinationScope, registerScopedCoordinationReads } from "../lib/coordination.ts";

const permissionPolicies = policies as PermissionPolicies;
interface PendingReview { call: ToolCall; role: string; sessionId: string; review?: Promise<PermissionReview | undefined> }

export default function permissionsExtension(pi: ExtensionAPI): void {
	registerScopedCoordinationReads();
	const pending = new Map<string, PendingReview>();
	async function vet(id: string, request: PendingReview, reason: string | undefined, ctx: ExtensionContext) {
		request.review ??= reviewPermission(request.call, request.role, reason, ctx, reviewer).then(result => {
			if (result) pi.appendEntry("tsuna-permission-review", { toolCallId: id, model: result.model, status: result.status,
				...(result.status === "reviewed" ? { risk: result.risk, scope: result.scope, usage: result.usage } : {}) });
			return result;
		});
		return request.review;
	}
	pi.registerCommand("tsuna-permissions", {
		description: "Select auto approval or the original source permission prompts.",
		async handler(args, ctx) {
			const mode = args.trim();
			if (mode && mode !== "auto" && mode !== "source") throw new Error("Use /tsuna-permissions auto or source");
			if (mode) process.env.TSUNA_PERMISSION_MODE = mode;
			ctx.ui.notify(`Tsuna permission mode: ${process.env.TSUNA_PERMISSION_MODE ?? preference.mode}; reviewer: ${reviewer.enabled ? `GPT-6-Luna (${reviewer.mode})` : "off"}`, "info");
		},
	});
	pi.on("tool_call", async (event, ctx) => {
		const role = roleForAgent(ctx.agent);
		const mode = process.env.TSUNA_PERMISSION_MODE ?? preference.mode;
		const call = asToolCall(event.toolName, event.input);
		const target = intentForToolCall(call).coordinationTarget;
		const coordinationScope = target ? await currentCoordinationScope(ctx) : undefined;
		const decision = decidePermission(permissionPolicies, role, call, { cwd: ctx.cwd, approvalMode: mode === "auto" ? "auto" : "source", coordinationScope });
		const revisedPath = target?.scheme === "agent" ? `tsuna-agent://${target.id}${target.pathname}` :
			target?.scheme === "history" && target.id === "" ? "tsuna-history://" : undefined;
		const revisedInput = revisedPath && target && !target.invalid && event.toolName === "read"
			? { ...call.input, path: `${revisedPath}${target.selector ? `:${target.selector}` : ""}` } : undefined;
		if (decision.effect === "deny") return { block: true, reason: decision.reason ?? "Denied by Tsuna policy" };
		const request = { call: revisedInput ? { ...call, input: revisedInput } : call, role, sessionId: ctx.sessionManager.getSessionId() };
		if (ctx.agent.kind === "main" && ctx.hasUI) {
			pending.set(event.toolCallId, request);
			if (pending.size > 64) pending.delete(pending.keys().next().value!);
		}
		if (decision.effect === "allow") return revisedInput ? { input: revisedInput } : undefined;

		// Task agents are headless: asking there would bypass the owner of the policy.
		if (ctx.agent.kind !== "main" || !ctx.hasUI) {
			return { block: true, reason: "This permission requires an interactive primary session" };
		}
		const review = await vet(event.toolCallId, request, decision.reason, ctx);
		if (shouldAutoApprovePermission(request.call, decision, review, reviewer)) {
			pi.appendEntry("tsuna-permission-decision", { toolCallId: event.toolCallId, source: "luna", approved: true });
			ctx.ui.notify(formatPermissionReview(review, false), "info");
			return revisedInput ? { input: revisedInput } : undefined;
		}
		const allowed = await ctx.ui.confirm(
			"Permission required",
			`${decision.reason ? `Reason: ${decision.reason}\n\n` : ""}${review ? `${formatPermissionReview(review)}\n\n` : ""}Allow ${decision.action} on ${decision.resource} for ${role}?`,
		);
		pi.appendEntry("tsuna-permission-decision", { toolCallId: event.toolCallId, source: "human", approved: allowed });
		if (!allowed) return { block: true, reason: "Permission was not approved" };
		return revisedInput ? { input: revisedInput } : undefined;
	});
	pi.on("tool_approval_requested", async (event, ctx) => {
		if (ctx.agent.kind !== "main" || !ctx.hasUI) return;
		const request = pending.get(event.toolCallId);
		if (!request || request.sessionId !== event.sessionId) return;
		const review = await vet(event.toolCallId, request, event.reason, ctx);
		if (review) ctx.ui.notify(formatPermissionReview(review), review.status === "reviewed" && review.risk === "low" ? "info" : "warning");
	});
	pi.on("tool_result", event => { pending.delete(event.toolCallId); });
	pi.on("tool_approval_resolved", event => { pending.delete(event.toolCallId); });
	pi.on("session_shutdown", () => { pending.clear(); });
	pi.on("session_start", () => { pending.clear(); });
}
