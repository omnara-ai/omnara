package modelcontext

import "testing"

func TestAudioAttachmentMediaTypes(t *testing.T) {
	for mediaType, format := range map[string]string{
		"audio/wav":  "wav",
		"audio/mpeg": "mp3",
		"audio/mp4":  "m4a",
		"audio/flac": "flac",
	} {
		if kind, ok := AttachmentKindForMediaType(mediaType); !ok || kind != AttachmentKindAudio {
			t.Fatalf("kind for %s = %q, %t", mediaType, kind, ok)
		}
		if got, ok := AudioInputFormat(mediaType); !ok || got != format {
			t.Fatalf("format for %s = %q, %t; want %q", mediaType, got, ok, format)
		}
		if got := MediaFilename("", mediaType); got != "attachment."+format {
			t.Fatalf("filename for %s = %q", mediaType, got)
		}
	}
	if _, ok := AudioInputFormat("image/png"); ok {
		t.Fatal("image/png must not have an audio format")
	}
	if IsAttachmentMedia("audio/ogg") {
		t.Fatal("audio/ogg must stay unsupported")
	}
}

func TestCanonicalAttachmentMediaType(t *testing.T) {
	for alias, want := range map[string]string{
		"audio/x-wav":  "audio/wav",
		"audio/wave":   "audio/wav",
		"audio/mp3":    "audio/mpeg",
		"audio/x-m4a":  "audio/mp4",
		"audio/x-flac": "audio/flac",
		"image/jpg":    "image/jpeg",
		"audio/ogg":    "audio/ogg",
	} {
		if got := CanonicalAttachmentMediaType(alias); got != want {
			t.Fatalf("canonical %s = %q, want %q", alias, got, want)
		}
	}
}
