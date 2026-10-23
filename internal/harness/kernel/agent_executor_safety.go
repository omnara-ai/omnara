package kernel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func (e AgentExecutor) safetyIdentifier(ctx context.Context, input ModelWorkExecution) (string, error) {
	if len(e.SafetyIdentifierKey) == 0 {
		return "", nil
	}
	principal, err := e.Store.Execution().SafetyPrincipal(ctx, input.ProjectID, input.AgentID, input.InputIDs)
	if err != nil {
		return "", err
	}
	if principal.Kind == publicid.KindProject && e.StreamLog != nil {
		e.StreamLog.WarnContext(ctx, "model request has no responsible actor",
			"project_id", input.ProjectID, "agent_id", input.AgentID)
	}
	mac := hmac.New(sha256.New, e.SafetyIdentifierKey)
	_, _ = mac.Write([]byte(string(principal.Kind) + ":" + principal.ID.String()))
	return hex.EncodeToString(mac.Sum(nil)[:16]), nil
}
