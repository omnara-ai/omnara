//go:build integration

package integration

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/stretchr/testify/require"
)

func TestDiscordIdentifyPermitCoordinatesWorkersAndDoesNotResetBudget(t *testing.T) {
	redis := integrationredis.OpenClient(t)
	prefix := "test:discord:{" + uuid.NewString() + "}:"
	var successes atomic.Int32
	var group sync.WaitGroup
	failures := make(chan error, 12)
	for i := range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			bucket := "a"
			if i%2 == 1 {
				bucket = "b"
			}
			wait, err := redis.EvalInt(
				t.Context(),
				discordIdentifyPermitScript,
				[]string{prefix + "budget", prefix + bucket},
				4,
				60000,
			)
			if err != nil {
				failures <- err
				return
			}
			if wait == 0 {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Equal(t, int32(2), successes.Load(), "one IDENTIFY per concurrency bucket, across workers")
	remaining, found, err := redis.GetBytes(t.Context(), prefix+"budget")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "2", string(remaining))
	// Model a later spacing window without sleeping. A stale larger provider
	// observation must not replenish the already consumed bot-wide budget.
	_, err = redis.EvalInt(
		t.Context(),
		`redis.call('DEL',KEYS[1],KEYS[2]);return 0`,
		[]string{prefix + "a", prefix + "b"},
	)
	require.NoError(t, err)
	wait, err := redis.EvalInt(
		t.Context(),
		discordIdentifyPermitScript,
		[]string{prefix + "budget", prefix + "a"},
		4,
		60000,
	)
	require.NoError(t, err)
	require.Zero(t, wait)
	wait, err = redis.EvalInt(
		t.Context(),
		discordIdentifyPermitScript,
		[]string{prefix + "budget", prefix + "b"},
		4,
		60000,
	)
	require.NoError(t, err)
	require.Positive(t, wait)
	remaining, _, err = redis.GetBytes(t.Context(), prefix+"budget")
	require.NoError(t, err)
	require.Equal(t, "1", string(remaining), "never consume the final provider start")
	// A fresh provider observation after Redis data loss still blocks starts if
	// the provider reports that another client has exhausted the daily quota.
	_, err = redis.EvalInt(t.Context(), `redis.call('DEL',KEYS[1]);return 0`, []string{prefix + "budget"})
	require.NoError(t, err)
	wait, err = redis.EvalInt(
		t.Context(),
		discordIdentifyPermitScript,
		[]string{prefix + "budget", prefix + "b"},
		0,
		60000,
	)
	require.NoError(t, err)
	require.Positive(t, wait)
	// Zero is not excluded by Discord's reset_after contract. It must not
	// prevent a fresh bot from connecting, nor immediately erase its budget.
	wait, err = redis.EvalInt(
		t.Context(),
		discordIdentifyPermitScript,
		[]string{prefix + "fresh", prefix + "fresh-bucket"},
		1000,
		0,
	)
	require.NoError(t, err)
	require.Zero(t, wait)
	ttl, err := redis.EvalInt(t.Context(), `return redis.call('PTTL',KEYS[1])`, []string{prefix + "fresh"})
	require.NoError(t, err)
	require.Greater(t, ttl, 86000000)
}
