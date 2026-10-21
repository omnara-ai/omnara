package httpapi

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestUploadedArtifactContentTypePrefersDetectedImageType(t *testing.T) {
	var pngContent bytes.Buffer
	if err := png.Encode(&pngContent, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		filename string
		content  []byte
		want     string
	}{
		{filename: "screenshot.jpg", content: pngContent.Bytes(), want: "image/png"},
		{filename: "screenshot.png", content: []byte("png bytes"), want: "image/png"},
		{filename: "notes", content: []byte("plain text"), want: "text/plain; charset=utf-8"},
	} {
		if got := uploadedArtifactContentType(test.filename, test.content); got != test.want {
			t.Fatalf("uploadedArtifactContentType(%q) = %q, want %q", test.filename, got, test.want)
		}
	}
}

func TestCheckUploadSeen(t *testing.T) {
	const path, current = "/memory/notes/a.md", "sha256:current"
	for _, test := range []struct {
		name  string
		seen  memorystore.FileView
		found bool
		want  string
	}{
		{"read current content", memorystore.FileView{Digest: current}, true, ""},
		{"never read", memorystore.FileView{}, false,
			path + " exists but you have not read it; read or download it before uploading"},
		{"changed since read", memorystore.FileView{Digest: "sha256:old"}, true,
			path + " changed since you last read it; read or download it again before uploading"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkUploadSeen(path, current, test.seen, test.found)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != test.want || !errors.Is(err, storeerr.ErrConflict) {
				t.Fatalf("error = %v, want conflict %q", err, test.want)
			}
		})
	}
}
