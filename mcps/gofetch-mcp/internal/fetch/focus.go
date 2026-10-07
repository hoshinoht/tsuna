package fetch

import "strings"

// focusWindow is how many blocks of context to keep around each match.
const focusWindow = 1

// focusContent narrows markdown to the blocks that mention the focus terms,
// keeping focusWindow blocks of context around each match and marking gaps
// with an ellipsis. Returns ok=false (content unchanged) when nothing
// matches, so callers fall back to the full document.
func focusContent(markdown, focus string) (string, bool) {
	terms := strings.Fields(strings.ToLower(focus))
	if len(terms) == 0 {
		return markdown, false
	}

	blocks := strings.Split(markdown, "\n\n")
	keep := make([]bool, len(blocks))
	matched := false
	for i, block := range blocks {
		lower := strings.ToLower(block)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				matched = true
				for j := max(0, i-focusWindow); j <= min(len(blocks)-1, i+focusWindow); j++ {
					keep[j] = true
				}
				break
			}
		}
	}
	if !matched {
		return markdown, false
	}

	var out []string
	inGap := false
	for i, block := range blocks {
		if keep[i] {
			out = append(out, block)
			inGap = false
		} else if !inGap {
			out = append(out, "[…]")
			inGap = true
		}
	}
	return strings.Join(out, "\n\n"), true
}
