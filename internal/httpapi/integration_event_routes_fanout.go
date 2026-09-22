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

// Receipts commit independently. Redelivery can recover failed apps because
// committed receipts deduplicate successful intake.
func fanoutIntegrationApps(
	ctx context.Context,
	listApps func(context.Context, string, string, string, uuid.UUID, int) ([]integrationstore.ProjectAppRecord, error),
	provider, tenant, account string,
	accept func(context.Context, integrationstore.ProjectAppRecord) (bool, error),
) (integrationFanoutResult, error) {
	var result integrationFanoutResult
	var retryErr error
	var disconnectedVerificationErr error
	after := uuid.Nil
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		page, err := listApps(ctx, provider, tenant, account, after, integrationIngressPageSize)
		if err != nil {
			return result, err
		}
		if len(page) > integrationIngressPageSize {
			return result, fmt.Errorf("app ingress page exceeds limit")
		}
		for _, app := range page {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if app.ID == uuid.Nil || app.ID.String() <= after.String() {
				return result, fmt.Errorf("app ingress cursor did not advance")
			}
			after = app.ID
			// Acknowledge disconnected apps to avoid retries: https://docs.slack.dev/apis/events-api/#responding
			eligible := app.State == integrationstore.ProjectAppStateActive ||
				(provider == integrationstore.IntegrationProviderSlack &&
					app.State == integrationstore.ProjectAppStateDisconnected && app.CredentialSecretID != uuid.Nil)
			if !eligible || app.DeletedAt != nil || app.Provider != provider ||
				app.ProviderTenantID != tenant || app.ProviderAccountRef != account {
				continue
			}
			result.Matched++
			verified, err := accept(ctx, app)
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
				log.LoggerFromContext(ctx).WarnContext(ctx, "provider event app failed",
					"provider", app.Provider, "project_id", app.ProjectID, "app_id", app.ID,
					"app_state", app.State,
					"setup_revision", app.SetupRevision, "stage", stage,
					"retryable", retryable, "error_type", fmt.Sprintf("%T", err))
				if retryable {
					if app.State == integrationstore.ProjectAppStateDisconnected {
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
