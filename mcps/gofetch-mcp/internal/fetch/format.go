package fetch

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/html/charset"
)

var pdfMagic = []byte("%PDF-")

// sniffKind decides how to extract a response: "html", "pdf", "text" or
// "markdown". ok is false for formats fetch cannot extract (images, archives,
// office documents, ...) so they are rejected instead of parsed as HTML.
func sniffKind(body []byte, contentType, pageURL string) (kind string, ok bool) {
	if bytes.HasPrefix(bytes.TrimLeft(body, "\r\n\t "), pdfMagic) {
		return "pdf", true
	}
	mediaType := mediaTypeOf(contentType)
	sniffed := mediaTypeOf(http.DetectContentType(body))

	switch {
	case mediaType == "application/pdf" || strings.HasSuffix(mediaType, "/x-pdf"):
		return "pdf", true
	case mediaType == "" || mediaType == "application/octet-stream" || mediaType == "binary/octet-stream":
		// Missing or generic type: trust the body.
		switch sniffed {
		case "text/html":
			return "html", true
		case "text/plain", "text/xml":
			return textKind(pageURL), true
		}
		return "", false
	}

	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		kind = "html"
	case mediaType == "text/markdown" || mediaType == "text/x-markdown":
		kind = "markdown"
	case mediaType == "text/plain":
		kind = textKind(pageURL)
	case isTextual(mediaType):
		kind = "text"
	default:
		return "", false
	}
	// A declared text type over a binary body (an image behind text/html,
	// say) is still binary.
	if !strings.HasPrefix(sniffed, "text/") {
		return "", false
	}
	return kind, true
}

func mediaTypeOf(contentType string) string {
	if contentType == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt, _, _ = strings.Cut(contentType, ";")
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

func isTextual(mediaType string) bool {
	if strings.HasPrefix(mediaType, "text/") ||
		strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript",
		"application/yaml", "application/x-yaml", "application/toml", "application/x-sh":
		return true
	}
	return false
}

func textKind(pageURL string) string {
	if u, err := url.Parse(pageURL); err == nil {
		switch strings.ToLower(path.Ext(u.Path)) {
		case ".md", ".markdown", ".mdx":
			return "markdown"
		}
	}
	return "text"
}

// describeType names a rejected response's type for the error message.
func describeType(body []byte, contentType string) string {
	if mt := mediaTypeOf(contentType); mt != "" && mt != "application/octet-stream" {
		if sniffed := mediaTypeOf(http.DetectContentType(body)); !strings.HasPrefix(sniffed, "text/") && sniffed != mt && sniffed != "application/octet-stream" {
			return mt + " (body looks like " + sniffed + ")"
		}
		return mt
	}
	return "binary content (" + mediaTypeOf(http.DetectContentType(body)) + ")"
}

// convertText decodes a text body to UTF-8 (honouring a declared charset or
// BOM) and normalizes line endings.
func convertText(body []byte, contentType string) string {
	text := decodeToUTF8(body, contentType)
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimSpace(text)
}

func decodeToUTF8(body []byte, contentType string) string {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return strings.ToValidUTF8(string(body), "\uFFFD")
	}
	decoded, err := io.ReadAll(r)
	if err != nil {
		return strings.ToValidUTF8(string(body), "\uFFFD")
	}
	return strings.ToValidUTF8(string(decoded), "\uFFFD")
}

func firstMarkdownHeading(md string) string {
	for _, line := range strings.SplitN(md, "\n", 50) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
