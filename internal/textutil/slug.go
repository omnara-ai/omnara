package textutil

func IsLowerURLSafeLabel(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	lastHyphen := false
	for i, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			lastHyphen = false
		case r >= '0' && r <= '9':
			lastHyphen = false
		case r == '-' && i > 0:
			lastHyphen = true
		default:
			return false
		}
	}
	return !lastHyphen
}
