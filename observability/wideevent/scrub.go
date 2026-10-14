package wideevent

import (
	"net/url"
	"regexp"
	"strings"
)

const redactedLogValue = "[REDACTED]"

var (
	urlLikePattern = regexp.MustCompile(`(?i)(^|[\s"'(<])((?:[a-z][a-z0-9+.-]*://|/)[^\s"'<>)]*)`)

	bearerTokenPattern = regexp.MustCompile(
		`(?i)(\b(?:authorization\s*[:=]\s*)?bearer\s+)(?:"[^"]*"|'[^']*'|[^\s,;&]+)`,
	)
	sensitiveAssignmentPattern = regexp.MustCompile(
		`(?i)(\b(?:access[_-]?token|api[_-]?key|auth[_-]?token|client[_-]?secret|code|password|` +
			`passwd|pwd|refresh[_-]?token|secret|token)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;&]+)`,
	)
	quotedSensitivePattern = regexp.MustCompile(
		`(?i)(["'](?:access[_-]?token|api[_-]?key|auth[_-]?token|authorization|bearer|` +
			`client[_-]?secret|code|password|passwd|pwd|refresh[_-]?token|secret|token)["']` +
			`\s*:\s*)(?:"[^"]*"|'[^']*'|[^\s,;&}]+)`,
	)
	// providerSecretPattern matches well-known provider credential shapes that
	// can appear in error text without an assignment: Stripe secret,
	// restricted, and webhook-signing keys and OpenRouter keys.
	providerSecretPattern = regexp.MustCompile(`\b(?:(?:sk|rk)_(?:live|test)_|whsec_|sk-or-)[A-Za-z0-9_-]+`)
)

// ScrubLogString removes URL query strings and redacts common free-form
// secret assignments and provider credential shapes before a value is emitted
// to process logs.
func ScrubLogString(value string) string {
	if value == "" {
		return value
	}
	value = scrubURLParameters(value)
	value = bearerTokenPattern.ReplaceAllString(value, `${1}`+redactedLogValue)
	value = quotedSensitivePattern.ReplaceAllString(value, `${1}"`+redactedLogValue+`"`)
	value = sensitiveAssignmentPattern.ReplaceAllString(value, `${1}`+redactedLogValue)
	value = providerSecretPattern.ReplaceAllString(value, redactedLogValue)
	return value
}

func scrubURLParameters(value string) string {
	return urlLikePattern.ReplaceAllStringFunc(value, func(match string) string {
		prefixLen := 0
		for prefixLen < len(match) {
			c := match[prefixLen]
			if c == '/' || isASCIILetter(c) {
				break
			}
			prefixLen++
		}
		if prefixLen >= len(match) {
			return match
		}
		candidate := match[prefixLen:]
		suffix := ""
		for len(candidate) > 0 {
			last := candidate[len(candidate)-1]
			if !strings.ContainsRune(".,;:!?", rune(last)) {
				break
			}
			suffix = string(last) + suffix
			candidate = candidate[:len(candidate)-1]
		}
		scrubbed := scrubURL(candidate)
		if scrubbed == candidate {
			return match
		}
		return match[:prefixLen] + scrubbed + suffix
	})
}

func scrubURL(raw string) string {
	if raw == "" || (!strings.Contains(raw, "?") && !strings.Contains(raw, "@")) {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.RawQuery == "" && parsed.User == nil) {
		return raw
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.User = nil
	return parsed.String()
}

func isASCIILetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
