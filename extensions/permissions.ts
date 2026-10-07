import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import policies from "../config/permissions.json" with { type: "json" };
import preference from "../config/permission-mode.json" with { type: "json" };
import {
	asToolCall,
	decidePermission,
	roleForAgent,
	type PermissionPolicies,
} from "../lib/permissions.ts";

const permissionPolicies = policies as PermissionPolicies;

export default function permissionsExtension(pi: ExtensionAPI): void {
	pi.registerCommand("hoshi-permissions", {
		description: "Select auto approval or the original source permission prompts.",
		async handler(args, ctx) {
			const mode = args.trim();
			if (mode && mode !== "auto" && mode !== "source") throw new Error("Use /hoshi-permissions auto or source");
			if (mode) process.env.HOSHI_PERMISSION_MODE = mode;
			ctx.ui.notify(`Hoshi permission mode: ${process.env.HOSHI_PERMISSION_MODE ?? preference.mode}`, "info");
		},
	});
	pi.on("tool_call", async (event, ctx) => {
		const role = roleForAgent(ctx.agent);
		const mode = process.env.HOSHI_PERMISSION_MODE ?? preference.mode;
		const decision = decidePermission(permissionPolicies, role, asToolCall(event.toolName, event.input), { cwd: ctx.cwd, approvalMode: mode === "auto" ? "auto" : "source" });
		if (decision.effect === "deny") return { block: true, reason: decision.reason ?? "Denied by hoshi policy" };
		if (decision.effect === "allow") return;

		// Task agents are headless: asking there would bypass the owner of the policy.
		if (ctx.agent.kind !== "main" || !ctx.hasUI) {
			return { block: true, reason: "This permission requires an interactive primary session" };
		}
		const allowed = await ctx.ui.confirm(
			"Permission required",
			`Allow ${decision.action} on ${decision.resource} for ${role}?`,
		);
		if (!allowed) return { block: true, reason: "Permission was not approved" };
	});
}
