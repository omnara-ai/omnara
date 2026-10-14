package webaccess

import (
	"bytes"
	"cmp"
	"fmt"
	"mime"
	"slices"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
)

type extractedContent struct {
	Title   string
	Content string
}

// extractContent turns a fetched body into model-readable text. HTML is
// converted from the page's main element, or the whole page when it has none;
// non-HTML text types pass through; binary types are rejected.
func extractContent(body []byte, contentType, pageURL, format string) (extractedContent, error) {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		mediaType = parsed
	}
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || (mediaType == "" && looksLikeHTML(body)):
		return extractHTML(body, contentType, pageURL, format)
	case strings.HasPrefix(mediaType, "text/"),
		mediaType == "application/json",
		mediaType == "application/xml",
		strings.HasSuffix(mediaType, "+json"),
		strings.HasSuffix(mediaType, "+xml"):
		return extractedContent{Content: string(body)}, nil
	default:
		return extractedContent{}, &ProviderError{
			Code:    ErrorCodeFetchUnsupported,
			Message: fmt.Sprintf("unsupported content type %q: only HTML and text content can be fetched", contentType),
		}
	}
}

func extractHTML(body []byte, contentType, pageURL, format string) (extractedContent, error) {
	decoded, err := charset.NewReader(bytes.NewReader(body), contentType)
	var doc *html.Node
	if err == nil {
		doc, err = html.Parse(decoded)
	}
	if err != nil {
		return extractedContent{}, &ProviderError{
			Code:    ErrorCodeFetchFailed,
			Message: fmt.Sprintf("parse html: %v", err),
		}
	}
	removeNonContent(doc)
	var title string
	if node := findElement(doc, atom.Title); node != nil {
		title = strings.Join(strings.Fields(textContent(node)), " ")
	}
	root := cmp.Or(findElement(doc, atom.Main), findElement(doc, atom.Body), doc)
	if format == "text" {
		text := strings.TrimSpace(textContent(root))
		if text == "" {
			return extractedContent{}, &ProviderError{
				Code:    ErrorCodeFetchFailed,
				Message: "no readable text content found on the page",
			}
		}
		return extractedContent{Title: title, Content: text}, nil
	}
	markdown, err := htmltomarkdown.ConvertNode(root, converter.WithDomain(pageURL))
	if err != nil {
		return extractedContent{}, &ProviderError{
			Code:    ErrorCodeFetchFailed,
			Message: fmt.Sprintf("convert content to markdown: %v", err),
		}
	}
	content := strings.TrimSpace(string(markdown))
	if content == "" {
		return extractedContent{}, &ProviderError{
			Code:    ErrorCodeFetchFailed,
			Message: "no readable content found on the page",
		}
	}
	return extractedContent{Title: title, Content: content}, nil
}

var nonContentElements = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Noscript: true,
	atom.Template: true,
	atom.Svg:      true,
}

func removeNonContent(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && nonContentElements[c.DataAtom] {
			n.RemoveChild(c)
		} else {
			removeNonContent(c)
		}
		c = next
	}
}

func hasAttr(n *html.Node, key string) bool {
	return slices.ContainsFunc(n.Attr, func(attr html.Attribute) bool { return attr.Key == key })
}

func findElement(n *html.Node, tag atom.Atom) *html.Node {
	for d := range n.Descendants() {
		if d.Type == html.ElementNode && d.DataAtom == tag && !hasAttr(d, "hidden") {
			return d
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var b strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			b.WriteString(d.Data)
		}
	}
	return b.String()
}

func looksLikeHTML(body []byte) bool {
	head := bytes.ToLower(bytes.TrimSpace(body))
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("<html")) || bytes.HasPrefix(head, []byte("<!doctype html"))
}
