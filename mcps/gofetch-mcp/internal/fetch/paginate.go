package fetch

const (
	// DefaultMaxChars matches researcher-mcp's pagination defaults.
	DefaultMaxChars = 40000
	MaxMaxChars     = 150000
)

type page struct {
	content    string
	total      int
	offset     int
	truncated  bool
	nextOffset int
}

// paginate slices markdown by rune offsets so multi-byte characters are
// never split mid-sequence.
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
	if !truncated {
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
