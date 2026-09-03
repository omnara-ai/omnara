package registryname

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "simple", value: "discord", want: true},
		{name: "namespaced", value: "chat_sdk-v1.provider", want: true},
		{name: "empty", value: "", want: false},
		{name: "uppercase", value: "Discord", want: false},
		{name: "leading punctuation", value: "_discord", want: false},
		{name: "too long", value: strings.Repeat("a", 129), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Valid(test.value); got != test.want {
				t.Fatalf("Valid(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}
