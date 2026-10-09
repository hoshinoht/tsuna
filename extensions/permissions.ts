import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import policies from "../config/permissions.json" with { type: "json" };
import preference from "../config/permission-mode.json" with { type: "json" };
import {
	asToolCall,
	decidePermission,
	intentForToolCall,
	roleForAgent,
	type PermissionPolicies,
} from "../lib/permissions.ts";
import { currentCoordinationScope, registerScopedCoordinationReads } from "../lib/coordination.ts";

const permissionPolicies = policies as PermissionPolicies;

export default function permissionsExtension(pi: ExtensionAPI): void {
	registerScopedCoordinationReads();
	pi.registerCommand("tsuna-permissions", {
		description: "Select auto approval or the original source permission prompts.",
		async handler(args, ctx) {
			const mode = args.trim();
			if (mode && mode !== "auto" && mode !== "source") throw new Error("Use /tsuna-permissions auto or source");
			if (mode) process.env.TSUNA_PERMISSION_MODE = mode;
			ctx.ui.notify(`Tsuna permission mode: ${process.env.TSUNA_PERMISSION_MODE ?? preference.mode}`, "info");
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
		if (decision.effect === "allow") return revisedInput ? { input: revisedInput } : undefined;

		// Task agents are headless: asking there would bypass the owner of the policy.
		if (ctx.agent.kind !== "main" || !ctx.hasUI) {
			return { block: true, reason: "This permission requires an interactive primary session" };
		}
		const allowed = await ctx.ui.confirm(
			"Permission required",
			`${decision.reason ? `Reason: ${decision.reason}\n\n` : ""}Allow ${decision.action} on ${decision.resource} for ${role}?`,
		);
		pi.appendEntry("tsuna-permission-decision", { toolCallId: event.toolCallId, source: "human", approved: allowed });
		if (!allowed) return { block: true, reason: "Permission was not approved" };
		return revisedInput ? { input: revisedInput } : undefined;
	});
}
