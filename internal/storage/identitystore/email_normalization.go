package identitystore

import (
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"
)

func NormalizeEmail(email string) string {
	normalized := legacyNormalizedEmail(email)
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
	legacy := legacyNormalizedEmail(email)
	canonical := NormalizeEmail(email)
	candidates := []string{canonical, legacy}
	local, domain, ok := splitEmail(canonical)
	if ok {
		unicodeDomain, err := idna.Lookup.ToUnicode(domain)
		if err == nil && unicodeDomain != "" {
			unicodeDomain = strings.ToLower(unicodeDomain)
			candidates = append(
				candidates,
				local+"@"+unicodeDomain,
				local+"@"+norm.NFD.String(unicodeDomain),
			)
		}
	}
	keys := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		keys = append(keys, candidate)
	}
	return keys
}

func legacyNormalizedEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func splitEmail(email string) (string, string, bool) {
	separator := strings.LastIndexByte(email, '@')
	if separator <= 0 || separator == len(email)-1 {
		return "", "", false
	}
	return email[:separator], email[separator+1:], true
}
