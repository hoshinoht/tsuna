import type { DefinitionProvenance, ReasoningLevel } from "../agents/definitions.ts";
import type { PermissionRule } from "../policy/rules.ts";

export type AgentStatus = "queued" | "running" | "idle" | "parked" | "cancelled" | "failed";

/** Everything needed to restore a child faithfully. Hashed at spawn. */
export interface AgentContract {
	role: string;
	definition: DefinitionProvenance;
	systemPrompt: string;
	modelEntry: string;
	provider: string;
	modelId: string;
	reasoning: ReasoningLevel;
	tools: string[];
	spawns: string[];
	permissions: PermissionRule[];
	harnessDenies: PermissionRule[];
	approvalMode: "source" | "auto";
	interactive: boolean;
	cwd: string;
	outputSchema?: Record<string, unknown>;
	maxDepth: number;
}

export interface DeliveryState {
	state: "pending" | "delivering" | "delivered";
	via?: "task" | "wait" | "steer" | "prompt";
	deliveryId?: string;
	at?: number;
}

export interface ResultRecord {
	resultId: string;
	runId: string;
	agentId: string;
	status: "success" | "failure";
	/** Structured payload from the terminal yield. */
	data?: unknown;
	text?: string;
	error?: string;
	interrupted?: boolean;
	acceptedAt: number;
	delivery: DeliveryState;
}

export interface IncrementalOutput {
	runId: string;
	seq: number;
	label: string;
	data: unknown;
	at: number;
}

export interface JobRef {
	runDirectory: string;
	jobId: string;
	command: string;
	toolCallId: string;
	startedAt: number;
}

export interface AgentRecord {
	version: 1;
	id: string;
	rootId: string;
	parentId: string | null;
	depth: number;
	role: string;
	contract: AgentContract;
	contractHash: string;
	sessionFile?: string;
	/** True once Pi has persisted at least one message to `sessionFile`. */
	transcriptStarted?: boolean;
	status: AgentStatus;
	/** Bumped on every session attach (spawn or revival). */
	generation: number;
	runSeq: number;
	currentRunId?: string;
	/** Set when a run was in flight when Tsuna stopped (outcome unknown, not replayed). */
	interruptedRunId?: string;
	activity?: string;
	task?: string;
	results: ResultRecord[];
	outputs: IncrementalOutput[];
	jobs: JobRef[];
	/** Manual tool-result compressions (toolCallId -> summary); transcript untouched. */
	compressions: Record<string, string>;
	/** Persisted image-budget decisions (stable keys keep the prompt prefix identical). */
	imagePrune?: Record<string, "budget" | "duplicate">;
	failure?: string;
	createdAt: number;
	updatedAt: number;
}

export type Actor = { kind: "human"; rootId: string } | { kind: "agent"; agentId: string };

export type SendOutcome = "delivered" | "queued" | "revived" | "rejected";

export interface RuntimeEvent {
	type:
		| "agent_spawned"
		| "agent_status"
		| "agent_activity"
		| "agent_output"
		| "agent_result"
		| "message"
		| "permission"
		| "warning"
		| "assistant_text"
		| "tool";
	agentId?: string;
	[key: string]: unknown;
}
