import type { ComposerShapeDefinition } from "@oh-my-pi/pi-tui/overlays/composer-shape-registry";
import type { ComposerBox, ComposerChromeContext, ComposerRowContext, ComposerStyle } from "@oh-my-pi/pi-tui/components/composer/types";

// Shapes receive every colour through the chrome context, so they never touch the host's theme singleton.
const WHALE = "";
const WAKE = " ∿∿∿ ";

function cells(count: number): string {
	return " ".repeat(Math.max(0, count));
}

/** Kujira: rounded box with the status in the top border and the whale swimming along a closed bottom rule. */
export const kujiraStyle: ComposerStyle = {
	id: "kujira",
	sideBorders: true,
	verticalChrome: 2,
	statusAttachment: "top-border",
	bottomBar: "none",
	bottomBarGap: false,
	defaultPromptGutter: undefined,
	defaultPaddingX: themePaddingX => Math.max(0, themePaddingX ?? 2),
	sideChromeWidth: paddingX => paddingX + 1,

	renderTop(ctx: ComposerChromeContext): string {
		const { box, paddingX, width, borderColor, topBorder } = ctx;
		const fill = Math.max(0, width - (paddingX + 1) * 2);
		const left = borderColor(box.topLeft + box.horizontal.repeat(paddingX));
		const right = borderColor(box.horizontal.repeat(paddingX) + box.topRight);
		if (!topBorder || topBorder.width > fill) return left + borderColor(box.horizontal.repeat(fill)) + right;
		return left + topBorder.content + borderColor(box.horizontal.repeat(fill - topBorder.width)) + right;
	},

	renderRow(ctx: ComposerRowContext): string[] {
		const { box, paddingX, borderColor, text, pad } = ctx;
		const left = borderColor(box.vertical + cells(paddingX));
		// Leave the cells after an end-of-input cursor empty so IME preedit cannot push the frame.
		if (ctx.isLastRow && ctx.imeSafeCursorTail) return [left + text];
		const rightCells = Math.max(1, paddingX + 1 - ctx.cursorOverflow);
		const glyph = ctx.scrollbarThumb ? "█" : box.vertical;
		return [left + text + pad + borderColor(cells(rightCells - 1) + glyph)];
	},

	renderBottom(ctx: ComposerChromeContext): string {
		const { box, width, borderColor, accentColor } = ctx;
		const lead = `${box.bottomLeft}${box.horizontal} `;
		const used = lead.length + 1 + WAKE.length + 1;
		if (width < used + 1) return borderColor(box.bottomLeft + box.horizontal.repeat(Math.max(0, width - 2)) + box.bottomRight);
		return borderColor(lead) + accentColor(WHALE) + borderColor(WAKE + box.horizontal.repeat(width - used) + box.bottomRight);
	},
};

/** Siesta: no frame; a dotted rule carries the right status chip, a quiet › prompt, the left status below. */
export const siestaStyle: ComposerStyle = {
	id: "siesta",
	sideBorders: false,
	verticalChrome: 1,
	statusAttachment: "top-rule-chip",
	bottomBar: "left",
	bottomBarGap: true,
	defaultPromptGutter: "› ",
	defaultPaddingX: () => 1,
	sideChromeWidth: paddingX => paddingX,

	renderTop(ctx: ComposerChromeContext): string {
		const { width, borderColor, topBorder } = ctx;
		if (!topBorder || topBorder.width === 0 || topBorder.width > width - 4) return borderColor("╌".repeat(width));
		return borderColor("╌".repeat(width - topBorder.width - 2)) + " " + topBorder.content + " ";
	},

	renderRow: (ctx: ComposerRowContext) => [cells(ctx.paddingX) + ctx.gutter + ctx.text + ctx.pad + cells(ctx.paddingX)],

	renderBottom: () => undefined,
};

export const tsunaComposerShapes: readonly ComposerShapeDefinition[] = [
	{ label: "Kujira", description: "Rounded box; the whale swims along the closed bottom rule", style: kujiraStyle },
	{ label: "Siesta", description: "Dotted status rule, quiet › prompt, left status below", style: siestaStyle },
];

/** Pads Kujira to `minimumLines` typing rows by inserting empty framed rows above its whale rule. */
export function spaciousKujiraRows(rows: readonly string[], width: number, box: ComposerBox, color: (text: string) => string, minimumLines = 4): string[] {
	const bottomIndex = rows.findIndex(line => Bun.stripANSI(line).startsWith(`${box.bottomLeft}${box.horizontal} ${WHALE}`));
	if (bottomIndex < 1) return [...rows];
	const blanks = Math.max(0, minimumLines - (bottomIndex - 1));
	const blank = color(box.vertical + cells(width - 2) + box.vertical);
	return [...rows.slice(0, bottomIndex), ...Array<string>(blanks).fill(blank), ...rows.slice(bottomIndex)];
}
