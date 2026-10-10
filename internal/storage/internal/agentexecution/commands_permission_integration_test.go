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
			require.ErrorIs(t, u.Savepoint(t.Context(), func(_ *agentexecution.Unit) error {
				_, err := h.RecordPresentation(t.Context(), opened.ID, json.RawMessage(`{}`), receipt)
				return err
			}), storeerr.ErrUnauthorized)
			require.ErrorIs(t, u.Savepoint(t.Context(), func(_ *agentexecution.Unit) error {
				_, err := h.RecordPresentation(t.Context(), opened.ID, dest, json.RawMessage(`{}`))
				return err
			}), storeerr.ErrIdempotencyConflict)
			snapshot := assertCommandState(t, u, h)
			if mode == "allow" {
				require.Equal(t, agentexecution.WorkTool, snapshot.Selection.Work)
			}
			require.NoError(t, u.Commit(t.Context(), "permission"))
		})
	}
}

func TestExecutionRepairAndMetadata(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "metadata")
	changed, err := h.SetInteractionTarget(t.Context(), uuid.Nil, "", true)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = h.Repair(t.Context())
	require.NoError(t, err)
	assertCommandState(t, u, h)
	require.NoError(t, u.Commit(t.Context(), "repair metadata"))
	u, h = f.handle(t, a)
	_, err = u.DB().Exec(t.Context(), `DELETE FROM agent_execution_state WHERE agent_id=$1`, a)
	require.NoError(t, err)
	_, err = h.Repair(t.Context())
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "repair missing head"))
	u, h = f.handle(t, a)
	assertCommandState(t, u, h)
	require.NoError(t, u.ClearIntegrationTargets(t.Context(), f.project, uuid.New()))
	require.NoError(t, u.Commit(t.Context(), "empty integration cleanup"))
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
	_,
		err = u.DB().Exec(t.Context(),
		`UPDATE integration_targets SET deleted_at=statement_timestamp() WHERE id=$1`,
		target)
	require.NoError(t, err)
	require.NoError(t, u.ClearIntegrationTargets(t.Context(), f.project, integration))
	var selected *uuid.UUID
	var automatic bool
	require.NoError(t,
		u.DB().QueryRow(t.Context(),
			`SELECT interaction_target_id,interaction_auto_select FROM agents WHERE id=$1`,
			a).Scan(&selected,
			&automatic))
	require.Nil(t, selected)
	require.False(t, automatic)
	statements, roundTrips, err := agentexecution.CountCommit(t.Context(), u)
	require.NoError(t, err)
	require.Zero(t, statements)
	require.Zero(t, roundTrips)
}
