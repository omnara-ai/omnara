package daemonversion

import "testing"

func TestParseRelease(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw   string
		valid bool
	}{
		{raw: "0.0.0", valid: true},
		{raw: "1.2.3", valid: true},
		{raw: "18446744073709551615.0.1", valid: true},
		{raw: "v1.2.3"},
		{raw: "1.2"},
		{raw: "1.2.3.4"},
		{raw: "01.2.3"},
		{raw: "1.02.3"},
		{raw: "1.2.03"},
		{raw: "1.2.x"},
		{raw: "1.2.3-dev"},
		{raw: Development},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()
			_, err := ParseRelease(test.raw)
			if (err == nil) != test.valid {
				t.Fatalf("parse release error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		left  Release
		right Release
		want  int
	}{
		{left: Release{1, 2, 3}, right: Release{1, 2, 3}, want: 0},
		{left: Release{2, 0, 0}, right: Release{1, 9, 9}, want: 1},
		{left: Release{1, 3, 0}, right: Release{1, 2, 9}, want: 1},
		{left: Release{1, 2, 4}, right: Release{1, 2, 5}, want: -1},
	} {
		if got := Compare(test.left, test.right); got != test.want {
			t.Fatalf("Compare(%v, %v) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestValidateDevelopment(t *testing.T) {
	t.Parallel()
	if err := Validate(Development); err != nil {
		t.Fatalf("Validate(%q): %v", Development, err)
	}
}
