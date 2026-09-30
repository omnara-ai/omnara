package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"testing"
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
