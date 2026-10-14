import { expect, test } from "bun:test";
import type { ExtensionAPI, ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { visibleWidth } from "@oh-my-pi/pi-tui";
import { getComposerStyle, registerComposerStyle } from "@oh-my-pi/pi-tui/components/composer/registry";
import type { ComposerShapeDefinition } from "@oh-my-pi/pi-tui/overlays/composer-shape-registry";
import { CustomEditor } from "@oh-my-pi/pi-tui/prompt/custom-editor";
import { initThemeSync, theme, getEditorTheme } from "@oh-my-pi/pi-tui/theme";
import composer from "../extensions/composer";
import { spaciousComposerRows } from "../lib/composer";
import { spaciousKujiraRows } from "../lib/composer-shapes";

// Like the real host, the fake installs each registered shape in the pi-tui registry it shares with
// extensions; registering it anywhere else as well makes the host throw "already registered".
function installLikeHost(shape: ComposerShapeDefinition): void {
	if (getComposerStyle(shape.style.id).id !== shape.style.id) registerComposerStyle(shape.style);
}

function tsunaEditor(shapes: ComposerShapeDefinition[] = []): CustomEditor {
	initThemeSync("unicode", false, "dark", "light");
	let factory: ((...args: unknown[]) => CustomEditor) | undefined;
	let start: ((event: unknown, ctx: ExtensionContext) => void) | undefined;
	composer({
		on: (_name: string, handler: unknown) => { start = handler as typeof start; },
		registerComposerShape: (shape: ComposerShapeDefinition) => { shapes.push(shape); installLikeHost(shape); },
	} as unknown as ExtensionAPI);
	start!(undefined, { agent: { kind: "main" }, hasUI: true, ui: { theme, setEditorComponent: (value: typeof factory) => { factory = value; } } } as never);
	return factory!({ requestRender: () => {} }, getEditorTheme());
}

test("composer runtime imports use only the pi-coding-agent root that bundled omp serves", async () => {
	// The bundled omp binary serves a fixed set of import paths to extensions; it does not serve
	// `@oh-my-pi/pi-tui/prompt/*`, and one unserved runtime import makes omp skip the whole extension.
	for (const file of ["../extensions/composer.ts", "../lib/composer.ts", "../lib/composer-shapes.ts"]) {
		const source = await Bun.file(new URL(file, import.meta.url)).text();
		const runtime = [...source.matchAll(/^import (?!type )[^;]*from "(@oh-my-pi\/[^"]+)"/gm)].map(match => match[1]);
		for (const specifier of runtime) expect(`${file}: ${specifier}`).toBe(`${file}: @oh-my-pi/pi-coding-agent`);
	}
});

test("the extension registers Kujira and Siesta only through the host API", () => {
	const shapes: ComposerShapeDefinition[] = [];
	const registered: string[] = [];
	initThemeSync("unicode", false, "dark", "light");
	// A host that throws on a second registration, as OMP does, must not be pre-empted by the extension.
	composer({
		on: () => {},
		registerComposerShape: (shape: ComposerShapeDefinition) => { registered.push(shape.style.id); shapes.push(shape); },
	} as unknown as ExtensionAPI);
	expect(shapes.map(shape => [shape.style.id, shape.label])).toEqual([["kujira", "Kujira"], ["siesta", "Siesta"]]);
	expect(registered).toEqual(["kujira", "siesta"]);
});

test("Kujira keeps a closed frame, four typing rows, the whale rule, and an unchanged draft/cursor", () => {
	const editor = tsunaEditor();
	editor.setBorderStyle("kujira");
	for (const width of [20, 40, 80, 200]) {
		const rows = editor.render(width).map(line => Bun.stripANSI(line));
		expect(rows).toHaveLength(6);
		expect(rows[0].startsWith("╭")).toBe(true);
		expect(rows.at(-1)!.startsWith("╰─  ∿∿∿ ─")).toBe(true);
		expect(rows.at(-1)!.endsWith("╯")).toBe(true);
		for (const row of rows) expect(visibleWidth(row)).toBe(width);
	}
	editor.setText("one\ntwo\nthree\nfour\nfive");
	const cursor = editor.getCursor();
	expect(editor.render(80)).toHaveLength(7);
	expect(editor.getText()).toBe("one\ntwo\nthree\nfour\nfive");
	expect(editor.getCursor()).toEqual(cursor);
});

test("Siesta draws a dotted rule over a › prompt without side borders", () => {
	const editor = tsunaEditor();
	editor.setBorderStyle("siesta");
	editor.setText("tidy the readme");
	for (const width of [20, 40, 80]) {
		const rows = editor.render(width).map(line => Bun.stripANSI(line));
		expect(rows).toHaveLength(2);
		expect(rows[0]).toBe("╌".repeat(width));
		expect(rows[1].startsWith(" › tidy the readme")).toBe(true);
		for (const row of rows) expect(visibleWidth(row)).toBe(width);
	}
	expect(editor.getText()).toBe("tidy the readme");
});

test("the real editor has a complete frame, four empty typing rows, and an unchanged draft/cursor", () => {
	const editor = tsunaEditor();
	try {
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
	// Kujira pads above its whale rule only, so the box patch never rewrites it.
	const whale = ["╭──────────────────╮", "│draft             │", "╰─  ∿∿∿ ────────╯", "autocomplete"];
	const kujira = spaciousKujiraRows(whale, 20, box, text => text);
	expect(kujira).toHaveLength(7);
	expect(kujira[5]).toBe(whale[2]);
	expect(kujira.at(-1)).toBe("autocomplete");
	expect(spaciousKujiraRows(other, 20, box, text => text)).toEqual(other);
});
