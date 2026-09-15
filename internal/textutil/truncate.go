package textutil

import "unicode/utf8"

func TruncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit || utf8.RuneCountInString(s) <= limit {
		return s
	}
	count := 0
	for index := range s {
		if count == limit {
			return s[:index]
		}
		count++
	}
	return s
}

func TruncateBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
