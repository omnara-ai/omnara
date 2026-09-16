package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type integrationControlMaintenanceStub struct {
	calls       []string
	failLimit   int
	deleteInput integrationstore.DeleteRetainedIntegrationControlsInput
	failed      int64
	deleted     int64
	failErr     error
	deleteErr   error
	oldest      *time.Time
	oldestErr   error
}

func (s *integrationControlMaintenanceStub) FailUnprocessableIntegrationControls(
	_ context.Context, limit int,
) (int64, error) {
	s.calls = append(s.calls, "fail")
	s.failLimit = limit
	return s.failed, s.failErr
}

func (s *integrationControlMaintenanceStub) DeleteRetainedIntegrationControls(
	_ context.Context, input integrationstore.DeleteRetainedIntegrationControlsInput,
) (int64, error) {
	s.calls = append(s.calls, "delete")
	s.deleteInput = input
	return s.deleted, s.deleteErr
}

func (s *integrationControlMaintenanceStub) OldestPendingIntegrationControl(_ context.Context) (*time.Time, error) {
	s.calls = append(s.calls, "observe")
	return s.oldest, s.oldestErr
}

func TestIntegrationControlMaintenanceIsBoundedAndUsesConfiguredRetention(t *testing.T) {
	oldest := time.Unix(1_700_000_000, 0)
	store := &integrationControlMaintenanceStub{
		failed: integrationControlBatchSize, deleted: integrationControlBatchSize, oldest: &oldest,
	}
	set := metrics.New()
	recorder := metrics.NewIntegrationControlRecorder(set)
	retention := 37*time.Hour + time.Second
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	worked, err := runIntegrationControlMaintenanceTick(t.Context(), log, store, recorder, retention)
	if err != nil || !worked {
		t.Fatalf("tick = (%t, %v), want (true, nil)", worked, err)
	}
	if !slices.Equal(store.calls, []string{"fail", "delete", "observe"}) {
		t.Fatalf("calls = %v, want one bounded pass even when batches are full", store.calls)
	}
	if store.failLimit != 1000 || store.deleteInput.Limit != 1000 || store.deleteInput.Retention != retention {
		t.Fatalf("fail limit = %d, deletion input = %+v", store.failLimit, store.deleteInput)
	}
	if got := controlGauge(t, set, "oldest_unapplied"); got != float64(oldest.Unix()) {
		t.Fatalf("oldest timestamp = %v, want %v", got, oldest.Unix())
	}
	if got := controlGauge(t, set, "last_observation"); got <= 0 {
		t.Fatalf("successful observation timestamp = %v, want positive", got)
	}
}

func TestIntegrationControlMaintenanceContinuesAfterFailuresAndPreservesLastObservation(t *testing.T) {
	oldest := time.Unix(1_700_000_000, 0)
	set := metrics.New()
	recorder := metrics.NewIntegrationControlRecorder(set)
	recorder.RecordOldestUnapplied(&oldest, oldest.Add(time.Minute))
	failErr := errors.New("fail scan unavailable")
	deleteErr := errors.New("retention unavailable")
	oldestErr := errors.New("observation unavailable")
	store := &integrationControlMaintenanceStub{failErr: failErr, deleteErr: deleteErr, oldestErr: oldestErr}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	worked, err := runIntegrationControlMaintenanceTick(t.Context(), log, store, recorder, time.Hour)
	if worked || !errors.Is(err, failErr) || !errors.Is(err, deleteErr) || !errors.Is(err, oldestErr) {
		t.Fatalf("tick = (%t, %v), want all failures and no work", worked, err)
	}
	if !slices.Equal(store.calls, []string{"fail", "delete", "observe"}) {
		t.Fatalf("calls after failure = %v", store.calls)
	}
	if got := controlGauge(t, set, "oldest_unapplied"); got != float64(oldest.Unix()) {
		t.Fatalf("failed lookup erased oldest timestamp: %v", got)
	}
	if got := controlGauge(t, set, "last_observation"); got != float64(oldest.Add(time.Minute).Unix()) {
		t.Fatalf("failed lookup changed observation timestamp: %v", got)
	}

	// Cleanup failure does not prevent a successful empty-queue observation.
	store.oldestErr = nil
	_, err = runIntegrationControlMaintenanceTick(t.Context(), log, store, recorder, time.Hour)
	if !errors.Is(err, failErr) || !errors.Is(err, deleteErr) {
		t.Fatalf("cleanup failures lost: %v", err)
	}
	if got := controlGauge(t, set, "oldest_unapplied"); got != 0 {
		t.Fatalf("empty queue timestamp = %v, want zero", got)
	}
}

func TestIntegrationControlMaintenanceCancellationDoesNotPublishEmptyObservation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(strconv.FormatBool(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}
			store := &integrationControlMaintenanceStub{
				failErr: context.Canceled, deleteErr: context.Canceled, oldestErr: context.Canceled,
			}
			set := metrics.New()
			recorder := metrics.NewIntegrationControlRecorder(set)
			oldest := time.Unix(1_700_000_000, 0)
			recorder.RecordOldestUnapplied(&oldest, oldest)
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			worked, err := runIntegrationControlMaintenanceTick(ctx, log, store, recorder, time.Hour)
			if worked || (canceled && err != nil) || (!canceled && !errors.Is(err, context.Canceled)) {
				t.Fatalf("tick = (%t, %v) with canceled context = %t", worked, err, canceled)
			}
			if got := controlGauge(t, set, "oldest_unapplied"); got != float64(oldest.Unix()) {
				t.Fatalf("canceled observation changed oldest timestamp: %v", got)
			}
			if got := controlGauge(t, set, "last_observation"); got != float64(oldest.Unix()) {
				t.Fatalf("canceled observation changed last-success timestamp: %v", got)
			}
		})
	}
}

func controlGauge(t *testing.T, set *metrics.Set, name string) float64 {
	t.Helper()
	response := httptest.NewRecorder()
	set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil))
	prefix := "omnara_integration_control_" + name + "_timestamp_seconds "
	for line := range strings.SplitSeq(response.Body.String(), "\n") {
		if value, found := strings.CutPrefix(line, prefix); found {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatal(err)
			}
			return number
		}
	}
	t.Fatalf("metric %q missing from scrape", prefix)
	return 0
}
