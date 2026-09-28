//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/stretchr/testify/require"
)

func (p *choiceTestProvider) NotifyLaunchUnavailable(_ context.Context, _ IntegrationLaunchContext) error {
	p.notices = append(p.notices, launchUnavailableMessage)
	return nil
}

func launcherObserver(t *testing.T, f *choiceJourney) executionstore.AgentRecord {
	t.Helper()
	launched, err := f.store.Execution().LaunchAgent(t.Context(), executionstore.LaunchAgentInput{
		ProjectID: f.ids.ProjectID, AgentConfigID: f.profiles[0].CurrentConfigID,
		LaunchedBy: identitystore.NewUserPrincipal(f.ids.ProviderAdminUserID),
	})
	require.NoError(t, err)
	createTestIntegrationSubscription(t, f.store, f.integration, launched.Agent.ID,
		`{"channel_id":"C123","thread_ts":"1.2"}`)
	return launched.Agent
}

func TestLauncherReceiveOnlyObserverDoesNotSuppressLaunch(t *testing.T) {
	f := newChoiceJourney(t, 1)
	observer := launcherObserver(t, f)
	results := f.receive("mention-with-observer", f.event)
	require.Len(t, results, 2)
	var launches, forwarded int
	for _, result := range results {
		if result.Launch != nil {
			launches++
			require.Equal(t, f.profiles[0].ID, result.Launch.Agent.AgentProfileID)
		} else {
			forwarded++
			require.NotNil(t, result.Input)
			require.Equal(t, observer.ID, result.Input.AgentInput.AgentID)
		}
	}
	require.Equal(t, 1, launches)
	require.Equal(t, 1, forwarded)
}

func TestLauncherDeletedProfilesFailClearlyWithoutNarrowingOrBlockingObservers(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("profiles=%d", count), func(t *testing.T) {
			f := newChoiceJourney(t, count)
			observer := launcherObserver(t, f)
			require.NoError(t, f.store.Execution().DeleteAgentProfile(t.Context(), f.ids.ProjectID, f.profiles[count-1].ID))
			results := f.receive("deleted-profile", f.event)
			require.Len(t, results, 1)
			require.Nil(t, results[0].Launch)
			require.NotNil(t, results[0].Input)
			require.Equal(t, observer.ID, results[0].Input.AgentInput.AgentID)
			require.Empty(t, f.provider.menus)
			require.Equal(t, []string{launchUnavailableMessage}, f.provider.notices)
		})
	}
}

func TestLauncherMissingIntentProfileReportsUnavailable(t *testing.T) {
	f := newChoiceJourney(t, 1)
	f.consumer.launchers.launchers[integrationdefinition.SlackThread] = func(
		_ context.Context, input IntegrationLaunchContext,
	) ([]IntegrationLaunchIntent, error) {
		return []IntegrationLaunchIntent{{
			IntegrationID: input.Integration.ID, ProfileID: f.profiles[0].ID,
			LaunchKey: integrationdefinition.ProfileLaunchKey,
		}}, nil
	}
	require.NoError(t, f.store.Execution().DeleteAgentProfile(t.Context(), f.ids.ProjectID, f.profiles[0].ID))
	require.Empty(t, f.receive("missing-intent-profile", f.event))
	require.Equal(t, []string{launchUnavailableMessage}, f.provider.notices)
}

func TestLauncherArchivedOwnerWithLegacyKeyPreventsRespawn(t *testing.T) {
	f := newChoiceJourney(t, 1)
	results := f.receive("first-mention", f.event)
	require.Len(t, results, 1)
	owner := results[0].Launch.Agent
	_, err := f.pool.Exec(t.Context(),
		`INSERT INTO org_memberships(org_id,user_id,role,created_at)
 VALUES($1,$2,'owner',now()) ON CONFLICT DO NOTHING`, f.ids.OrgID, f.ids.ProviderAdminUserID)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(),
		`UPDATE integration_targets SET launch_key='legacy-selection' WHERE agent_id=$1`, owner.ID)
	require.NoError(t, err)
	_, _, err = f.store.Execution().ArchiveAgent(t.Context(), f.ids.ProjectID, owner.ID,
		identitystore.NewUserPrincipal(f.ids.ProviderAdminUserID))
	require.NoError(t, err)
	// A deleted profile is permitted and cannot erase saved conversation ownership.
	require.NoError(t, f.store.Execution().DeleteAgentProfile(t.Context(), f.ids.ProjectID, f.profiles[0].ID))
	event := f.event
	event.SemanticKey = "later-mention"
	event.ContentBlocks = json.RawMessage(`[{"type":"text","text":"please continue"}]`)
	require.Empty(t, f.receive("later-mention", event))
	require.Empty(t, f.provider.notices)
	var agents int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Equal(t, 1, agents)
}

func TestLauncherLateSiblingFindsLegacyOwnerWithoutSubscriptionOrLiveProfile(t *testing.T) {
	f := newChoiceJourney(t, 2)
	f.event.Sibling = &executionstore.InboxMessageSibling{Key: "late-files"}
	require.Empty(t, f.receive("mention", f.event))
	f.choose(f.provider.menus[0], "heavy")
	results, err := f.consumer.Consume(t.Context(), f.claim().Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	owner := results[0].Launch.Agent
	removeTestAgentSubscriptions(t, f.store, f.integration, owner.ID)
	_, err = f.pool.Exec(t.Context(),
		`UPDATE integration_targets SET launch_key='legacy-selection' WHERE agent_id=$1`, owner.ID)
	require.NoError(t, err)
	require.NoError(t, f.store.Execution().DeleteAgentProfile(t.Context(), f.ids.ProjectID, owner.AgentProfileID))
	content, artifactID := []byte("original attachment"), uuid.New()
	event := f.event
	event.SemanticKey = "late-files"
	event.Sibling = &executionstore.InboxMessageSibling{Key: f.event.SemanticKey, AttachmentNotice: "Original files"}
	event.Files = []executionstore.InboxPlannedFile{{
		ArtifactID: artifactID, ProviderFileID: "F123", Expected: &artifactstore.PreparedArtifact{
			ID: artifactID, ContentType: "text/plain", Filename: "request.txt",
			SizeBytes: int64(len(content)), Digest: blobstore.ContentDigest(content),
		}}}
	event.ContentBlocks = json.RawMessage(fmt.Sprintf(`[{"type":"media_ref","artifact_id":%q}]`, artifactID.String()))
	f.provider.file = IntegrationInboxFile{Content: content, ContentType: "text/plain", Filename: "request.txt"}
	results = f.receive("late-files", event)
	require.Len(t, results, 1)
	require.Nil(t, results[0].Launch)
	require.NotNil(t, results[0].Input)
	require.True(t, results[0].Input.Created)
	require.Equal(t, owner.ID, results[0].Input.AgentInput.AgentID)
	results = f.receive("replayed-late-files", event)
	require.Len(t, results, 1)
	require.False(t, results[0].Input.Created)
	require.Empty(t, f.provider.notices)
}
