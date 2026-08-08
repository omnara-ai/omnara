package envname

import "testing"

func TestValid(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "APP_ENV", want: true},
		{name: "lower_name", want: true},
		{name: "_PRIVATE", want: true},
		{name: "A1", want: true},
		{name: ""},
		{name: "1APP"},
		{name: "APP-NAME"},
		{name: "APP.NAME"},
		{name: "APP NAME"},
		{name: "APP\nNAME"},
		{name: "ÄPP"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Valid(test.name); got != test.want {
				t.Fatalf("Valid(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}
