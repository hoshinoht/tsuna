import { CustomEditor, type ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { spaciousComposerRows } from "../lib/composer";
import { spaciousKujiraRows, tsunaComposerShapes } from "../lib/composer-shapes";

// Runtime imports must use paths the bundled omp binary serves to extensions. It serves the
// pi-coding-agent root (which re-exports CustomEditor) but not `@oh-my-pi/pi-tui/prompt/*`;
// importing that path makes omp skip this whole extension.
export default function composerExtension(pi: ExtensionAPI): void {
	// The host installs each shape in the pi-tui registry it shares with extensions.
	for (const shape of tsunaComposerShapes) pi.registerComposerShape(shape);
	pi.on("session_start", (_event, ctx) => {
		if (ctx.agent.kind !== "main" || !ctx.hasUI) return;
		ctx.ui.setEditorComponent((tui, theme, keybindings) => {
			class SpaciousEditor extends CustomEditor {
				override render(width: number): string[] {
					const rows = super.render(width);
					const box = ctx.ui.theme.boxRound;
					switch (this.getBorderStyle()) {
						case "box": return spaciousComposerRows(rows, width, box, this.borderColor);
						case "kujira": return spaciousKujiraRows(rows, width, box, this.borderColor);
						default: return [...rows];
					}
				}
			}
			const editor = new SpaciousEditor(tui, theme, keybindings);
			editor.setMaxHeight(10);
			editor.setScrollbarVisible(true);
			return editor;
		});
	});
}
