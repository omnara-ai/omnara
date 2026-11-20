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

func TestAudioMediaTypeForFilename(t *testing.T) {
	for filename, want := range map[string]string{
		"take.wav":       "audio/wav",
		"take.MP3":       "audio/mpeg",
		"voice memo.m4a": "audio/mp4",
		"master.flac":    "audio/flac",
	} {
		if got, ok := AudioMediaTypeForFilename(filename); !ok || got != want {
			t.Fatalf("media type for %s = %q, %t; want %q", filename, got, ok, want)
		}
	}
	for _, filename := range []string{"take.ogg", "notes.txt", "flac", ""} {
		if got, ok := AudioMediaTypeForFilename(filename); ok {
			t.Fatalf("media type for %q = %q, want none", filename, got)
		}
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
