package search

import (
	"bytes"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func parseDDG(body []byte, numResults int) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	var results []Result
	doc.Find("div.result").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		if sel.HasClass("result--ad") {
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
		return nil, err
	}

	var results []Result
	doc.Find("ul.results-standard li").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
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
