package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestInteractionDismissalPreservesPromptAndStatus(t *testing.T) {
	form := interactionform.Form{
		Title:   "Question",
		Context: []interactionform.ContextItem{{Label: "Repository", Value: "owner/repo"}},
		Questions: []interactionform.Question{{
			Prompt: "Deploy?", Options: []interactionform.Option{{Label: "Yes"}, {Label: "No"}},
		}},
	}
	raw, err := json.Marshal(form)
	require.NoError(t, err)
	record := executionstore.AgentInteractionRecord{InteractionKind: executionstore.AgentInteractionKindQuestion,
		State: executionstore.AgentInteractionStateCanceled, Request: raw, ResolvedByInputID: uuid.New()}
	for _, limit := range []int{2000, 3000} {
		text := interactionClosedText(record, limit)
		for _, want := range []string{
			"Question", "Repository: owner/repo", "Deploy?", "Yes", "No", "Dismissed because a newer message was sent.",
		} {
			require.Contains(t, text, want)
		}
	}
	record.ResolvedByInputID = uuid.Nil
	require.True(t, strings.HasSuffix(interactionClosedText(record, 2000), "Dismissed."))
	record.State = executionstore.AgentInteractionStateResolved
	require.Equal(t,
		"Question\nRepository: owner/repo\nDeploy?\n• Yes\n• No\n\nAnswers recorded.",
		interactionClosedText(record, 2000),
	)
	record.State = executionstore.AgentInteractionStateCanceled
	form.Questions[0].Prompt = strings.Repeat("界", 4000)
	record.Request, err = json.Marshal(form)
	require.NoError(t, err)
	for _, limit := range []int{2000, 3000} {
		text := interactionClosedText(record, limit)
		require.True(t, utf8.ValidString(text))
		require.Equal(t, limit, utf8.RuneCountInString(text))
		require.True(t, strings.HasSuffix(text, "Dismissed."))
	}
}

func TestInteractionPromptTextDependsOnKind(t *testing.T) {
	t.Parallel()
	permissionForm, err := toolpermission.NewAllowDenyForm("Run command?", nil)
	require.NoError(t, err)
	for _, test := range []struct {
		kind          executionstore.AgentInteractionKind
		request       any
		text          bool
		slackBlocks   int
		discordAction string
	}{
		{
			kind: executionstore.AgentInteractionKindPermission,
			request: toolpermission.Request{
				Permission:    toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
				Authorization: toolpermission.Authorization{ToolName: "run_command", Input: json.RawMessage(`{}`)},
				Form:          permissionForm,
			},
			slackBlocks: 3, discordAction: "c1",
		},
		{
			kind: executionstore.AgentInteractionKindQuestion,
			request: interactionform.Form{Title: "Question", Questions: []interactionform.Question{{
				Prompt: "Ship?", Options: []interactionform.Option{{Label: "Yes"}, {Label: "Other", AllowsText: true}},
			}}},
			text: true, slackBlocks: 4, discordAction: "t1",
		},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(test.request)
			require.NoError(t, err)
			record := executionstore.AgentInteractionRecord{
				ID: uuid.New(), InteractionKind: test.kind, Request: raw,
			}
			form, err := interactionPromptForm(record)
			require.NoError(t, err)
			require.Equal(t, test.text, form.Questions[0].Options[1].AllowsText)
			payload, err := SlackInteractionPromptPayload(executionstore.InteractionDestination{
				IntegrationTargetID: uuid.New(),
				Address:             integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
			}, uuid.New(), record)
			require.NoError(t, err)
			var prompt struct {
				Blocks []json.RawMessage `json:"blocks"`
			}
			require.NoError(t, json.Unmarshal(payload, &prompt))
			require.Len(t, prompt.Blocks, test.slackBlocks)
			id, err := publicid.Encode(publicid.KindAgentInteraction, record.ID)
			require.NoError(t, err)
			_, rows := discord.InteractionFormPrompt(form, id)
			require.Len(t, rows, 1)
			require.Len(t, rows[0].Components, 2)
			button, err := discord.DecodeCustomID(rows[0].Components[1].CustomID)
			require.NoError(t, err)
			require.Equal(t, test.discordAction, button.Action)
			stored, err := record.Form()
			require.NoError(t, err)
			require.True(t, stored.Questions[0].Options[1].AllowsText, "presentation must preserve the canonical form")
		})
	}
}

func TestInteractionPreflightRetryBoundaries(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"database connection", fmt.Errorf("read secret: %w", &pgconn.PgError{Code: "08006"}), true},
		{"database restart", &pgconn.PgError{Code: "57P01"}, true},
		{"connection closed", io.EOF, true},
		{"Slack auth timeout", &slack.APIError{Result: slack.APIResult{DeliveryUnknown: true}}, true},
		{"short throttle", &slack.APIError{Result: slack.APIResult{RateLimited: true, RetryAfter: time.Second}}, true},
		{"long throttle", &slack.APIError{Result: slack.APIResult{RateLimited: true, RetryAfter: time.Minute}}, false},
		{"revoked", errors.Join(storeerr.ErrUnauthorized, io.EOF), false},
		{"closed interaction", storeerr.ErrStateTransitionConflict, false},
		{"bad credentials", &slack.APIError{Result: slack.APIResult{PermanentFailure: true}}, false},
		{"schema error", &pgconn.PgError{Code: "42703"}, false},
		{"invalid data", errors.New("invalid credential payload"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				err := retryInteractionPreflight(t.Context(), func() error { calls++; return test.err })
				require.ErrorIs(t, err, test.err)
				want := 1
				if test.retry {
					want = 3
				}
				require.Equal(t, want, calls)
			})
		})
	}
}

func TestInteractionPreflightRecoversAndHonorsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		require.NoError(t, retryInteractionPreflight(t.Context(), func() error {
			calls++
			if calls == 1 {
				return io.EOF
			}
			return nil
		}))
		require.Equal(t, 2, calls)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		calls = 0
		err := retryInteractionPreflight(ctx, func() error { calls++; return io.EOF })
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, 1, calls)
	})
}
