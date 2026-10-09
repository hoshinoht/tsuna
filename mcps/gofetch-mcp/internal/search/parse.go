package search

import (
	"bytes"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Markers of a bot check served in place of results (lower-cased).
var blockMarkers = []string{
	"anomaly-modal", "challenge-form", "bots use duckduckgo too", "unusual traffic",
	"automated queries", "captcha", "are you a robot",
}

// classifyEmptyPage decides what a page without recognizable results is:
// a genuine "no results" answer, a block page, or a layout gofetch no
// longer understands.
func classifyEmptyPage(doc *goquery.Document, body []byte, noResultsSelector string, noResultsPhrases []string) error {
	if noResultsSelector != "" && doc.Find(noResultsSelector).Length() > 0 {
		return nil
	}
	text := strings.ToLower(doc.Find("body").Text())
	for _, p := range noResultsPhrases {
		if strings.Contains(text, p) {
			return nil
		}
	}
	lower := bytes.ToLower(body)
	for _, m := range blockMarkers {
		if bytes.Contains(lower, []byte(m)) {
			return &providerError{outcome: OutcomeBlocked, detail: "bot check page instead of results"}
		}
	}
	return &providerError{outcome: OutcomeParseError, detail: "unrecognized result page layout"}
}

func parseDDG(body []byte, numResults int) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, &providerError{outcome: OutcomeParseError, detail: err.Error()}
	}

	items := doc.Find("div.result")
	if items.Length() == 0 {
		if err := classifyEmptyPage(doc, body, ".no-results", []string{"no results."}); err != nil {
			return nil, err
		}
		return []Result{}, nil
	}

	results := []Result{}
	items.EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		if sel.HasClass("result--ad") || sel.HasClass("result--no-result") {
			return true
		}
		link := sel.Find("a.result__a").First()
		href, ok := link.Attr("href")
		if !ok {
			return true
		}
		title := strings.TrimSpace(link.Text())
		if title == "" {
			return true
		}
		results = append(results, Result{
			Title:   title,
			URL:     decodeDDGHref(href),
			Snippet: strings.TrimSpace(sel.Find(".result__snippet").First().Text()),
		})
		return len(results) < numResults
	})
	return results, nil
}

func parseMojeek(body []byte, numResults int) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, &providerError{outcome: OutcomeParseError, detail: err.Error()}
	}

	list := doc.Find("ul.results-standard")
	if list.Length() == 0 {
		if err := classifyEmptyPage(doc, body, "", []string{"no pages found"}); err != nil {
			return nil, err
		}
		return []Result{}, nil
	}

	results := []Result{}
	list.Find("li").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		link := sel.Find("h2 a").First()
		href, ok := link.Attr("href")
		if !ok {
			return true
		}
		title := strings.TrimSpace(link.Text())
		if title == "" {
			return true
		}
		results = append(results, Result{
			Title:   title,
			URL:     href,
			Snippet: strings.TrimSpace(sel.Find("p.s").First().Text()),
		})
		return len(results) < numResults
	})
	return results, nil
}
