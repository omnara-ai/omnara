//go:build integration

package appstore_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/stretchr/testify/require"
)

func TestConversationDisplayNameReusesLatestLiveLabelAcrossAgents(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	execution := executionstore.New(f.pool, executionstore.Config{})
	var configID uuid.UUID
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`, f.project).Scan(&configID))
	address := appstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	read := func(project, appID uuid.UUID, address appstore.ConversationAddress) string {
		t.Helper()
		name, err := f.store.GetConversationDisplayName(f.ctx, project, appID, address)
		require.NoError(t, err)
		return name
	}
	require.Empty(t, read(f.project, f.appID, address))
	var targets []uuid.UUID
	for index, name := range []string{"old", "latest", "", "retired"} {
		launch, err := execution.LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
			ProjectID: f.project, AgentConfigID: configID, LaunchedBy: identitystore.NewUserPrincipal(f.user),
		})
		require.NoError(t, err)
		targetID := uuid.Must(uuid.NewV7())
		targets = append(targets, targetID)
		updated := time.Date(2026, 9, 18, 1, index, 0, 0, time.UTC)
		var deleted *time.Time
		if name == "retired" {
			deleted = &updated
		}
		f.exec(t, `INSERT INTO app_targets
 (id,project_id,agent_id,app_id,provider_ref_kind,provider_ref,
  display_name,created_at,updated_at,deleted_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9)`,
			targetID, f.project, launch.Agent.ID, f.appID, address.Kind, address.Ref,
			name, updated, deleted)
	}
	require.Equal(t, "latest", read(f.project, f.appID, address))
	require.Empty(t, read(uuid.New(), f.appID, address))
	require.Empty(t, read(f.project, uuid.New(), address))
	require.Empty(t, read(f.project, f.appID, appstore.ConversationAddress{Kind: "dm", Ref: address.Ref}))
	require.Empty(
		t,
		read(f.project, f.appID, appstore.ConversationAddress{Kind: "thread", Ref: "C456:1.2"}),
	)
	f.exec(t, `UPDATE app_targets SET deleted_at=now() WHERE id=$1`, targets[1])
	require.Equal(t, "old", read(f.project, f.appID, address))
	f.exec(t, `UPDATE app_targets SET deleted_at=now() WHERE id=$1`, targets[0])
	require.Empty(t, read(f.project, f.appID, address))
}
