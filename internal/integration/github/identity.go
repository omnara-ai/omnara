package github

import "strings"

// These limits match the existing gateway's GitHub configuration and opaque ID
// schemas. Setup must not persist an identity that the runtime cannot consume.
func validNodeID(value string) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	for _, char := range value {
		if !asciiAlphanumeric(char) && !strings.ContainsRune("_+=/-", char) {
			return false
		}
	}
	return true
}

func validRepositoryOwner(value string) bool {
	if len(value) == 0 || len(value) > 100 || !asciiAlphanumeric(rune(value[0])) {
		return false
	}
	for _, char := range value {
		if !asciiAlphanumeric(char) && char != '-' {
			return false
		}
	}
	return true
}

func validRepositoryName(value string) bool {
	if len(value) == 0 || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if !asciiAlphanumeric(char) && !strings.ContainsRune("_.-", char) {
			return false
		}
	}
	return true
}

func asciiAlphanumeric(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}
