import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { stringify } from "yaml";

export interface PackAgent {
	name: string;
	model?: string;
	primary?: boolean;
	tools?: string[];
	spawns?: string[];
	permissions?: [string, string, string][];
	reasoning?: unknown;
	output?: unknown;
	prompt?: string;
}

/** Write a throwaway agent pack and return its directory. */
export function writePack(dir: string, name: string, agents: PackAgent[]): string {
	const packDir = join(dir, name);
	mkdirSync(join(packDir, "agents"), { recursive: true });
	writeFileSync(join(packDir, "pack.json"), JSON.stringify({ name, version: "0.0.1" }));
	for (const a of agents) {
		const meta: Record<string, unknown> = {
			name: a.name,
			description: `${a.name} test agent`,
			model: a.model ?? "fast",
			tools: a.tools ?? ["read", "grep", "glob"],
			permissions: a.permissions ?? [["deny", "*", "*"], ["allow", "read", "*"], ["allow", "grep", "*"], ["allow", "glob", "*"]],
		};
		if (a.primary) meta.primary = true;
		if (a.spawns) meta.spawns = a.spawns;
		if (a.reasoning) meta.reasoning = a.reasoning;
		if (a.output) meta.output = a.output;
		writeFileSync(join(packDir, "agents", `${a.name}.md`), `---\n${stringify(meta)}---\n\n${a.prompt ?? `You are ${a.name}.`}\n`);
	}
	return packDir;
}

export const ORCH_TOOLS = ["task", "dispatch", "agent_list", "agent_read", "agent_send", "agent_interrupt", "agent_cancel", "agent_resume", "wait"];
export const ORCH_RULES: [string, string, string][] = [
	["deny", "*", "*"],
	["allow", "read", "*"],
	["allow", "grep", "*"],
	["allow", "glob", "*"],
	["allow", "subagent", "*"],
	["allow", "subagent_list", "*"],
	["allow", "subagent_message", "*"],
	["allow", "subagent_stop", "*"],
	["allow", "subagent_resume", "*"],
];

/** root (primary) -> lead (can spawn) -> worker (leaf). */
export function treePack(dir: string): string {
	return writePack(dir, "tree", [
		{ name: "root", model: "primary", primary: true, tools: [...ORCH_TOOLS, "read", "grep", "glob"], spawns: ["lead", "worker"], permissions: ORCH_RULES },
		{ name: "lead", tools: [...ORCH_TOOLS, "read"], spawns: ["worker"], permissions: ORCH_RULES },
		{ name: "worker", tools: ["read", "grep", "glob", "bash", "edit", "write"], permissions: [["deny", "*", "*"], ["allow", "read", "*"], ["allow", "grep", "*"], ["allow", "glob", "*"], ["allow", "shell", "echo *"], ["allow", "shell", "sleep *"]] },
	]);
}
