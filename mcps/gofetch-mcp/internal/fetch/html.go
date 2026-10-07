package fetch

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"
)

// mainContentSelectors are tried in order; the first non-empty match is
// treated as the article body.
var mainContentSelectors = []string{"article", "main", "[role=main]", "#content", ".content", "body"}

// strippedSelectors is page chrome removed before conversion.
const strippedSelectors = "script, style, nav, header, footer, aside, form, noscript, iframe, svg, [role=navigation], [role=banner], [aria-hidden=true]"

func convertHTML(body []byte, pageURL string) (markdown, title string, err error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	title = strings.TrimSpace(doc.Find("title").First().Text())

	var sel *goquery.Selection
	for _, selector := range mainContentSelectors {
		if s := doc.Find(selector).First(); s.Length() > 0 {
			sel = s
			break
		}
	}
	if sel == nil {
		sel = doc.Selection
	}
	sel.Find(strippedSelectors).Remove()
	resolveLinks(sel, pageURL)

	fragment, err := goquery.OuterHtml(sel)
	if err != nil {
		return "", "", err
	}

	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(),
	))

	// WithDomain is a backstop for hrefs resolveLinks could not parse.
	opts := []converter.ConvertOptionFunc{}
	if domain := domainOf(pageURL); domain != "" {
		opts = append(opts, converter.WithDomain(domain))
	}

	md, err := conv.ConvertString(fragment, opts...)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(md), title, nil
}

// resolveLinks rewrites relative a[href] and img[src] values against the
// page's final URL, so fragment links keep their page path
// ("#install" -> "https://host/docs/page/#install") and "../"-style paths
// resolve correctly instead of being joined to the bare domain.
func resolveLinks(sel *goquery.Selection, pageURL string) {
	pageBase, err := url.Parse(pageURL)
	if err != nil || pageBase.Scheme == "" || pageBase.Host == "" {
		return
	}
	rewrite := func(s *goquery.Selection, attr string) {
		raw, ok := s.Attr(attr)
		if !ok || raw == "" {
			return
		}
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return
		}
		s.SetAttr(attr, pageBase.ResolveReference(ref).String())
	}
	sel.Find("a[href]").Each(func(_ int, s *goquery.Selection) { rewrite(s, "href") })
	sel.Find("img[src]").Each(func(_ int, s *goquery.Selection) { rewrite(s, "src") })
}

func domainOf(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
