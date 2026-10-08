import type { ComposerBox } from "@oh-my-pi/pi-tui/components/composer/types";

export function spaciousComposerRows(rows: readonly string[], width: number, box: ComposerBox, color: (text: string) => string, minimumLines = 4): string[] {
	const bottomRule = box.bottomLeft + box.horizontal.repeat(Math.max(0, width - 2)) + box.bottomRight;
	const bottomIndex = rows.findIndex(line => Bun.stripANSI(line).startsWith(box.bottomLeft));
	if (bottomIndex < 0) return [...rows]; // Respect a different shape selected by the user.
	const result = [...rows];
	let frameEnd = bottomIndex;
	if (Bun.stripANSI(result[bottomIndex]) !== bottomRule) {
		// OMP merges the last text row into its bottom border. Keep its text/caret cells,
		// replace only the chrome, and put a complete bottom rule on a separate row.
		let row = result[bottomIndex].replace(box.bottomLeft + box.horizontal, box.vertical + " ");
		const closing = row.lastIndexOf(box.horizontal + box.bottomRight);
		if (closing >= 0) row = row.slice(0, closing) + " " + box.vertical + row.slice(closing + box.horizontal.length + box.bottomRight.length);
		else { const corner = row.lastIndexOf(box.bottomRight); if (corner >= 0) row = row.slice(0, corner) + box.vertical + row.slice(corner + box.bottomRight.length); }
		result[bottomIndex] = row;
		result.splice(++frameEnd, 0, color(bottomRule));
	}
	const blanks = Math.max(0, minimumLines - (frameEnd - 1));
	const blank = color(box.vertical + " ".repeat(Math.max(0, width - 2)) + box.vertical);
	return [...result.slice(0, frameEnd), ...Array<string>(blanks).fill(blank), ...result.slice(frameEnd)];
}
