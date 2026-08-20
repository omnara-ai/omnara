package modelprovider

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	MinimumHostedAPITokenBytes     = 32
	hostedCredentialMaxSecretBytes = 16 * 1024
)

func ValidateHostedAPIToken(token string) error {
	if token == "" || token != strings.TrimSpace(token) {
		return errors.New("OMNARA_HOSTED_API_TOKEN is required and cannot have surrounding whitespace")
	}
	if len([]byte(token)) < MinimumHostedAPITokenBytes {
		return fmt.Errorf("OMNARA_HOSTED_API_TOKEN must contain at least %d bytes", MinimumHostedAPITokenBytes)
	}
	return nil
}

// ValidateHostedCredentialValue validates a hosted credential for storage.
func ValidateHostedCredentialValue(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("credential_value is required and cannot have surrounding whitespace")
	}
	if len(value) > hostedCredentialMaxSecretBytes {
		return errors.New("credential_value exceeds size limit")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("credential_value contains control characters")
		}
	}
	return nil
}
