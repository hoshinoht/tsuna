/** Fixture model entry point for tests: delegates to the script installed by the test. */
import type { FixtureReply, FixtureTurn } from "../../src/backend/fixture-types.ts";

export default async function script(turn: FixtureTurn): Promise<FixtureReply> {
	const impl = (globalThis as { __TSUNA_SCRIPT__?: (t: FixtureTurn) => FixtureReply | Promise<FixtureReply> }).__TSUNA_SCRIPT__;
	if (!impl) return { error: "no test script installed" };
	return impl(turn);
}
