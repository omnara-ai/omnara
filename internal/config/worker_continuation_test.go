package config

import (
	"testing"
	"time"
)

func TestWorkerContinuationConfig(t *testing.T) {
	for _, test := range []struct {
		name, starts, duration string
		wantStarts             int
		wantDuration           time.Duration
		invalid                bool
	}{
		{"defaults", "", "", 2, 30 * time.Second, false},
		{"explicit", "3", "12s", 3, 12 * time.Second, false},
		{"disabled", "0", "30s", 0, 30 * time.Second, false},
		{"negative starts", "-1", "30s", 0, 0, true},
		{"invalid starts", "bad", "30s", 0, 0, true},
		{"zero duration", "2", "0s", 0, 0, true},
		{"negative duration", "2", "-1s", 0, 0, true},
		{"invalid duration", "2", "bad", 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
			t.Setenv("OMNARA_WORKER_CONTINUATION_MAX_MODEL_STARTS", test.starts)
			t.Setenv("OMNARA_WORKER_CONTINUATION_MAX_DURATION", test.duration)
			cfg, err := Load()
			if err == nil {
				err = cfg.ValidateWorker()
			}
			if test.invalid {
				if err == nil {
					t.Fatal("expected invalid continuation config")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.WorkerContinuationMaxModelStarts != test.wantStarts ||
				cfg.WorkerContinuationMaxDuration != test.wantDuration {
				t.Fatalf("continuation=%d/%s want=%d/%s",
					cfg.WorkerContinuationMaxModelStarts, cfg.WorkerContinuationMaxDuration, test.wantStarts, test.wantDuration)
			}
		})
	}
}
