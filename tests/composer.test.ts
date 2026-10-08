import { expect, test } from "bun:test";
import type { ExtensionAPI, ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { visibleWidth } from "@oh-my-pi/pi-tui";
import { CustomEditor } from "@oh-my-pi/pi-tui/prompt/custom-editor";
import { initThemeSync, theme, getEditorTheme } from "@oh-my-pi/pi-tui/theme";
import composer from "../extensions/composer";
import { spaciousComposerRows } from "../lib/composer";

test("the real editor has a complete frame, four empty typing rows, and an unchanged draft/cursor", () => {
	initThemeSync("unicode", false, "dark", "light");
	let factory: ((...args: unknown[]) => CustomEditor) | undefined;
	let start: ((event: unknown, ctx: ExtensionContext) => void) | undefined;
	composer({ on: (_name: string, handler: unknown) => { start = handler as typeof start; } } as unknown as ExtensionAPI);
	const skin = getEditorTheme();
	try {
		start!(undefined, { agent: { kind: "main" }, hasUI: true, ui: { theme, setEditorComponent: (value: typeof factory) => { factory = value; } } } as never);
		const editor = factory!({ requestRender: () => {} }, skin);
		editor.setBorderStyle("box");
		for (const width of [20, 40, 80, 200]) {
			const rows = editor.render(width).map(line => Bun.stripANSI(line));
			expect(rows).toHaveLength(6);
			expect(rows[0].startsWith("╭")).toBe(true);
			expect(rows.at(-1)).toBe(`╰${"─".repeat(width - 2)}╯`);
			for (const row of rows) expect(visibleWidth(row)).toBe(width);
		}
		expect(editor.getText()).toBe("");
		editor.setText("one\ntwo\nthree\nfour\nfive");
		const cursor = editor.getCursor();
		expect(editor.render(80)).toHaveLength(7);
		expect(editor.getText()).toBe("one\ntwo\nthree\nfour\nfive");
		expect(editor.getCursor()).toEqual(cursor);
	} finally { /* The editor uses the host's ordinary box registry. */ }
});

test("other composer shapes and autocomplete rows are left intact", () => {
	const box = { topLeft: "╭", topRight: "╮", bottomLeft: "╰", bottomRight: "╯", horizontal: "─", vertical: "│" };
	const other = ["ordinary band", "draft"];
	expect(spaciousComposerRows(other, 20, box, text => text)).toEqual(other);
	const rows = ["╭──────────────────╮", "│draft             │", "╰──────────────────╯", "autocomplete"];
	const padded = spaciousComposerRows(rows, 20, box, text => text);
	expect(padded).toHaveLength(7);
	expect(padded.at(-1)).toBe("autocomplete");
});
