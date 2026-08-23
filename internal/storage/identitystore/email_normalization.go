package identitystore

import (
	"strings"

	"golang.org/x/net/idna"
)

func NormalizeEmail(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	local, domain, ok := splitEmail(normalized)
	if !ok {
		return normalized
	}
	asciiDomain, err := idna.Lookup.ToASCII(domain)
	if err != nil || asciiDomain == "" {
		return normalized
	}
	return local + "@" + strings.ToLower(asciiDomain)
}

// Include forms that may have been stored before domains were converted to IDNA ASCII.
func normalizedEmailLookupKeys(email string) []string {
	legacy := strings.ToLower(strings.TrimSpace(email))
	canonical := NormalizeEmail(email)
	keys := []string{canonical}
	if legacy != canonical {
		keys = append(keys, legacy)
	}
	local, domain, ok := splitEmail(canonical)
	if !ok {
		return keys
	}
	unicodeDomain, err := idna.Lookup.ToUnicode(domain)
	if err != nil || unicodeDomain == "" {
		return keys
	}
	unicodeKey := local + "@" + strings.ToLower(unicodeDomain)
	for _, key := range keys {
		if key == unicodeKey {
			return keys
		}
	}
	return append(keys, unicodeKey)
}

func splitEmail(email string) (string, string, bool) {
	separator := strings.LastIndexByte(email, '@')
	if separator <= 0 || separator == len(email)-1 {
		return "", "", false
	}
	return email[:separator], email[separator+1:], true
}
