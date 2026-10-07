package fetch

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ledongthuc/pdf"
)

// convertPDF extracts text with the pure-Go extractor. The library can panic
// on malformed PDFs, so extraction is wrapped in a recover.
func convertPDF(body []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf extraction panicked: %v", r)
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("pdf open failed: %w", err)
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("pdf text extraction failed: %w", err)
	}
	raw, err := io.ReadAll(plain)
	if err != nil {
		return "", err
	}
	return cleanExtractedText(string(raw)), nil
}

var (
	hyphenBreakPattern = regexp.MustCompile(`([a-zA-Z])-\n([a-z])`)
	manyNewlines       = regexp.MustCompile(`\n{3,}`)
)

// cleanExtractedText rejoins hyphenated line breaks, turns form feeds into
// horizontal rules, and collapses runs of blank lines.
func cleanExtractedText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = hyphenBreakPattern.ReplaceAllString(text, "$1$2")
	text = strings.ReplaceAll(text, "\f", "\n\n---\n\n")
	text = manyNewlines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}
