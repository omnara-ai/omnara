package slack

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDefaultAppIconValid(t *testing.T) {
	icon := DefaultAppIcon()
	if icon.Filename != "default-app-icon.png" || icon.ContentType != "image/png" || len(icon.Content) == 0 {
		t.Fatalf("default icon = %+v bytes=%d", icon, len(icon.Content))
	}
	validated, err := NewAppIcon(icon.Filename, icon.Content)
	if err != nil {
		t.Fatalf("validate default icon: %v", err)
	}
	if validated.Filename != icon.Filename || validated.ContentType != icon.ContentType {
		t.Fatalf("validated default icon = %+v", validated)
	}
}

func TestNewAppIconRejectsSmallImage(t *testing.T) {
	content, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGBgAAAABQABh6FO1AAAAABJRU5ErkJggg==",
	)
	if err != nil {
		t.Fatalf("decode test icon: %v", err)
	}
	_, err = NewAppIcon("small.png", content)
	if err == nil || !strings.Contains(err.Error(), "between 512x512 and 2000x2000") {
		t.Fatalf("NewAppIcon error = %v, want dimension error", err)
	}
}
