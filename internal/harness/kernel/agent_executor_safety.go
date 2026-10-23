package kernel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

func (e AgentExecutor) safetyIdentifier(orgID uuid.UUID) string {
	if len(e.SafetyIdentifierKey) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, e.SafetyIdentifierKey)
	_, _ = mac.Write(orgID[:])
	return hex.EncodeToString(mac.Sum(nil)[:16])
}
