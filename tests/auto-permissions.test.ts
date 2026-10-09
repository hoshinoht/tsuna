import { expect, test } from "bun:test";
import { approvalHash, ReviewCircuit, routeAutoPermission } from "../lib/auto-permissions";
import { asToolCall, decidePermission, type PermissionPolicies } from "../lib/permissions";

const policies: PermissionPolicies = { worker: [
	{ action: "*", resource: "*", effect: "ask" },
	{ action: "read", resource: "*", effect: "allow" },
	{ action: "edit", resource: "*", effect: "allow" },
	{ action: "shell", resource: "git push*", effect: "ask" },
	{ action: "edit", resource: "*/secret", effect: "ask" },
	{ action: "edit", resource: "*/forbidden", effect: "deny" },
] };
const cwd = "/private/tmp/tsuna-route";
function route(tool: string, input: Record<string, unknown>) {
	const call = asToolCall(tool, input);
	return routeAutoPermission(call, decidePermission(policies, "worker", call, { cwd }), cwd);
}

test("routine local work bypasses review, executable and external operations do not", () => {
	expect(route("read", { path: "src/main.ts" })).toBe("allow");
	expect(route("read", { path: "AGENTS.md" })).toBe("allow");
	expect(route("read", { path: ".env.example" })).toBe("allow");
	expect(route("write", { path: "src/main.ts", content: "hello" })).toBe("allow");
	expect(route("write", { path: "/private/tmp/elsewhere/main.ts", content: "hello" })).toBe("review");
	for (const command of ["git status", "bun test", "python -c 'print(1)'", "echo ok; curl https://example.test", "rm /tmp/unknown"]) {
		expect(route("bash", { command })).toBe("review");
	}
	expect(route("eval", { language: "js", code: "print(1)" })).toBe("review");
	expect(route("task", { agent: "explore", task: "Investigate" })).toBe("review");
});

test("denials, protected paths, and asks in earlier batch paths and shell segments survive", () => {
	expect(route("write", { path: "forbidden", content: "x" })).toBe("deny");
	expect(route("write", { path: "config/auto-review.json", content: "{}" })).toBe("ask-human");
	expect(route("edit", { path: "src/extract/AGENTS.md", input: "factual documentation update" })).toBe("review");
	expect(route("read", { path: ".env" })).toBe("ask-human");
	expect(route("edit", { paths: ["secret", "ordinary"] })).toBe("ask-human");
	expect(route("bash", { command: "git push origin main && echo done" })).toBe("ask-human");
	expect(route("bash", { command: "echo ready; git push origin main | cat" })).toBe("ask-human");
	expect(route("apply_patch", { input: "*** Begin Patch\n*** Delete File: src/main.ts\n*** End Patch" })).toBe("review");
	expect(route("edit", { path: "src/main.ts", edits: [{ op: "delete" }] })).toBe("review");
	expect(route("checkpoint", {})).toBe("deny");
});

test("exact action identity includes working directory, role and every argument", () => {
	const action = { cwd, role: "worker", input: { command: "git status", timeout: 10 } };
	expect(approvalHash(action)).toBe(approvalHash({ input: { timeout: 10, command: "git status" }, role: "worker", cwd }));
	expect(approvalHash(action)).not.toBe(approvalHash({ ...action, cwd: "/elsewhere" }));
	expect(approvalHash(action)).not.toBe(approvalHash({ ...action, input: { ...action.input, command: "git push" } }));
});

test("review circuit limits denials and outages without treating outages as unsafe verdicts", () => {
	const consecutive = new ReviewCircuit();
	expect(consecutive.record("deny")).toBe(false);
	expect(consecutive.record("deny")).toBe(false);
	expect(consecutive.record("deny")).toBe(true);
	const rolling = new ReviewCircuit();
	for (let i = 0; i < 9; i++) { expect(rolling.record("deny")).toBe(false); expect(rolling.record("allow")).toBe(false); }
	expect(rolling.record("deny")).toBe(true);
	const outage = new ReviewCircuit();
	expect(outage.record("unavailable")).toBe(false);
	expect(outage.record("unavailable")).toBe(false);
	expect(outage.record("unavailable")).toBe(false);
});
