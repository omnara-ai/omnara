package fileread_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestPagingPreservesContent(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "unicode lines", content: strings.Repeat("é😀\n", 2000)},
		{name: "giant line", content: "é😀first\n" + strings.Repeat("é😀", 5000) + "\nlast"},
		{name: "escaped text", content: strings.Repeat("\t", 9000)},
		{name: "empty file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []byte(test.content)
			page := fileread.Lines(content, "/artifacts/example", 1, toolcatalog.ReadFileDefaultLines)
			var got string
			for {
				chunk, ok := page["content"].(string)
				if !ok || !utf8.ValidString(chunk) || (chunk == "" && page["has_more"] == true) {
					t.Fatal("invalid or stalled page")
				}
				if len(chunk) > toolcatalog.FilePageBytes || page["bytes_read"] != len(chunk) {
					t.Fatalf("invalid byte count: %v", page)
				}
				got += chunk
				if len(got) > len(content) {
					t.Fatal("paging duplicated content")
				}
				assertRetrievalBudget(t, page)
				if page["has_more"] == false {
					if page["next_offset_char"] != nil || page["next_offset_line"] != nil {
						t.Fatal("final page has continuation")
					}
					break
				}
				if offset, ok := page["next_offset_line"].(int); ok {
					page = fileread.Lines(content, "/artifacts/example", offset, toolcatalog.ReadFileDefaultLines)
				} else if offset, ok := page["next_offset_char"].(int); ok {
					if offset != utf8.RuneCountInString(got) {
						t.Fatalf("character continuation = %d, want %d", offset, utf8.RuneCountInString(got))
					}
					page = fileread.Chars(content, "/artifacts/example", offset, toolcatalog.ReadFileDefaultChars)
				} else {
					t.Fatal("missing continuation")
				}
			}
			if got != test.content {
				t.Fatal("paging lost content")
			}
		})
	}
	page := fileread.Lines([]byte("a\nb\nc"), "path", 2, 1)
	if page["content"] != "b\n" || page["next_offset_line"] != 3 {
		t.Fatalf("line page = %v", page)
	}
}

func TestChars(t *testing.T) {
	for _, test := range []struct {
		name          string
		content       string
		offset, limit int
		want          string
		wantOffset    int
		nextOffset    any
	}{
		{name: "four-byte character", content: "abc😀z", offset: 3, limit: 1, want: "😀", wantOffset: 3, nextOffset: 4},
		{name: "after multibyte characters", content: "é😀z", offset: 2, limit: 1, want: "z", wantOffset: 2},
		{name: "code point", content: "éx", offset: 1, limit: 1, want: "́", wantOffset: 1, nextOffset: 2},
		{name: "at EOF", content: "é😀", offset: 2, limit: 1, wantOffset: 2},
		{name: "past EOF", content: "é😀", offset: 100, limit: 1, wantOffset: 2},
		{name: "empty file", offset: 100, limit: 1},
		{
			name: "byte cap", content: strings.Repeat("a", 4095) + "😀z",
			limit: toolcatalog.ReadFileMaxChars, want: strings.Repeat("a", 4095), nextOffset: 4095,
		},
		{
			name: "unicode byte cap", content: strings.Repeat("😀", 2000),
			limit: toolcatalog.ReadFileMaxChars, want: strings.Repeat("😀", 1024), nextOffset: 1024,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := fileread.Chars([]byte(test.content), "path", test.offset, test.limit)
			if page["content"] != test.want || page["offset_char"] != test.wantOffset ||
				page["bytes_read"] != len(test.want) || page["next_offset_char"] != test.nextOffset ||
				page["has_more"] != (test.nextOffset != nil) {
				t.Fatalf("page = %v", page)
			}
			assertRetrievalBudget(t, page)
		})
	}
}

func TestNormalizeDefaultsAndRejectsMixedModes(t *testing.T) {
	zero, five, big := 0, 5, toolcatalog.ReadFileMaxChars+1
	lines, err := fileread.Paging{}.Normalize()
	if err != nil || *lines.OffsetLine != 1 || *lines.LimitLines != toolcatalog.ReadFileDefaultLines ||
		lines.OffsetChar != nil || lines.LimitChars != nil {
		t.Fatalf("line defaults = %+v, %v", lines, err)
	}
	chars, err := fileread.Paging{OffsetChar: &five}.Normalize()
	if err != nil || *chars.OffsetChar != 5 || *chars.LimitChars != toolcatalog.ReadFileDefaultChars ||
		chars.OffsetLine != nil {
		t.Fatalf("character defaults = %+v, %v", chars, err)
	}
	for _, invalid := range []fileread.Paging{
		{OffsetLine: &five, LimitChars: &five},
		{OffsetLine: &zero},
		{LimitChars: &zero},
		{LimitChars: &big},
	} {
		if _, err := invalid.Normalize(); err == nil {
			t.Fatalf("accepted paging %+v", invalid)
		}
	}
}

func TestPageSelectsModeAndRejectsBinary(t *testing.T) {
	two := 2
	paging, err := fileread.Paging{OffsetChar: &two}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	page, err := paging.Page([]byte("abcdef"), "notes.txt")
	if err != nil || page["content"] != "cdef" || page["offset_char"] != 2 {
		t.Fatalf("character page = %v, %v", page, err)
	}
	lines, err := fileread.Paging{}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range [][]byte{{0xff, 0xfe}, []byte("a\x00b")} {
		if _, err := lines.Page(content, "blob.bin"); !errors.Is(err, fileread.ErrUnreadableContent) {
			t.Fatalf("binary page error = %v", err)
		}
	}
	if _, err := (fileread.Paging{}).Page([]byte("a"), "a"); err == nil {
		t.Fatal("unnormalized paging was accepted")
	}
}

func TestIsViewableImageRequiresValidMatchingImage(t *testing.T) {
	images := testImages(t)
	for contentType, content := range images {
		if !fileread.IsViewableImage(contentType, content) {
			t.Fatalf("IsViewableImage(%q) = false for a valid image", contentType)
		}
	}
	pngContent := images["image/png"]
	for _, test := range []struct {
		contentType string
		content     []byte
	}{
		{contentType: "image/png", content: pngContent[:16]},
		{contentType: "image/png", content: []byte("plain text named .png")},
		{contentType: "image/jpeg", content: pngContent},
		{contentType: "application/octet-stream", content: pngContent},
	} {
		if fileread.IsViewableImage(test.contentType, test.content) {
			t.Fatalf("IsViewableImage(%q, %q) = true, want false", test.contentType, test.content)
		}
	}
}

func TestViewableImageTypesAreModelImages(t *testing.T) {
	for _, contentType := range fileread.ViewableImageTypes() {
		if kind, _ := modelcontext.AttachmentKindForMediaType(contentType); kind != modelcontext.AttachmentKindImage {
			t.Fatalf("%s is not a model image attachment", contentType)
		}
	}
}

func testImages(t *testing.T) map[string][]byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	var pngContent, jpegContent, gifContent bytes.Buffer
	if err := png.Encode(&pngContent, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpegContent, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifContent, img, nil); err != nil {
		t.Fatal(err)
	}
	webpContent, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"image/png":  pngContent.Bytes(),
		"image/jpeg": jpegContent.Bytes(),
		"image/gif":  gifContent.Bytes(),
		"image/webp": webpContent,
	}
}

func assertRetrievalBudget(t *testing.T, result map[string]any) {
	t.Helper()
	encoded, err := json.Marshal([]map[string]any{{"type": "structured_data", "value": result}})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 32768 {
		t.Fatalf("retrieval response exceeds compaction budget: %d", len(encoded))
	}
}
