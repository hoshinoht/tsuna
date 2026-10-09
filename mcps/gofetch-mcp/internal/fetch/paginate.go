package fetch

import "strings"

const (
	// DefaultMaxChars matches researcher-mcp's pagination defaults.
	DefaultMaxChars = 40000
	MaxMaxChars     = 150000
	// A page may end up to this fraction of max_chars early to land on a
	// Markdown block boundary instead of mid-paragraph or mid-code-block.
	boundaryLookback = 0.25
)

type page struct {
	content    string
	total      int
	offset     int
	truncated  bool
	nextOffset int
}

// paginate slices markdown by rune offsets so multi-byte characters are
// never split mid-sequence. A truncated page ends on the last block
// boundary (blank line outside a code fence) in the final quarter of the
// window, else on a line boundary, else at max_chars exactly.
func paginate(markdown string, maxChars, offset int) page {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	if maxChars > MaxMaxChars {
		maxChars = MaxMaxChars
	}

	runes := []rune(markdown)
	total := len(runes)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}

	end := offset + maxChars
	truncated := end < total
	if truncated {
		end = cutPoint(runes, offset, end, end-int(float64(maxChars)*boundaryLookback))
	} else {
		end = total
	}

	p := page{
		content:   string(runes[offset:end]),
		total:     total,
		offset:    offset,
		truncated: truncated,
	}
	if truncated {
		p.nextOffset = end
	}
	return p
}

// cutPoint returns the best place in (minEnd, end] to end a page: the start
// of a block outside a code fence, then the start of any line, then end.
func cutPoint(runes []rune, offset, end, minEnd int) int {
	if minEnd <= offset {
		minEnd = offset + 1
	}
	block, line := -1, -1
	inFence := false
	prevBlank := false
	for lineStart := 0; lineStart < end; {
		lineEnd := lineStart
		for lineEnd < len(runes) && runes[lineEnd] != '\n' {
			lineEnd++
		}
		if lineStart >= minEnd {
			if !inFence && prevBlank {
				block = lineStart
			}
			line = lineStart
		}
		text := string(runes[lineStart:lineEnd])
		if isFence(text) {
			inFence = !inFence
		}
		prevBlank = strings.TrimSpace(text) == ""
		lineStart = lineEnd + 1
	}
	switch {
	case block > 0:
		return block
	case line > 0:
		return line
	}
	return end
}

// isFence reports whether a line opens or closes a fenced code block.
func isFence(line string) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return false
	}
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// splitBlocks splits markdown on blank lines that are not inside a fenced
// code block, so a code sample with blank lines stays one block.
func splitBlocks(markdown string) []string {
	var blocks []string
	var cur []string
	inFence := false
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(markdown, "\n") {
		if isFence(line) {
			inFence = !inFence
		}
		if !inFence && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}
