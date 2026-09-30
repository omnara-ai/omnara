//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestStateWorkIsInterpretedByItsIntegration(t *testing.T) {
	for _, registered := range []bool{true, false} {
		t.Run(fmt.Sprintf("registered=%t", registered), func(t *testing.T) {
			f := newChoiceJourney(t, 1)
			ctx := t.Context()
			stateID := uuid.New()
			_, err := f.pool.Exec(ctx, `INSERT INTO integration_states(id,project_id,integration_id,kind,key,data)
 VALUES($1,$2,$3,'counter','daily','{"count":1}')`, stateID, f.ids.ProjectID, f.integrationSetup.ID)
			require.NoError(t, err)
			_, err = f.pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,integration_id,receipt_key,source,source_state_id)
 VALUES($1,$2,'counter:daily','state',$3)`, f.ids.ProjectID, f.integrationSetup.ID, stateID)
			require.NoError(t, err)
			receipt := f.claim()
			calls := 0
			f.consumer.stateHandlers = map[integrationdefinition.Kind]IntegrationStateHandler{
				integrationdefinition.SlackThread: func(_ context.Context, got integrationstore.IntegrationInboxRecord,
					integration integrationstore.IntegrationRecord, process IntegrationStateProcess,
				) ([]IntegrationRecipientAdmission, error) {
					calls++
					require.Equal(t, stateID, got.SourceStateID)
					require.Equal(t, f.integrationSetup.ID, integration.ID)
					return process(nil, nil)
				},
			}
			if !registered {
				f.consumer.stateHandlers = nil
			}
			outcomes, err := f.consumer.Consume(ctx, receipt.Lease())
			if !registered {
				require.ErrorIs(t, err, ErrIntegrationInboundPermanent)
				require.Zero(t, calls)
				return
			}
			require.NoError(t, err)
			require.Empty(t, outcomes)
			require.Equal(t, 1, calls)
			saved, err := f.store.Integrations().GetIntegrationInbox(ctx, receipt.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, saved.State)
			require.Empty(t, f.provider.menus)
		})
	}
}
