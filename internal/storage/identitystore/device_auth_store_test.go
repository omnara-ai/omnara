package identitystore

import "testing"

func TestCanonicalDeviceUserCode(t *testing.T) {
	for input, want := range map[string]string{
		"abcde f1234": "ABCDE-F1234",
		"ABCDE-F1234": "ABCDE-F1234",
		"not-a-code":  "",
	} {
		got, ok := CanonicalDeviceUserCode(input)
		if got != want || ok != (want != "") {
			t.Errorf("CanonicalDeviceUserCode(%q) = %q, %t; want %q, %t", input, got, ok, want, want != "")
		}
	}
}
