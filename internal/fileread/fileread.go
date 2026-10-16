// Package fileread pages and classifies read_file content for Omnara and machine files.
package fileread

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	_ "golang.org/x/image/webp"
)

var ErrUnreadableContent = errors.New(
	"file must contain UTF-8 text without NUL bytes, or be a PNG, JPEG, GIF, or WebP image",
)

type Paging struct {
	OffsetLine *int `json:"offset_line,omitempty"`
	LimitLines *int `json:"limit_lines,omitempty"`
	OffsetChar *int `json:"offset_char,omitempty"`
	LimitChars *int `json:"limit_chars,omitempty"`
}

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

// IsText reports whether content is UTF-8 text without NUL bytes.
func IsText(content []byte) bool {
	return utf8.Valid(content) && bytes.IndexByte(content, 0) < 0
}

func (p Paging) Page(content []byte, path string) (map[string]any, error) {
	if !IsText(content) {
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

func ViewableImageTypes() []string {
	return []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
}

func IsViewableImage(contentType string, content []byte) bool {
	if !slices.Contains(ViewableImageTypes(), contentType) {
		return false
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(content))
	return err == nil && "image/"+format == contentType
}

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
