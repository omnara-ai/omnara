// Package fileread renders read_file results so Omnara-stored files and
// machine files read through daemon uploads share one paging and image policy.
package fileread

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"  // Registers GIF decoding for viewable image checks.
	_ "image/jpeg" // Registers JPEG decoding for viewable image checks.
	_ "image/png"  // Registers PNG decoding for viewable image checks.
	"slices"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	_ "golang.org/x/image/webp" // Registers WebP decoding for viewable image checks.
)

// ErrUnreadableContent reports bytes that are neither UTF-8 text nor a viewable image.
var ErrUnreadableContent = errors.New(
	"file must contain UTF-8 text without NUL bytes, or be a PNG, JPEG, GIF, or WebP image",
)

// Paging selects the text window returned by read_file.
type Paging struct {
	OffsetLine *int `json:"offset_line,omitempty"`
	LimitLines *int `json:"limit_lines,omitempty"`
	OffsetChar *int `json:"offset_char,omitempty"`
	LimitChars *int `json:"limit_chars,omitempty"`
}

// Normalize validates the paging mode and fills its defaults.
func (p Paging) Normalize() (Paging, error) {
	charMode := p.OffsetChar != nil || p.LimitChars != nil
	lineMode := p.OffsetLine != nil || p.LimitLines != nil
	if charMode && lineMode {
		return Paging{}, errors.New("character paging cannot be combined with line paging")
	}
	if charMode {
		p.OffsetChar = valueOrDefault(p.OffsetChar, 0)
		p.LimitChars = valueOrDefault(p.LimitChars, toolcatalog.ReadFileDefaultChars)
		if *p.OffsetChar < 0 || *p.LimitChars < 1 || *p.LimitChars > toolcatalog.ReadFileMaxChars {
			return Paging{}, errors.New("invalid file character range")
		}
		return p, nil
	}
	p.OffsetLine = valueOrDefault(p.OffsetLine, 1)
	p.LimitLines = valueOrDefault(p.LimitLines, toolcatalog.ReadFileDefaultLines)
	if *p.OffsetLine < 1 || *p.LimitLines < 1 || *p.LimitLines > toolcatalog.ReadFileMaxLines {
		return Paging{}, errors.New("invalid file line range")
	}
	return p, nil
}

func valueOrDefault(value *int, fallback int) *int {
	if value != nil {
		return value
	}
	return &fallback
}

// Page returns one page of text from normalized paging. It reports
// ErrUnreadableContent when content is not UTF-8 text without NUL bytes.
func (p Paging) Page(content []byte, path string) (map[string]any, error) {
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, ErrUnreadableContent
	}
	if p.OffsetChar != nil && p.LimitChars != nil {
		return Chars(content, path, *p.OffsetChar, *p.LimitChars), nil
	}
	if p.OffsetLine == nil || p.LimitLines == nil {
		return nil, errors.New("read_file paging must be normalized")
	}
	return Lines(content, path, *p.OffsetLine, *p.LimitLines), nil
}

// ViewableImageTypes lists the image media types read_file returns for models to view.
func ViewableImageTypes() []string {
	return []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
}

// IsViewableImage reports whether content is a decodable image whose format
// matches contentType and that models can view.
func IsViewableImage(contentType string, content []byte) bool {
	if !slices.Contains(ViewableImageTypes(), contentType) {
		return false
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(content))
	return err == nil && "image/"+format == contentType
}

// Chars returns up to limit Unicode code points starting at offset, capped at one page.
func Chars(content []byte, path string, offset, limit int) map[string]any {
	start, position := 0, 0
	for position < offset && start < len(content) {
		_, size := utf8.DecodeRune(content[start:])
		start += size
		position++
	}
	end, count := start, 0
	for count < limit && end < len(content) {
		_, size := utf8.DecodeRune(content[end:])
		if end-start+size > toolcatalog.FilePageBytes {
			break
		}
		end += size
		count++
	}
	result := map[string]any{
		"path":        path,
		"content":     string(content[start:end]),
		"offset_char": position,
		"bytes_read":  end - start,
		"has_more":    end < len(content),
	}
	if end < len(content) {
		result["next_offset_char"] = position + count
	}
	return result
}

// Lines returns up to limitLines lines starting at offsetLine, capped at one page.
func Lines(content []byte, path string, offsetLine, limitLines int) map[string]any {
	start, lineNumber := 0, 1
	for lineNumber < offsetLine && start < len(content) {
		next := bytes.IndexByte(content[start:], '\n')
		if next < 0 {
			start = len(content)
			break
		}
		start += next + 1
		lineNumber++
	}
	end, count := start, 0
	for end < len(content) && count < limitLines {
		length := bytes.IndexByte(content[end:], '\n')
		if length < 0 {
			length = len(content) - end
		} else {
			length++
		}
		if end-start+length > toolcatalog.FilePageBytes {
			break
		}
		end += length
		count++
	}
	result := map[string]any{
		"path":        path,
		"offset_line": offsetLine,
		"lines_read":  count,
	}
	if count == 0 && start < len(content) {
		chunk := content[start:min(start+toolcatalog.FilePageBytes+utf8.UTFMax, len(content))]
		end = start + len(textutil.TruncateBytes(string(chunk), toolcatalog.FilePageBytes))
		result["notice"] = "The requested line exceeds one page; continue in character mode."
	}
	result["content"] = string(content[start:end])
	result["bytes_read"] = end - start
	result["has_more"] = end < len(content)
	if end < len(content) {
		if count == 0 {
			result["next_offset_char"] = utf8.RuneCount(content[:end])
		} else {
			result["next_offset_line"] = offsetLine + count
		}
	}
	return result
}
