package emailaddr

import (
	"strings"

	"golang.org/x/net/idna"
)

var domain = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.VerifyDNSLength(true))

func Normalize(email string) string {
	trimmed := strings.TrimSpace(email)
	at := strings.LastIndexByte(trimmed, '@')
	if at <= 0 || at == len(trimmed)-1 {
		return strings.ToLower(trimmed)
	}
	local, host := trimmed[:at], trimmed[at+1:]
	ascii, err := domain.ToASCII(host)
	if err != nil {
		ascii = asciiLower(host)
	}
	return strings.ToLower(local) + "@" + ascii
}

func Equal(a, b string) bool {
	return Normalize(a) == Normalize(b)
}

func asciiLower(text string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, text)
}
