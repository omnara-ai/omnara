package tools

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/stretchr/testify/require"
)

func TestBackgroundExecutionRunnerQueuesWorkAtCapacity(t *testing.T) {
	metricSet := metrics.New()
	ctx, cancel := context.WithCancel(t.Context())
	runner, err := NewBackgroundExecutionRunner(ctx, slog.Default(), 1, metricSet)
	if err != nil {
		t.Fatalf("new background runner: %v", err)
	}
	defer runner.Shutdown()
	defer cancel()

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	if !runner.Submit("first", func(ctx context.Context) error {
		close(firstStarted)
		select {
		case <-firstRelease:
		case <-ctx.Done():
		}
		return nil
	}) {
		t.Fatal("first background task was rejected")
	}
	<-firstStarted
	require.Contains(t, scrapeBackgroundMetrics(t, metricSet), "omnara_background_execution_queue_depth 0\n")

	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	if !runner.TrySubmit("second", func(ctx context.Context) error {
		close(secondStarted)
		select {
		case <-secondRelease:
		case <-ctx.Done():
		}
		return nil
	}) {
		t.Fatal("second background task was not queued")
	}
	body := scrapeBackgroundMetrics(t, metricSet)
	require.Contains(t, body, "omnara_background_execution_queue_capacity 1\n")
	require.Contains(t, body, "omnara_background_execution_queue_depth 1\n")
	require.Contains(t, body, "omnara_background_execution_submissions_total{result=\"accepted\"} 2\n")
	require.Contains(t, body, "omnara_background_execution_start_delay_seconds_count 1\n")

	optionalAccepted := make(chan bool, 1)
	go func() {
		optionalAccepted <- runner.TrySubmit(
			"optional",
			func(context.Context) error { return nil },
		)
	}()
	select {
	case accepted := <-optionalAccepted:
		if accepted {
			t.Fatal("optional background task was accepted while the queue was full")
		}
	case <-time.After(time.Second):
		t.Fatal("optional background submission blocked on queue capacity")
	}
	require.Contains(t, scrapeBackgroundMetrics(t, metricSet),
		"omnara_background_execution_submissions_total{result=\"full\"} 1\n")

	thirdStarted := make(chan struct{})
	thirdAccepted := make(chan bool, 1)
	go func() {
		thirdAccepted <- runner.Submit("third", func(context.Context) error {
			close(thirdStarted)
			return nil
		})
	}()
	require.Eventually(t, func() bool {
		return strings.Contains(scrapeBackgroundMetrics(t, metricSet),
			"omnara_background_execution_submissions_in_flight 1\n")
	}, time.Second, time.Millisecond)
	select {
	case accepted := <-thirdAccepted:
		t.Fatalf("third submission returned while the bounded queue was full: accepted=%v", accepted)
	case <-time.After(25 * time.Millisecond):
	}

	close(firstRelease)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("queued second task did not start")
	}
	select {
	case accepted := <-thirdAccepted:
		if !accepted {
			t.Fatal("third background task was dropped")
		}
	case <-time.After(time.Second):
		t.Fatal("third submission did not proceed when queue space became available")
	}
	select {
	case <-thirdStarted:
		t.Fatal("runner exceeded its execution concurrency")
	default:
	}
	close(secondRelease)
	select {
	case <-thirdStarted:
	case <-time.After(time.Second):
		t.Fatal("queued third task did not start")
	}
	body = scrapeBackgroundMetrics(t, metricSet)
	require.Contains(t, body, "omnara_background_execution_queue_depth 0\n")
	require.Contains(t, body, "omnara_background_execution_submissions_in_flight 0\n")
	require.Contains(t, body, "omnara_background_execution_submissions_total{result=\"accepted\"} 3\n")
	require.Contains(t, body, "omnara_background_execution_submission_duration_seconds_count 4\n")
	require.Contains(t, body, "omnara_background_execution_start_delay_seconds_count 3\n")
}

func TestBackgroundExecutionRunnerShutdownRunsAcceptedWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		metricSet := metrics.New()
		runner, err := NewBackgroundExecutionRunner(t.Context(), slog.Default(), 1, metricSet)
		require.NoError(t, err)
		release := make(chan struct{})
		ran := make(chan string, 3)
		require.True(t, runner.Submit("running", func(ctx context.Context) error {
			select {
			case <-release:
			case <-ctx.Done():
			}
			ran <- fmt.Sprintf("running: %v", ctx.Err())
			return nil
		}))
		require.True(t, runner.Submit("queued", func(ctx context.Context) error {
			ran <- fmt.Sprintf("queued: %v", ctx.Err())
			return nil
		}))
		waiting := make(chan bool, 1)
		go func() {
			waiting <- runner.Submit("waiting", func(context.Context) error {
				ran <- "waiting"
				return nil
			})
		}()
		synctest.Wait()

		shutdownDone := make(chan struct{})
		go func() {
			runner.Shutdown()
			close(shutdownDone)
		}()
		require.False(t, <-waiting)
		require.False(t, runner.TrySubmit("late", func(context.Context) error { return nil }))
		close(release)
		<-shutdownDone
		close(ran)
		require.Equal(t, []string{"running: <nil>", "queued: <nil>"}, collect(ran))
		body := scrapeBackgroundMetrics(t, metricSet)
		require.Contains(t, body, "omnara_background_execution_submissions_total{result=\"accepted\"} 2\n")
		require.Contains(t, body, "omnara_background_execution_submissions_total{result=\"shutdown\"} 2\n")
	})
}

func TestBackgroundExecutionRunnerCancellationStopsWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		metricSet := metrics.New()
		ctx, cancel := context.WithCancel(t.Context())
		runner, err := NewBackgroundExecutionRunner(ctx, slog.Default(), 1, metricSet)
		require.NoError(t, err)
		ran := make(chan string, 3)
		require.True(t, runner.Submit("running", func(ctx context.Context) error {
			<-ctx.Done()
			ran <- fmt.Sprintf("running: %v", ctx.Err())
			return ctx.Err()
		}))
		require.True(t, runner.Submit("queued", func(context.Context) error {
			ran <- "queued"
			return nil
		}))
		waiting := make(chan bool, 1)
		go func() {
			waiting <- runner.Submit("waiting", func(context.Context) error {
				ran <- "waiting"
				return nil
			})
		}()
		synctest.Wait()

		cancel()
		require.False(t, <-waiting)
		runner.Shutdown()
		close(ran)
		require.Equal(t, []string{"running: context canceled"}, collect(ran))
		require.False(t, runner.Submit("late", func(context.Context) error { return nil }))
		require.False(t, runner.TrySubmit("late", func(context.Context) error { return nil }))
		body := scrapeBackgroundMetrics(t, metricSet)
		require.Contains(t, body, "omnara_background_execution_submissions_total{result=\"accepted\"} 2\n")
		require.Contains(t, body, "omnara_background_execution_submissions_total{result=\"shutdown\"} 3\n")
	})
}

func collect(values <-chan string) []string {
	var collected []string
	for value := range values {
		collected = append(collected, value)
	}
	return collected
}

func scrapeBackgroundMetrics(t *testing.T, set *metrics.Set) string {
	t.Helper()
	response := httptest.NewRecorder()
	set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil))
	require.Equal(t, http.StatusOK, response.Code)
	return response.Body.String()
}
