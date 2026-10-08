import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { CustomEditor } from "@oh-my-pi/pi-tui/prompt/custom-editor";
import { spaciousComposerRows } from "../lib/composer";

export default function composerExtension(pi: ExtensionAPI): void {
	pi.on("session_start", (_event, ctx) => {
		if (ctx.agent.kind !== "main" || !ctx.hasUI) return;
		ctx.ui.setEditorComponent((tui, theme, keybindings) => {
			class SpaciousEditor extends CustomEditor {
				override render(width: number): string[] {
					return spaciousComposerRows(super.render(width), width, ctx.ui.theme.boxRound, this.borderColor);
				}
			}
			const editor = new SpaciousEditor(tui, theme, keybindings);
			editor.setMaxHeight(10);
			editor.setScrollbarVisible(true);
			return editor;
		});
	});
}
