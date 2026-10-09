/**
 * Execution-time permission gate.
 *
 * Every tool invocation (built-in, orchestration, MCP, nested batch calls,
 * native operations) passes through `PermissionGate.authorize` immediately
 * before it executes. The trusted role and policy come from the runtime
 * session binding, never from model-generated arguments or process-global
 * environment variables.
 *
 * Approval order for an `ask`:
 *   1. Headless sessions (every child, and a primary without UI) fail closed.
 *   2. An optional reviewer may waive only *generic* asks; it can never
 *      override a deny, and its failure preserves the requirement to ask.
 *   3. The interactive approver (human) decides.
 *
 * These checks are application policy inside the harness process, not an
 * operating-system sandbox.
 */
import { decide, type PermissionDecision, type PolicyContext, type ToolCall } from "./engine.ts";

export interface ApprovalRequest {
	agentId: string;
	role: string;
	toolName: string;
	input: Record<string, unknown>;
	decision: PermissionDecision;
}

export type Approver = (request: ApprovalRequest) => Promise<boolean>;

export interface ReviewVerdict {
	risk: "low" | "medium" | "high";
	scope: "within-request" | "outside-request";
	reason: string;
}

export type Reviewer = (request: ApprovalRequest, signal?: AbortSignal) => Promise<ReviewVerdict>;

export interface GateSession {
	agentId: string;
	/** True only for the primary session attached to an interactive UI. */
	interactive: boolean;
	policy: PolicyContext;
}

export interface AuditEntry {
	at: number;
	agentId: string;
	role: string;
	toolName: string;
	action: string;
	resource: string;
	effect: "allow" | "deny";
	source: "policy" | "human" | "reviewer" | "headless";
	reason?: string;
}

export type GateResult = { allowed: true; decision: PermissionDecision } | { allowed: false; decision: PermissionDecision; reason: string };

export class PermissionGate {
	readonly audit: AuditEntry[] = [];

	constructor(
		private readonly options: {
			approver?: Approver;
			reviewer?: Reviewer;
			onAudit?: (entry: AuditEntry) => void;
		} = {},
	) {}

	setApprover(approver: Approver | undefined): void {
		this.options.approver = approver;
	}

	private record(session: GateSession, call: ToolCall, decision: PermissionDecision, effect: "allow" | "deny", source: AuditEntry["source"], reason?: string) {
		const entry: AuditEntry = {
			at: Date.now(),
			agentId: session.agentId,
			role: session.policy.role,
			toolName: call.toolName,
			action: decision.action,
			resource: decision.resource,
			effect,
			source,
			reason,
		};
		this.audit.push(entry);
		if (this.audit.length > 1000) this.audit.shift();
		this.options.onAudit?.(entry);
	}

	async authorize(session: GateSession, call: ToolCall, signal?: AbortSignal): Promise<GateResult> {
		const decision = decide(session.policy, call);
		if (decision.effect === "allow") {
			this.record(session, call, decision, "allow", "policy");
			return { allowed: true, decision };
		}
		if (decision.effect === "deny") {
			const reason = decision.reason ?? "Denied by permission rule";
			this.record(session, call, decision, "deny", "policy", reason);
			return { allowed: false, decision, reason };
		}
		// ask
		if (!session.interactive) {
			const reason = `This permission requires an interactive primary session (${decision.reason ?? `${decision.action} ${decision.resource}`})`;
			this.record(session, call, decision, "deny", "headless", reason);
			return { allowed: false, decision, reason };
		}
		const request: ApprovalRequest = { agentId: session.agentId, role: session.policy.role, toolName: call.toolName, input: call.input, decision };
		if (this.options.reviewer && decision.genericAsk) {
			try {
				const verdict = await this.options.reviewer(request, signal);
				if (verdict.risk === "low" && verdict.scope === "within-request") {
					this.record(session, call, decision, "allow", "reviewer", verdict.reason);
					return { allowed: true, decision };
				}
			} catch {
				// Reviewer failure preserves the requirement for human approval.
			}
		}
		const approver = this.options.approver;
		if (!approver) {
			const reason = "Approval required but no interactive approver is attached";
			this.record(session, call, decision, "deny", "headless", reason);
			return { allowed: false, decision, reason };
		}
		let approved = false;
		try {
			approved = await approver(request);
		} catch {
			approved = false;
		}
		if (approved) {
			this.record(session, call, decision, "allow", "human");
			return { allowed: true, decision };
		}
		const reason = "Denied by user";
		this.record(session, call, decision, "deny", "human", reason);
		return { allowed: false, decision, reason };
	}
}
