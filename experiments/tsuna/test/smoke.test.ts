import { afterEach, expect, test } from "bun:test";
import { call, setScript, startRig, type TestRig } from "./helpers/harness.ts";

let rig: TestRig | undefined;
afterEach(async () => {
	await rig?.harness.shutdown();
	rig = undefined;
});

test("primary delegates to explore and receives its structured result", async () => {
	setScript(turn => {
		if (turn.role === "orchestrator") {
			if (turn.lastToolResults.length === 0) return call("task", { agent: "explore", task: "find the config loader" });
			return { text: `done: ${turn.lastToolResults[0]!.text}` };
		}
		if (turn.role === "explore") {
			return call("yield", { data: { answer: "src/config.ts", findings: [{ path: "src/config.ts", line: 1, why: "loader" }] } });
		}
		return { text: "?" };
	});
	rig = await startRig();
	const rt = rig.harness.runtime;
	await rt.promptPrimary("go");
	const records = rt.records();
	const child = records.find(r => r.role === "explore")!;
	expect(child.status).toBe("idle");
	expect(child.results).toHaveLength(1);
	expect(child.results[0]!.status).toBe("success");
	expect(child.results[0]!.data).toEqual({ answer: "src/config.ts", findings: [{ path: "src/config.ts", line: 1, why: "loader" }] });
	expect(child.results[0]!.delivery.state).toBe("delivered");
	const primaryView = rt.read({ kind: "human", rootId: rt.rootId }, rt.primaryId);
	expect(typeof primaryView).not.toBe("string");
	if (typeof primaryView !== "string") expect(primaryView.transcript.at(-1)!.text).toContain("tsuna-result");
});
