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

// fanoutIntegrationApps bounds each lookup and the request lifetime, not the
// number of independently saved apps. A failed app does not prevent a sibling's
// receipt from committing. Active-app failures still force provider redelivery;
// already committed receipts make that retry safe.
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
			// Slack must acknowledge verified events for locally disconnected
			// apps. The callback decides to ignore them without durable intake.
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
				// Unknown storage/decryption errors may contain credential or row
				// contents. Log owner and error type, never their raw message.
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
			// Disconnected apps have no intake obligation. Their failed
			// verification cannot poison an independently verified sibling,
			// but cannot authorize an acknowledgement on its own either.
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
