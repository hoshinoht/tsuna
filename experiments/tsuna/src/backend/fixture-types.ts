/**
 * Contract for deterministic fixture model scripts.
 *
 * A fixture script is executable configuration: it is loaded only from the
 * trusted Tsuna config file, never from project directories.
 */
export interface FixtureTurn {
	agentId: string;
	role: string;
	/** Full transcript Pi sends to the provider (after Tsuna context transforms). */
	messages: { role: string; content?: unknown; [key: string]: unknown }[];
	lastUserText: string | undefined;
	userTexts: string[];
	/** Tool results since the previous assistant message, in call order. */
	lastToolResults: { name: string; text: string; isError: boolean; details?: unknown }[];
	assistantCount: number;
	signal?: AbortSignal;
}

export interface FixtureReply {
	text?: string;
	thinking?: string;
	toolCalls?: { name: string; arguments: Record<string, unknown>; id?: string }[];
	/** Simulated generation time; ends early when the request is aborted. */
	delayMs?: number;
	/** Produce a provider error instead of a message. */
	error?: string;
}

export type FixtureScript = (turn: FixtureTurn) => FixtureReply | Promise<FixtureReply>;
