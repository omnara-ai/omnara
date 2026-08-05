package webaccess

import (
	"bytes"
	"fmt"
	"mime"
	"net/url"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
)

type extractedContent struct {
	Title   string
	Content string
}

// extractContent turns a fetched body into model-readable text. HTML goes
// through readability extraction and markdown conversion; non-HTML text types
// pass through; binary types are rejected.
func extractContent(body []byte, contentType, pageURL, format string) (extractedContent, error) {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		mediaType = parsed
	}
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || (mediaType == "" && looksLikeHTML(body)):
		return extractHTML(body, pageURL, format)
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

func extractHTML(body []byte, pageURL, format string) (extractedContent, error) {
	parsedURL, err := url.Parse(pageURL)
	if err != nil {
		parsedURL = &url.URL{}
	}
	article, err := readability.FromReader(bytes.NewReader(body), parsedURL)
	if err != nil {
		return extractedContent{}, &ProviderError{
			Code:    ErrorCodeFetchFailed,
			Message: fmt.Sprintf("extract readable content: %v", err),
		}
	}
	if format == "text" {
		text := strings.TrimSpace(article.TextContent)
		if text == "" {
			return extractedContent{}, &ProviderError{
				Code:    ErrorCodeFetchFailed,
				Message: "no readable text content found on the page",
			}
		}
		return extractedContent{Title: article.Title, Content: text}, nil
	}
	html := article.Content
	if strings.TrimSpace(html) == "" {
		html = string(body)
	}
	markdown, err := htmltomarkdown.ConvertString(html)
	if err != nil {
		return extractedContent{}, &ProviderError{
			Code:    ErrorCodeFetchFailed,
			Message: fmt.Sprintf("convert content to markdown: %v", err),
		}
	}
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return extractedContent{}, &ProviderError{
			Code:    ErrorCodeFetchFailed,
			Message: "no readable content found on the page",
		}
	}
	return extractedContent{Title: article.Title, Content: markdown}, nil
}

func looksLikeHTML(body []byte) bool {
	head := bytes.ToLower(bytes.TrimSpace(body))
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("<html")) || bytes.HasPrefix(head, []byte("<!doctype html"))
}
