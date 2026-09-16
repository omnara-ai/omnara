//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSetupSessionEncryptedAndOwnerScoped(t *testing.T) {
	t.Parallel()
	server, session := integrationSetupSessionFixture(t)
	ctx, state := t.Context(), session.State
	require.NoError(t, server.saveIntegrationSetupSession(ctx, session))
	key := integrationSetupSessionKey(state.FlowID)
	raw, found, err := server.integrationSetupRedis.GetBytes(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	require.NotContains(t, string(raw), session.UserToken)
	require.NotContains(t, string(raw), state.CodeVerifier)
	require.False(t, json.Valid(raw), "Redis must contain a sealed token, not a plaintext session")
	ttl, err := server.integrationSetupRedis.EvalInt(ctx, `return redis.call('PTTL', KEYS[1])`, []string{key})
	require.NoError(t, err)
	require.Positive(t, ttl)
	require.LessOrEqual(t, ttl, int(integrationOAuthStateTTL.Milliseconds()))
	_, err = secrets.OpenToken(ctx, server.secretKeyWrapper, integrationOAuthStatePurpose, string(raw))
	require.Error(t, err, "a setup session cannot be used as callback state")

	for _, scope := range [][3]uuid.UUID{
		{uuid.New(), state.InstalledByUserID, state.FlowID},
		{state.ProjectID, uuid.New(), state.FlowID},
		{state.ProjectID, state.InstalledByUserID, uuid.New()},
		{uuid.Nil, state.InstalledByUserID, state.FlowID},
	} {
		_, err := server.loadIntegrationSetupSession(ctx, scope[0], scope[1], scope[2], true)
		require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	}
	read, err := server.loadIntegrationSetupSession(ctx, state.ProjectID, state.InstalledByUserID, state.FlowID, false)
	require.NoError(t, err)
	require.Equal(t, session.UserToken, read.UserToken)
	require.Equal(t, state.CodeVerifier, read.State.CodeVerifier)
	require.Equal(t, state.ProjectID, read.State.ProjectID)
	other := session
	other.UserToken = "ghu_replacement-must-not-win"
	require.ErrorIs(t, server.saveIntegrationSetupSession(ctx, other), storeerr.ErrIntegrationOAuthFlowConsumed)
	unchanged, found, err := server.integrationSetupRedis.GetBytes(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, raw, unchanged, "duplicate callbacks must not overwrite the original authorization")
}

func TestIntegrationSetupSessionConsumeHasOneWinner(t *testing.T) {
	t.Parallel()
	server, session := integrationSetupSessionFixture(t)
	ctx, state := t.Context(), session.State
	require.NoError(t, server.saveIntegrationSetupSession(ctx, session))
	const consumers = 8
	results := make(chan error, consumers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range consumers {
		workers.Go(func() {
			<-start
			_, err := server.loadIntegrationSetupSession(ctx, state.ProjectID, state.InstalledByUserID, state.FlowID, true)
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var winners int
	for err := range results {
		if err == nil {
			winners++
		} else {
			require.True(t, errors.Is(err, storeerr.ErrUnauthorized) || errors.Is(err, storeerr.ErrIntegrationOAuthFlowConsumed),
				"losers must observe either the deleted session or the failed atomic compare-and-delete: %v", err)
		}
	}
	require.Equal(t, 1, winners)
	_, err := server.loadIntegrationSetupSession(ctx, state.ProjectID, state.InstalledByUserID, state.FlowID, false)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
}

func TestIntegrationSetupSessionRejectsInvalidStoredAuthorization(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"expired", "wrong_flow", "wrong_purpose", "plaintext", "oversized", "tampered"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			server, session := integrationSetupSessionFixture(t)
			ctx, state := t.Context(), session.State
			purpose := integrationSetupSessionPurpose
			if test == "expired" {
				session.State.ExpiresAt = time.Now().Add(-time.Minute)
			}
			if test == "wrong_flow" {
				session.State.FlowID = uuid.New()
			}
			if test == "wrong_purpose" {
				purpose = integrationOAuthStatePurpose
			}
			body, err := json.Marshal(session)
			require.NoError(t, err)
			sealed, err := secrets.SealToken(ctx, server.secretKeyWrapper, purpose, body)
			require.NoError(t, err)
			switch test {
			case "plaintext":
				sealed = string(body)
			case "oversized":
				sealed = strings.Repeat("x", 24*1024+1)
			case "tampered":
				sealed = "corrupt" + sealed
			}
			key := integrationSetupSessionKey(state.FlowID)
			require.NoError(t, server.integrationSetupRedis.Set(ctx, key, sealed, time.Minute))
			_, err = server.loadIntegrationSetupSession(ctx, state.ProjectID, state.InstalledByUserID, state.FlowID, true)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
		})
	}
}

func TestIntegrationSetupSessionExpiresAndRejectsInvalidSave(t *testing.T) {
	t.Parallel()
	server, session := integrationSetupSessionFixture(t)
	ctx, state := t.Context(), session.State
	for _, expires := range []time.Time{time.Now().Add(-time.Second), time.Now().Add(2 * integrationOAuthStateTTL)} {
		invalid := session
		invalid.State.ExpiresAt = expires
		require.Error(t, server.saveIntegrationSetupSession(ctx, invalid))
	}
	for _, token := range []string{"", strings.Repeat("x", 8193)} {
		invalid := session
		invalid.UserToken = token
		require.Error(t, server.saveIntegrationSetupSession(ctx, invalid))
	}
	_, found, err := server.integrationSetupRedis.GetBytes(ctx, integrationSetupSessionKey(state.FlowID))
	require.NoError(t, err)
	require.False(t, found)
	session.State.ExpiresAt = time.Now().Add(time.Second)
	require.NoError(t, server.saveIntegrationSetupSession(ctx, session))
	require.Eventually(t, func() bool {
		_, found, err := server.integrationSetupRedis.GetBytes(ctx, integrationSetupSessionKey(state.FlowID))
		return err == nil && !found
	}, 3*time.Second, 20*time.Millisecond, "Redis expires the encrypted authorization without a completion request")
	_, err = server.loadIntegrationSetupSession(ctx, state.ProjectID, state.InstalledByUserID, state.FlowID, true)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
}

func integrationSetupSessionFixture(t *testing.T) (*Server, integrationSetupSession) {
	t.Helper()
	server := &Server{integrationSetupRedis: integrationredis.OpenClient(t), secretKeyWrapper: integrationKeyWrapper()}
	session := integrationSetupSession{
		State: integrationOAuthState{
			FlowID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(), InstalledByUserID: uuid.New(),
			IntegrationAppID: uuid.New(), AppConfigurationRevision: 1, Provider: "github",
			CodeVerifier: strings.Repeat("v", 43), ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		},
		UserToken: "ghu_private-test-authorization",
	}
	t.Cleanup(func() {
		key := integrationSetupSessionKey(session.State.FlowID)
		_, _, _ = server.integrationSetupRedis.GetDelBytes(context.Background(), key)
	})
	return server, session
}
