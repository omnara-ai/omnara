package integrationstore

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestNormalizedJSONObjectUsesDatabaseSafeConnectorContract(t *testing.T) {
	t.Parallel()

	normalized, err := normalizedJSONObject([]byte(`{ "z": 1, "a": "value" }`), "metadata")
	if err != nil {
		t.Fatalf("normalize valid object: %v", err)
	}
	if string(normalized) != `{"a":"value","z":1}` {
		t.Fatalf("normalized object = %s", normalized)
	}

	for _, value := range [][]byte{
		[]byte(`{"message":"bad\u0000value"}`),
		[]byte(`{"value":1e1000000}`),
		[]byte(`[]`),
		bytes.Repeat([]byte(" "), channelconnector.MaxMetadataBytes+1),
	} {
		if _, err := normalizedJSONObject(value, "metadata"); !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("normalizedJSONObject(%q) error = %v, want invalid request", value, err)
		}
	}
}

func TestIntegrationChannelWriteErrorClassifiesOnlyJSONBounds(t *testing.T) {
	t.Parallel()

	for _, constraint := range []string{
		"integration_apps_provider_config_bytes_check",
		"integration_apps_provider_metadata_bytes_check",
		"integration_installs_channel_payload_bounds_check",
		"integration_routes_configuration_bytes_check",
		"integration_targets_channel_payload_bounds_check",
		"integration_target_bindings_metadata_bytes_check",
		"integration_deliveries_payload_bytes_check",
		"integration_deliveries_last_error_bytes_check",
		"integration_runtime_units_configuration_bytes_check",
		"integration_runtime_units_checkpoint_bytes_check",
		"integration_runtime_units_last_error_bytes_check",
	} {
		t.Run(constraint, func(t *testing.T) {
			t.Parallel()
			err := integrationChannelWriteError("write channel object", &pgconn.PgError{
				Code: "23514", ConstraintName: constraint,
			})
			if !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("error = %v, want invalid request", err)
			}
		})
	}
	for _, databaseError := range []*pgconn.PgError{
		{Code: "23514", ConstraintName: "unrelated_check"},
		{Code: "23505", ConstraintName: "integration_deliveries_payload_bytes_check"},
	} {
		if err := integrationChannelWriteError("write channel object", databaseError); errors.Is(
			err,
			storeerr.ErrInvalidRequest,
		) {
			t.Fatalf("error = %v, do not want invalid request", err)
		}
	}
}

func TestValidateIntegrationRuntimeLeaseProofClassifiesMalformedProof(t *testing.T) {
	t.Parallel()

	if err := ValidateIntegrationRuntimeLeaseProof(nil); err != nil {
		t.Fatalf("optional runtime lease proof: %v", err)
	}
	if err := ValidateIntegrationRuntimeLeaseProof(
		&IntegrationRuntimeLeaseProof{},
	); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("malformed runtime lease proof error = %v, want invalid request", err)
	}
}
