package fetch

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// focusWindow is how many blocks of context to keep around each match.
	focusWindow = 1
	// sectionBlocks is how many blocks after a matching heading are kept as
	// that section's body (stopping at the next heading of the same level).
	sectionBlocks = 6
	focusGap      = "[…]"
)

type focusTerm struct {
	text   string // lower-cased, whitespace-collapsed
	weight int
	exact  bool // quoted phrase: whole-word match, no inflection allowed
}

type focusResult struct {
	text    string
	matched bool
	terms   []string // focus terms that matched, in query order
}

var quotedPhrase = regexp.MustCompile(`"([^"]+)"|“([^”]+)”`)

var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"do": true, "does": true, "for": true, "from": true, "how": true, "i": true, "in": true, "is": true,
	"it": true, "of": true, "on": true, "or": true, "the": true, "to": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "why": true, "with": true,
}

// parseFocus turns a focus string into weighted terms: quoted phrases
// (weight 3), individual words minus stopwords (weight 1), and the unquoted
// words as an implicit phrase (weight 2) when there are several.
func parseFocus(focus string) []focusTerm {
	var terms []focusTerm
	seen := map[string]bool{}
	add := func(text string, weight int, exact bool) {
		text = collapseSpace(strings.ToLower(text))
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		terms = append(terms, focusTerm{text, weight, exact})
	}

	for _, m := range quotedPhrase.FindAllStringSubmatch(focus, -1) {
		add(m[1]+m[2], 3, true)
	}
	rest := quotedPhrase.ReplaceAllString(focus, " ")

	var words, content []string
	for _, w := range strings.Fields(strings.ToLower(rest)) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
		if w == "" {
			continue
		}
		words = append(words, w)
		if !stopwords[w] {
			content = append(content, w)
		}
	}
	if len(content) == 0 {
		content = words // a query of only stopwords still means something
	}
	if len(content) >= 2 && len(content) <= 6 {
		add(strings.Join(content, " "), 2, false)
	}
	for _, w := range content {
		add(w, 1, false)
	}
	return terms
}

// focusDocument narrows markdown to the blocks that best match focus. A
// block's score is the summed weight of the distinct terms it contains;
// blocks scoring at least half the best score are kept, with focusWindow
// blocks of context, the heading of the section they sit in, and (for a
// matching heading) the start of its section. Gaps are marked "[…]".
// matched is false, and markdown is returned unchanged, when nothing matches.
func focusDocument(markdown, focus string) focusResult {
	terms := parseFocus(focus)
	if len(terms) == 0 {
		return focusResult{text: markdown}
	}
	blocks := splitBlocks(markdown)

	scores := make([]int, len(blocks))
	hit := make([]bool, len(terms))
	best := 0
	for i, block := range blocks {
		norm := collapseSpace(strings.ToLower(block))
		for t, term := range terms {
			if containsTerm(norm, term.text, term.exact) {
				scores[i] += term.weight
				hit[t] = true
			}
		}
		best = max(best, scores[i])
	}
	if best == 0 {
		return focusResult{text: markdown}
	}
	threshold := max(1, (best+1)/2)

	// section[i] is the index of the heading governing block i, or -1.
	section := make([]int, len(blocks))
	current := -1
	for i, block := range blocks {
		if headingLevel(block) > 0 {
			current = i
		}
		section[i] = current
	}

	keep := make([]bool, len(blocks))
	for i := range blocks {
		if scores[i] < threshold {
			continue
		}
		from := max(0, i-focusWindow)
		if headingLevel(blocks[i]) > 0 {
			from = i // a matching heading starts its own context
		}
		for j := from; j <= min(len(blocks)-1, i+focusWindow); j++ {
			keep[j] = true
		}
		if h := section[i]; h >= 0 {
			keep[h] = true
		}
		if level := headingLevel(blocks[i]); level > 0 {
			for j := i + 1; j < len(blocks) && j <= i+sectionBlocks; j++ {
				if l := headingLevel(blocks[j]); l > 0 && l <= level {
					break
				}
				keep[j] = true
			}
		}
	}

	var out []string
	inGap := false
	for i, block := range blocks {
		if keep[i] {
			out = append(out, block)
			inGap = false
		} else if !inGap {
			out = append(out, focusGap)
			inGap = true
		}
	}

	var matched []string
	for t, term := range terms {
		if hit[t] {
			matched = append(matched, term.text)
		}
	}
	return focusResult{text: strings.Join(out, "\n\n"), matched: true, terms: matched}
}

// containsTerm matches term in s (both lower-cased). Terms that start with
// a Latin letter or digit must start at a word boundary and may only be
// followed by a common inflection, so "go" does not match "good" but "cat"
// matches "cats" and "limit" matches "limiting"; with exact (quoted
// phrases) the term must also end at a word boundary. Other scripts (CJK,
// ...) match as plain substrings.
func containsTerm(s, term string, exact bool) bool {
	first, _ := utf8.DecodeRuneInString(term)
	if !(unicode.Is(unicode.Latin, first) || unicode.IsDigit(first)) {
		return strings.Contains(s, term)
	}
	for from := 0; ; {
		i := strings.Index(s[from:], term)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(term)
		prev, _ := utf8.DecodeLastRuneInString(s[:i])
		suffix := wordAt(s[end:])
		if (i == 0 || !isWordRune(prev)) && (suffix == "" || (!exact && inflectionSuffix[suffix])) {
			return true
		}
		from = end
	}
}

var inflectionSuffix = map[string]bool{"": true, "s": true, "es": true, "d": true, "ed": true, "ing": true, "er": true, "ers": true}

// wordAt returns the run of word runes at the start of s.
func wordAt(s string) string {
	for i, r := range s {
		if !isWordRune(r) {
			return s[:i]
		}
	}
	return s
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// headingLevel returns 1-6 for an ATX heading block, else 0.
func headingLevel(block string) int {
	n := 0
	for n < len(block) && n < 7 && block[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(block) || block[n] != ' ' {
		return 0
	}
	return n
}
