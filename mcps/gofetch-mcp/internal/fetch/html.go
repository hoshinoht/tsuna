package fetch

import (
	"bytes"
	"net/url"
	"strings"
	"unicode"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// candidateSelectors are plausible main-content containers. Every match is
// scored; none is trusted just for appearing first.
const candidateSelectors = "article, main, [role=main], [itemprop=articleBody], #content, #main, #main-content, " +
	".content, .post, .post-content, .entry-content, .article-body, .markdown-body, .prose, .documentation"

const (
	maxCandidates = 40
	// minCandidateChars is the least text a container needs to be eligible.
	minCandidateChars = 140
	// maxLinkDensity rejects containers that are mostly link lists.
	maxLinkDensity = 0.5
	// refineRatio: a nested candidate replaces its container when it keeps
	// at least this share of the container's score (it is more specific).
	refineRatio = 0.7
	// minBodyShare: a winner scoring below this share of the whole page is
	// incidental (a teaser card, a comment), so the page body is used.
	minBodyShare = 0.2
)

// alwaysStripped is page chrome and non-content markup removed everywhere.
const alwaysStripped = "script, style, noscript, template, iframe, svg, canvas, nav, footer, " +
	"button, input, select, textarea, [role=navigation], [role=contentinfo], [aria-hidden=true], [hidden]"

// strippedOutsideContent is removed only when not inside an article/main,
// where it usually carries the page heading or a callout.
const strippedOutsideContent = "header, aside, [role=banner], [role=complementary]"

const contentAncestors = "article, main, [role=main]"

// htmlExtraction is the converted page plus the raw-page signals the
// quality assessment needs.
type htmlExtraction struct {
	markdown  string
	title     string
	textChars int // visible text of the selected container

	rawLower     string // lower-cased raw HTML prefix, for marker search
	hasPassword  bool
	scriptCount  int
	noscriptJS   bool
	emptySPARoot bool
}

const rawMarkerScanBytes = 512 << 10

// convertHTML is the markdown-and-title view of extractHTML.
func convertHTML(body []byte, pageURL string) (markdown, title string, err error) {
	ex, err := extractHTML(body, "", pageURL)
	if err != nil {
		return "", "", err
	}
	return ex.markdown, ex.title, nil
}

func extractHTML(body []byte, contentType, pageURL string) (*htmlExtraction, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(decodeToUTF8(body, contentType)))
	if err != nil {
		return nil, err
	}
	ex := &htmlExtraction{title: collapseSpace(doc.Find("title").First().Text())}

	raw := body
	if len(raw) > rawMarkerScanBytes {
		raw = raw[:rawMarkerScanBytes]
	}
	ex.rawLower = string(bytes.ToLower(raw))
	ex.hasPassword = doc.Find(`input[type=password]`).Length() > 0
	ex.scriptCount = doc.Find("script").Length()
	doc.Find("noscript").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		ex.noscriptJS = strings.Contains(strings.ToLower(s.Text()), "javascript")
		return !ex.noscriptJS
	})
	doc.Find("#root, #__next, #app, #__nuxt, #___gatsby, [data-reactroot]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		ex.emptySPARoot = textChars(s.Text()) == 0
		return !ex.emptySPARoot
	})

	stripChrome(doc)
	sel := selectContent(doc)
	resolveLinks(sel, pageURL)
	ex.textChars = textChars(sel.Text())

	md, err := toMarkdown(sel, pageURL)
	if err != nil {
		return nil, err
	}
	if h := orphanHeading(doc, sel); h != "" && !strings.HasPrefix(md, "# ") {
		md = "# " + h + "\n\n" + md
	}
	ex.markdown = md
	return ex, nil
}

func stripChrome(doc *goquery.Document) {
	doc.Find(alwaysStripped).Remove()
	doc.Find(strippedOutsideContent).Each(func(_ int, s *goquery.Selection) {
		if s.ParentsFiltered(contentAncestors).Length() == 0 {
			s.Remove()
		}
	})
	// Forms are dropped only when they hold no document structure: some
	// frameworks wrap the entire page in one <form>.
	doc.Find("form").Each(func(_ int, s *goquery.Selection) {
		if s.Find("p, h1, h2, h3, h4, h5, h6, table, pre, li, article").Length() == 0 {
			s.Remove()
		}
	})
}

type candidate struct {
	sel   *goquery.Selection
	node  *html.Node
	score float64
}

// selectContent scores every plausible container by visible text weighted
// against link density, prefers a nested candidate that keeps most of its
// parent's text, and falls back to <body> when nothing is suitable.
func selectContent(doc *goquery.Document) *goquery.Selection {
	body := doc.Find("body").First()
	if body.Length() == 0 {
		body = doc.Selection
	}
	bodyScore := contentScore(body)

	var cands []candidate
	doc.Find(candidateSelectors).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if score := contentScore(s); score > 0 {
			cands = append(cands, candidate{sel: s, node: s.Get(0), score: score})
		}
		return len(cands) < maxCandidates
	})
	if len(cands) == 0 {
		return body
	}

	best := cands[0]
	for _, c := range cands[1:] {
		if c.score > best.score {
			best = c
		}
	}
	for refined := true; refined; {
		refined = false
		for _, c := range cands {
			if c.node != best.node && isAncestor(best.node, c.node) && c.score >= refineRatio*best.score {
				best, refined = c, true
				break
			}
		}
	}
	if best.score < minBodyShare*bodyScore {
		return body
	}
	return best.sel
}

// contentScore is visible text discounted by link density; 0 marks an
// ineligible container.
func contentScore(s *goquery.Selection) float64 {
	text := textChars(s.Text())
	if text < minCandidateChars {
		return 0
	}
	density := float64(textChars(s.Find("a").Text())) / float64(text)
	if density > maxLinkDensity {
		return 0
	}
	return float64(text) * (1 - density)
}

func isAncestor(ancestor, n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p == ancestor {
			return true
		}
	}
	return false
}

// orphanHeading returns the page's single <h1> when it sits outside the
// selected container (a title rendered above <main>), so it is not lost.
func orphanHeading(doc *goquery.Document, sel *goquery.Selection) string {
	if sel.Is("body") || sel.Find("h1").Length() > 0 {
		return ""
	}
	h1s := doc.Find("h1")
	if h1s.Length() != 1 {
		return ""
	}
	return collapseSpace(h1s.Text())
}

func toMarkdown(sel *goquery.Selection, pageURL string) (string, error) {
	fragment, err := goquery.OuterHtml(sel)
	if err != nil {
		return "", err
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
		return "", err
	}
	return strings.TrimSpace(md), nil
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

// textChars counts the runes of s with whitespace runs collapsed to one
// space, i.e. the visible text length.
func textChars(s string) int {
	n, inSpace := 0, true
	for _, r := range s {
		if unicode.IsSpace(r) {
			inSpace = true
			continue
		}
		if inSpace && n > 0 {
			n++
		}
		inSpace = false
		n++
	}
	return n
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
