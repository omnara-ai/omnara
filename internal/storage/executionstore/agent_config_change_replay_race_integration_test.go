//go:build integration

package executionstore_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type configChangeLockPause struct {
	once    sync.Once
	reached chan struct{}
	resume  chan struct{}
}

func (p *configChangeLockPause) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "-- name: LockAgentMachineSources") {
		p.once.Do(func() {
			close(p.reached)
			select {
			case <-p.resume:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}

func (p *configChangeLockPause) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestChangeAgentConfigReplayAfterInterveningChange(t *testing.T) {
	f := newIntegrationActivationFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	launch, err := f.store.Execution().LaunchAgent(ctx, f.launchInput(f.profile.CurrentConfigID, "replay-race-base"))
	require.NoError(t, err)
	input := f.changeInput(t, launch.Agent.ID, "first edit", "replay-race-same-key")
	input.ExpectedCurrentConfigID = launch.Agent.CurrentConfigID
	pause := &configChangeLockPause{reached: make(chan struct{}), resume: make(chan struct{})}
	config := f.store.pool.Config()
	config.ConnConfig.Tracer = pause
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer pool.Close()
	duplicate := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
		return newSecretIntegrationStore(pool).Execution().ChangeAgentConfig(ctx, input)
	})
	select {
	case <-pause.reached:
	case <-ctx.Done():
		t.Fatal("duplicate never reached the config change lock", ctx.Err())
	}
	original, err := f.store.Execution().ChangeAgentConfig(ctx, input)
	require.NoError(t, err)
	next := f.changeInput(t, launch.Agent.ID, "second edit", "replay-race-next-key")
	next.ExpectedCurrentConfigID = original.AgentConfig.ID
	_, err = f.store.Execution().ChangeAgentConfig(ctx, next)
	require.NoError(t, err)
	close(pause.resume)
	result := integrationdb.Await(t, duplicate, "duplicate after intervening config change")
	require.NoError(t, result.Err)
	require.Equal(t, original.AgentConfig.ID, result.Value.AgentConfig.ID)
	require.Equal(t, original.ConfigChange.AgentInput.ID, result.Value.ConfigChange.AgentInput.ID)
	require.Equal(t, original.ConfigChange.Event.ID, result.Value.ConfigChange.Event.ID)
}
