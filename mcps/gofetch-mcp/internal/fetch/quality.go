package fetch

import (
	"fmt"
	"net/http"
	"strings"
)

// Extraction-quality verdicts. They describe whether the requested document
// was actually extracted, never whether its content is true.
const (
	QualityUsable  = "usable"  // readable content of reasonable length
	QualityThin    = "thin"    // some text, but too little to trust as the document
	QualityBlocked = "blocked" // HTTP 200, but a challenge/login/consent wall
	QualityEmpty   = "empty"   // nothing readable extracted
)

const (
	// minContentChars is the least text (runes) for a "usable" verdict.
	minContentChars = 200
	// wallMaxChars bounds wall detection: a page with more visible text than
	// this is treated as real content even if it carries a wall marker,
	// because normal pages often embed captcha or bot-management scripts.
	wallMaxChars = 2000
)

type assessment struct {
	status   string
	reason   string
	warnings []string
}

// Lower-cased markers. Titles are matched on the <title>, phrases on the
// raw HTML (walls hide their text in scripts and attributes).
var (
	challengeTitles = []string{
		"just a moment", "attention required", "access denied", "security check", "are you a robot",
		"are you human", "ddos-guard", "checking your browser", "human verification", "bot verification",
		"pardon our interruption", "request unsuccessful", "please wait while we verify",
	}
	challengeMarkers = []string{
		"cf_chl_opt", "cf-browser-verification", "/cdn-cgi/challenge-platform/h/", "_incapsula_resource",
		"px-captcha", "captcha-delivery.com", "awswaf.com",
		"verify you are human", "verifying you are human", "enable javascript and cookies to continue",
		"complete the security check", "unusual traffic from your computer",
	}
	// captchaWidgets also appear on ordinary contact or comment forms, so
	// they only count on nearly empty pages.
	captchaWidgets = []string{"g-recaptcha", "h-captcha", "cf-turnstile"}
	loginTitles    = []string{"sign in", "sign-in", "log in", "login", "signin"}
	loginPhrases   = []string{
		"sign in to continue", "log in to continue", "please log in", "please sign in",
		"you must be logged in", "login required", "sign in to view",
	}
	consentHosts   = []string{"consent.", "guce."}
	consentPhrases = []string{"before you continue to", "we value your privacy", "your privacy choices", "manage cookie", "cookie preferences"}
	jsShellPhrases = []string{"enable javascript", "requires javascript", "javascript is disabled", "javascript to run this app"}
)

// assessHTML classifies an HTML extraction. Walls are checked first; their
// markers only count on pages with little visible text.
func assessHTML(ex *htmlExtraction, header http.Header, finalURL string) assessment {
	short := ex.textChars < wallMaxChars
	title := strings.ToLower(ex.title)
	text := strings.ToLower(ex.markdown)

	switch {
	case strings.EqualFold(strings.TrimSpace(header.Get("Cf-Mitigated")), "challenge"),
		short && (containsAny(title, challengeTitles) || containsAny(ex.rawLower, challengeMarkers)),
		ex.textChars < 2*minContentChars && containsAny(ex.rawLower, captchaWidgets):
		return assessment{QualityBlocked, "challenge", []string{
			"HTTP 200 but the page is a bot/anti-automation challenge, not the requested document"}}
	case short && (ex.hasPassword || containsAny(title, loginTitles) || containsAny(text, loginPhrases)) &&
		(ex.hasPassword || ex.textChars < wallMaxChars/2):
		return assessment{QualityBlocked, "login_required", []string{
			"the page is a sign-in wall; the document itself was not returned"}}
	case hasHostPrefix(finalURL, consentHosts) ||
		(short && (containsAny(text, consentPhrases) || (strings.Contains(text, "cookie") && strings.Contains(text, "consent")))):
		return assessment{QualityBlocked, "consent_wall", []string{
			"the page is a cookie/consent interstitial; the document was not reached"}}
	case ex.textChars < minContentChars && (ex.noscriptJS || ex.emptySPARoot || ex.scriptCount >= 3 || containsAny(ex.rawLower, jsShellPhrases)):
		return assessment{QualityEmpty, "javascript_required", []string{
			"the page is rendered client-side by JavaScript; only the static shell was received"}}
	}
	return assessLength(ex.textChars)
}

// assessPlain classifies text, Markdown and PDF extractions, which carry no
// wall markers.
func assessPlain(text, kind string) assessment {
	a := assessLength(textChars(text))
	if kind == "pdf" && a.status == QualityEmpty {
		a.warnings = []string{"the PDF has no extractable text (it may be scanned images)"}
	}
	return a
}

func assessLength(chars int) assessment {
	switch {
	case chars == 0:
		return assessment{QualityEmpty, "no_text", []string{"no readable text was extracted"}}
	case chars < minContentChars:
		return assessment{QualityThin, "short_content", []string{fmt.Sprintf(
			"only %d characters extracted; this may be a stub, redirect notice or partial render", chars)}}
	}
	return assessment{status: QualityUsable}
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func hasHostPrefix(rawURL string, prefixes []string) bool {
	host := strings.ToLower(hostOf(rawURL))
	for _, p := range prefixes {
		if strings.HasPrefix(host, p) {
			return true
		}
	}
	return false
}
