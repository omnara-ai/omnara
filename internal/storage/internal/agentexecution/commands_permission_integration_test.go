//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestExecutionPermissionAndPresentation(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "supersede", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			ref := toolCommand(t, h, lease, "built_in")
			form, err := toolpermission.NewAllowDenyForm("Permission", nil)
			require.NoError(t, err)
			permission := toolpermission.Request{
				Permission:    toolpermission.Selection{Mode: toolpermission.ModeAlwaysAsk},
				Authorization: toolpermission.Authorization{ToolName: "tool", Input: json.RawMessage(`{}`)},
				Form:          form,
			}
			dest := json.RawMessage(`{"target":"test"}`)
			opened, err := h.OpenInteraction(
				t.Context(),
				agentexecution.OpenInteractionInput{ToolRef: ref, Permission: &permission, Destination: dest},
			)
			require.NoError(t, err)
			assertCommandState(t, u, h)
			if mode == "cancel" {
				_, err = h.Cancel(
					t.Context(),
					agentexecution.CancelInput{Reason: "agent_canceled", Message: "canceled"},
				)
				require.NoError(t, err)
			} else if mode == "supersede" {
				received := receiveCommand(t, h, "steering", "supersede")
				ids, err := h.SupersedeInteractions(t.Context(), received.ID)
				require.NoError(t, err)
				require.Equal(t, []uuid.UUID{opened.ID}, ids)
			} else {
				option := toolpermission.AllowOptionIndex
				if mode == "deny" {
					option = toolpermission.DenyOptionIndex
				}
				input := agentexecution.ResolveInteractionInput{ID: opened.ID,
					Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{option}}}}}
				resolved, err := h.ResolveInteraction(t.Context(), input)
				require.NoError(t, err)
				replay, err := h.ResolveInteraction(t.Context(), input)
				require.NoError(t, err)
				require.Equal(t, resolved.InputID, replay.InputID)
			}
			receipt := json.RawMessage(`{"message":"sent"}`)
			changed, err := h.RecordPresentation(t.Context(), opened.ID, dest, receipt)
			require.NoError(t, err)
			require.True(t, changed)
			changed, err = h.RecordPresentation(t.Context(), opened.ID, dest, receipt)
			require.NoError(t, err)
			require.False(t, changed)
			require.NoError(t, u.Commit(t.Context(), "before rejected command"))
			u, h = f.handle(t, a)
			_, err = h.RecordPresentation(t.Context(), opened.ID, json.RawMessage(`{}`), receipt)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			require.NoError(t, u.Rollback(t.Context()))
			u, h = f.handle(t, a)
			_, err = h.RecordPresentation(t.Context(), opened.ID, dest, json.RawMessage(`{}`))
			require.NoError(t, err)
			require.NoError(t, u.Rollback(t.Context()))
			u, h = f.handle(t, a)
			snapshot := assertCommandState(t, u, h)
			var state string
			var outcome *string
			require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT c.state,r.outcome FROM tool_calls c
 LEFT JOIN tool_call_results r ON r.tool_call_id=c.id WHERE c.id=$1`, ref.ID).Scan(&state, &outcome))
			if mode == "allow" {
				require.Equal(t, agentexecution.WorkTool, snapshot.Selection.Work)
				require.Equal(t, "ready", state)
				require.Nil(t, outcome)
			} else {
				require.Equal(t, "completed", state)
				require.NotNil(t, outcome)
				if mode == "deny" {
					require.Equal(t, "denied", *outcome)
				} else {
					require.Equal(t, "canceled", *outcome)
				}
			}
			require.NoError(t, u.Commit(t.Context(), "permission"))
		})
	}
}

func TestExecutionIntegrationTargetCleanup(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	integration, target := uuid.New(), uuid.New()
	_, err := u.DB().Exec(t.Context(), `INSERT INTO integrations(id,org_id,project_id,name,integration_kind,settings,
 state,provider_tenant_id,provider_account_ref,credential_secret_id,installed_by_user_id,created_at,updated_at)
 VALUES($1,$2,$3,'test','slack_thread','{}','active','tenant','account',
 (SELECT id FROM secrets WHERE org_id=$2 LIMIT 1),(SELECT id FROM users LIMIT 1),
 statement_timestamp(),statement_timestamp())`, integration, f.org, f.project)
	require.NoError(t, err)
	_, err = u.DB().Exec(t.Context(), `INSERT INTO integration_targets(id,project_id,agent_id,integration_id,
 scope_kind,scope_ref,created_at,updated_at) VALUES($1,$2,$3,$4,'thread','C123:111.222',
 statement_timestamp(),statement_timestamp())`, target, f.project, a, integration)
	require.NoError(t, err)
	changed, err := h.SetInteractionTarget(t.Context(), target, "handler", false)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = u.DB().Exec(t.Context(),
		`UPDATE integration_targets SET deleted_at=statement_timestamp() WHERE id=$1`,
		target)
	require.NoError(t, err)
	require.NoError(t, u.ClearIntegrationTargets(t.Context(), f.project, integration))
	statements, roundTrips, err := agentexecution.CountCommit(t.Context(), u)
	require.NoError(t, err)
	require.Zero(t, statements)
	require.Zero(t, roundTrips)
}
