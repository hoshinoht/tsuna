/**
 * Deterministic fixture "model" for the offline demonstration. It reads only
 * the transcript Pi sends (no hidden state), so revived and restarted agents
 * behave exactly as their stored history implies.
 */
import type { FixtureReply, FixtureTurn } from "../../src/backend/fixture-types.ts";

type Msg = FixtureTurn["messages"][number];

function text(m: Msg): string {
	if (typeof m.content === "string") return m.content;
	if (!Array.isArray(m.content)) return "";
	return m.content.map(b => (b && typeof b === "object" && (b as { type?: string }).type === "text" ? String((b as { text?: string }).text) : "")).join("");
}

function isAssignment(m: Msg) {
	const t = text(m);
	// Harness notices (reminders, steered child results) and steering are not new assignments.
	return m.role === "user" && !t.startsWith("[tsuna]") && !t.startsWith("[steering");
}

/** Messages of the current run: everything after the last assignment. */
function run(turn: FixtureTurn) {
	let start = -1;
	turn.messages.forEach((m, i) => {
		if (isAssignment(m)) start = i;
	});
	const assignment = start >= 0 ? text(turn.messages[start]!) : "";
	const after = turn.messages.slice(start + 1);
	const results = after.filter(m => m.role === "toolResult").map(m => ({ name: String(m.toolName), text: text(m), isError: m.isError === true }));
	const steering = after.filter(m => m.role === "user" && text(m).startsWith("[steering")).map(text);
	return { assignment, results, steering };
}

const call = (name: string, args: Record<string, unknown>, extra: Partial<FixtureReply> = {}): FixtureReply => ({ ...extra, toolCalls: [{ name, arguments: args }] });

function orchestrator(turn: FixtureTurn): FixtureReply {
	const { assignment, results } = run(turn);
	if (assignment.includes("Publish")) {
		// `git push` matches the orchestrator's specific `ask` rule: interactive sessions prompt, headless ones deny.
		if (results.length === 0) return call("bash", { command: "git push origin HEAD", timeout_seconds: 30 });
		return { text: `Publish attempt: ${results[0]!.text.split("\n")[0]}` };
	}
	if (!assignment.includes("Survey")) return { text: `Acknowledged: ${assignment.split("\n").at(-1)?.slice(0, 120)}` };
	if (!results.some(r => r.name === "mcp__context7__echo")) return call("mcp__context7__echo", { text: "pi-coding-agent session API" }, { text: "Checking library notes first." });
	if (!results.some(r => r.name === "dispatch")) {
		return call("dispatch", {
			background: true,
			tasks: [
				{ agent: "explore", task: "Map the entry points of this repository: list source files and say which is the main entry." },
				{ agent: "tester", task: "Check the working tree status with `git status --short` and report it." },
			],
		}, { text: "Dispatching an explorer and a tester in the background." });
	}
	const seen = results.filter(r => r.name === "wait").reduce((n, r) => n + (r.text.match(/\[tsuna-result /g)?.length ?? 0), 0);
	const steered = turn.messages.filter(m => m.role === "user" && text(m).includes("[tsuna-result ")).length;
	if (seen + steered < 2 && results.filter(r => r.name === "wait").length < 6) return call("wait", { timeout_seconds: 20 });
	return { text: `Survey complete: received ${seen + steered} specialist results.` };
}

function explore(turn: FixtureTurn): FixtureReply {
	const { assignment, results, steering } = run(turn);
	const lines = (name: string) => results.filter(r => r.name === name && !r.isError).flatMap(r => r.text.split("\n")).filter(l => l && !l.startsWith("No "));
	if (assignment.includes("Map the entry points")) {
		if (results.length === 0) return call("glob", { pattern: "**/*.ts" }, { delayMs: 1500, text: "Scanning source files." });
		if (steering.some(s => /docs/i.test(s)) && !results.some(r => r.name === "glob" && r.text.includes(".md"))) return call("glob", { pattern: "docs/**/*.md" }, { text: "Steered: including the docs folder." });
		const files = lines("glob");
		return call("yield", {
			summary: "entry points mapped",
			data: {
				answer: files.includes("src/main.ts") ? "src/main.ts is the entry point" : "no obvious entry point",
				findings: files.map(path => ({ path, why: path.endsWith(".md") ? "documentation (added after steering)" : path.startsWith("test/") ? "test" : "source" })),
			},
		});
	}
	if (assignment.includes("test files")) {
		if (results.length === 0) return call("glob", { pattern: "test/**/*.ts" });
		return call("yield", { data: { answer: "test files listed", findings: lines("glob").map(path => ({ path, why: "test file" })) } });
	}
	if (assignment.includes("Re-check")) {
		if (results.length === 0) {
			return {
				text: "Re-checking after the restart; probing my limits too.",
				toolCalls: [
					{ name: "grep", arguments: { pattern: "export function", path: "src" } },
					{ name: "read", arguments: { path: "/etc/hostname" } },
					{ name: "bash", arguments: { command: "ls" } },
				],
			};
		}
		const denied = results.filter(r => r.isError).map(r => `${r.name}: ${r.text.split("\n")[0]}`);
		const hits = lines("grep");
		return call("yield", {
			data: {
				answer: `revived with original permissions; ${denied.length} operations refused`,
				findings: [...hits.map(h => ({ path: h.split(":")[0]!, line: Number(h.split(":")[1]), why: "exported function" })), ...denied.map(d => ({ path: "(denied)", why: d }))],
			},
		});
	}
	return call("yield", { data: { answer: "unrecognised assignment", findings: [] } });
}

function tester(turn: FixtureTurn): FixtureReply {
	const { results } = run(turn);
	if (results.length === 0) return call("bash", { command: "git status --short", timeout_seconds: 30 });
	const r = results[0]!;
	const exit = Number(/exit_code: (\d+)/.exec(r.text)?.[1] ?? NaN);
	return call("yield", {
		data: { status: exit === 0 ? "PASS" : "FAIL", checks: [{ command: "git status --short", exit: Number.isNaN(exit) ? null : exit, summary: r.text.split("\n").slice(0, 6).join(" | ") }] },
	});
}

export default function demoModel(turn: FixtureTurn): FixtureReply {
	switch (turn.role) {
		case "orchestrator":
			return orchestrator(turn);
		case "explore":
			return explore(turn);
		case "tester":
			return tester(turn);
		default:
			return call("yield", { status: "failure", error: `no demo behaviour for ${turn.role}` });
	}
}
