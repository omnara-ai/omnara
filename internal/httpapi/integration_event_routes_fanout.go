package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const integrationIntakeTimeout = 5 * time.Second
const integrationIngressPageSize = 100

type integrationFanoutResult struct {
	Matched  int
	Verified int
}

// Receipts commit independently. Redelivery can recover failed integrations because
// committed receipts deduplicate successful intake.
func fanoutIntegrations(
	ctx context.Context,
	listIntegrations func(
		context.Context,
		string,
		string,
		string,
		uuid.UUID,
		int,
	) ([]integrationstore.ProjectIntegrationRecord, error),
	provider, tenant, account string,
	accept func(context.Context, integrationstore.ProjectIntegrationRecord) (bool, error),
) (integrationFanoutResult, error) {
	var result integrationFanoutResult
	var retryErr error
	var disconnectedVerificationErr error
	after := uuid.Nil
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		page, err := listIntegrations(ctx, provider, tenant, account, after, integrationIngressPageSize)
		if err != nil {
			return result, err
		}
		if len(page) > integrationIngressPageSize {
			return result, fmt.Errorf("integration ingress page exceeds limit")
		}
		for _, integration := range page {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if integration.ID == uuid.Nil || integration.ID.String() <= after.String() {
				return result, fmt.Errorf("integration ingress cursor did not advance")
			}
			after = integration.ID
			// Acknowledge disconnected integrations to avoid retries: https://docs.slack.dev/apis/events-api/#responding
			eligible := integration.State == integrationstore.ProjectIntegrationStateActive ||
				(provider == integrationstore.IntegrationProviderSlack &&
					integration.State == integrationstore.ProjectIntegrationStateDisconnected &&
					integration.CredentialSecretID != uuid.Nil)
			if !eligible || integration.DeletedAt != nil || integration.Provider != provider ||
				integration.ProviderTenantID != tenant || integration.ProviderAccountRef != account {
				continue
			}
			result.Matched++
			verified, err := accept(ctx, integration)
			if verified {
				result.Verified++
			}
			if err != nil {
				retryable := !permanentIntegrationIngressError(err)
				stage := "credential_verification"
				if verified {
					stage = "intake"
				}
				// Raw storage/decryption errors can contain credentials; log only their type.
				log.LoggerFromContext(ctx).WarnContext(ctx, "provider event integration failed",
					"provider", integration.Provider, "project_id", integration.ProjectID, "integration_id", integration.ID,
					"integration_state", integration.State,
					"setup_revision", integration.SetupRevision, "stage", stage,
					"retryable", retryable, "error_type", fmt.Sprintf("%T", err))
				if retryable {
					if integration.State == integrationstore.ProjectIntegrationStateDisconnected {
						disconnectedVerificationErr = err
					} else {
						retryErr = err
					}
				}
			}
		}
		if len(page) < integrationIngressPageSize {
			if retryErr == nil && result.Verified == 0 {
				retryErr = disconnectedVerificationErr
			}
			return result, retryErr
		}
	}
}

func permanentIntegrationIngressError(err error) bool {
	return storeerr.IsNotFound(err) || errors.Is(err, storeerr.ErrUnauthorized) ||
		errors.Is(err, storeerr.ErrInvalidRequest) || errors.Is(err, storeerr.ErrStateTransitionConflict)
}
