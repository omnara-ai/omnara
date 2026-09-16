package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const integrationSetupSessionPurpose = "integration-setup-session"

// A browser's temporary provider authorization exists only to finish setup.
// Encrypt it in the existing Redis service; never put it in a callback URL,
// installation credential, gateway configuration, or durable integration row.
type integrationSetupSession struct {
	State     integrationOAuthState `json:"state"`
	UserToken string                `json:"user_token"`
}

func (s *Server) saveIntegrationSetupSession(ctx context.Context, session integrationSetupSession) error {
	if s.integrationSetupRedis == nil || s.secretKeyWrapper == nil {
		return errors.New("integration setup session storage is unavailable")
	}
	if err := validateIntegrationOAuthState(session.State, time.Now().UTC()); err != nil {
		return err
	}
	if session.UserToken == "" || len(session.UserToken) > 8192 {
		return errors.New("invalid provider setup authorization")
	}
	body, err := json.Marshal(session)
	if err != nil {
		return err
	}
	sealed, err := secrets.SealToken(ctx, s.secretKeyWrapper, integrationSetupSessionPurpose, body)
	if err != nil {
		return err
	}
	ttl := time.Until(session.State.ExpiresAt)
	if ttl <= 0 || ttl > integrationOAuthStateTTL {
		return storeerr.ErrUnauthorized
	}
	created, err := s.integrationSetupRedis.SetNX(ctx, integrationSetupSessionKey(session.State.FlowID), sealed, ttl)
	if err != nil {
		return err
	}
	if !created {
		return storeerr.ErrIntegrationOAuthFlowConsumed
	}
	return nil
}

// Consume after provider verification, immediately before final persistence.
// A failed completion asks the user to reconnect, like a failed OAuth exchange;
// no authorization is reused for a second repository selection.
func (s *Server) loadIntegrationSetupSession(
	ctx context.Context, projectID, userID, flowID uuid.UUID, consume bool,
) (integrationSetupSession, error) {
	var session integrationSetupSession
	if s.integrationSetupRedis == nil || s.secretKeyWrapper == nil {
		return session, errors.New("integration setup session storage is unavailable")
	}
	if projectID == uuid.Nil || userID == uuid.Nil || flowID == uuid.Nil {
		return session, storeerr.ErrUnauthorized
	}
	key := integrationSetupSessionKey(flowID)
	raw, found, err := s.integrationSetupRedis.GetBytes(ctx, key)
	if err != nil {
		return session, err
	}
	if !found || len(raw) > 24*1024 {
		return session, storeerr.ErrUnauthorized
	}
	plaintext, err := secrets.OpenToken(ctx, s.secretKeyWrapper, integrationSetupSessionPurpose, string(raw))
	if err != nil || httpjson.DecodeStrictRequiredBytes(plaintext, &session) != nil ||
		validateIntegrationOAuthState(session.State, time.Now().UTC()) != nil ||
		session.State.FlowID != flowID || session.State.ProjectID != projectID || session.State.InstalledByUserID != userID {
		return integrationSetupSession{}, storeerr.ErrUnauthorized
	}
	if consume {
		// Verify ownership before deleting and compare the exact encrypted value
		// atomically so concurrent completions cannot consume the same session.
		_, consumed, err := s.integrationSetupRedis.EvalBytes(ctx, consumeIntegrationSetupSession, []string{key}, raw)
		if err != nil {
			return integrationSetupSession{}, err
		}
		if !consumed {
			return integrationSetupSession{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
	}
	return session, nil
}

const consumeIntegrationSetupSession = `
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then return nil end
redis.call('DEL', KEYS[1])
return current
`

func integrationSetupSessionKey(flowID uuid.UUID) string {
	return "integration-setup:" + flowID.String()
}
