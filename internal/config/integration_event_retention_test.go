package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntegrationEventRetentionConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "default", want: 7 * 24 * time.Hour},
		{name: "override", raw: "48h", want: 48 * time.Hour},
		{name: "minimum precision", raw: "1us", want: time.Microsecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OMNARA_INTEGRATION_EVENT_RETENTION", tc.raw)
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.IntegrationEventRetention)
		})
	}
	for _, raw := range []string{"invalid", "0s", "-1s", "1ns"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("OMNARA_INTEGRATION_EVENT_RETENTION", raw)
			_, err := Load()
			require.ErrorContains(t, err, "OMNARA_INTEGRATION_EVENT_RETENTION")
		})
	}
}

func TestMaintenanceRejectsUnrepresentableIntegrationEventRetention(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_INTEGRATION_EVENT_RETENTION", "168h")
	cfg, err := Load()
	require.NoError(t, err)
	for _, retention := range []time.Duration{0, -time.Second, time.Nanosecond} {
		cfg.IntegrationEventRetention = retention
		require.ErrorContains(t, cfg.ValidateMaintenance(), "OMNARA_INTEGRATION_EVENT_RETENTION")
	}
}
