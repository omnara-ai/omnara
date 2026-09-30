package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var (
	ErrIntegrationLaunchReserved = errors.New("integration conversation reserved by another receipt")
	ErrIntegrationLaunchSettled  = errors.New("integration conversation already selected; replan")
)

type IntegrationLaunchClaimError struct {
	ReceiptID uuid.UUID
	State     IntegrationInboxState
}

func (e *IntegrationLaunchClaimError) Error() string {
	return fmt.Sprintf("%s: receipt %s (%s)", ErrIntegrationLaunchReserved, e.ReceiptID, e.State)
}

func (e *IntegrationLaunchClaimError) Unwrap() error { return ErrIntegrationLaunchReserved }

// InboxLaunchClaim reserves an integration/address so concurrent plans
// cannot split the initial conversation membership.
type InboxLaunchClaim struct {
	IntegrationID uuid.UUID           `json:"integration_id"`
	Address       ConversationAddress `json:"address"`
	LaunchKey     string              `json:"launch_key"`
}

type integrationLaunchIdentity struct {
	IntegrationID uuid.UUID           `json:"integration_id"`
	Address       ConversationAddress `json:"address"`
}

// MarkIntegrationPendingLaunch protects follow-ups during provider enrichment.
// Actual launch plans ignore provider markers and still arbitrate at Freeze.
func (w *IntegrationInboxLeaseTx) MarkIntegrationPendingLaunch(ctx context.Context, address ConversationAddress) error {
	if err := address.Validate(); err != nil {
		return err
	}
	if err := w.refreshLease(ctx); err != nil {
		return err
	}
	if w.record.Source != IntegrationInboxSourceProvider {
		return storeerr.ErrUnauthorized
	}
	if len(w.record.Plan) != 0 {
		return nil
	}
	if err := LockConversationTx(ctx, w.tx, w.record.ProjectID, w.record.IntegrationID, address); err != nil {
		return err
	}
	if err := w.CheckLease(ctx); err != nil {
		return err
	}
	owners, err := w.q.ListConversationLaunchOwners(ctx, dbsqlc.ListConversationLaunchOwnersParams{
		ProjectID: w.record.ProjectID, IntegrationID: w.record.IntegrationID, Kind: address.Kind, Ref: address.Ref,
	})
	if err != nil || len(owners) != 0 {
		return err
	}
	rows, err := w.q.MarkIntegrationInboxPendingLaunch(ctx, dbsqlc.MarkIntegrationInboxPendingLaunchParams{
		ProjectID: w.record.ProjectID, ID: w.record.ID, ClaimToken: w.lease.Token, Kind: address.Kind, Ref: address.Ref,
	})
	if err != nil {
		return fmt.Errorf("mark pending integration launch: %w", err)
	}
	if rows == 0 {
		if err := w.CheckLease(ctx); err != nil {
			return err
		}
		return fmt.Errorf("pending launch address changed: %w", storeerr.ErrConflict)
	}
	return nil
}

func (w *IntegrationInboxLeaseTx) CheckNoUnsettledIntegrationLaunch(
	ctx context.Context, address ConversationAddress,
) error {
	// Provider follow-ups wait for earlier provider candidates. State work never
	// waits on providers; accepted state reservations and frozen claims block at any age.
	if err := w.CheckLease(ctx); err != nil {
		return err
	}
	if err := LockConversationTx(ctx, w.tx, w.record.ProjectID, w.record.IntegrationID, address); err != nil {
		return err
	}
	if err := w.CheckLease(ctx); err != nil {
		return err
	}
	settled, err := w.q.ListConversationLaunchOwners(ctx, dbsqlc.ListConversationLaunchOwnersParams{
		ProjectID: w.record.ProjectID, IntegrationID: w.record.IntegrationID,
		Kind: address.Kind, Ref: address.Ref,
	})
	if err != nil {
		return err
	}
	// Ownership commits with the launch, independently of other recipients still retrying in its receipt.
	if len(settled) != 0 {
		return nil
	}
	if err := w.checkUnplannedIntegrationLaunch(ctx, address, w.record.Source != IntegrationInboxSourceState); err != nil {
		return err
	}
	claim, err := json.Marshal(integrationLaunchIdentity{IntegrationID: w.record.IntegrationID, Address: address})
	if err != nil {
		return err
	}
	owners, err := w.q.FindInboxLaunchClaims(ctx, dbsqlc.FindInboxLaunchClaimsParams{
		ProjectID:     w.record.ProjectID,
		IntegrationID: w.record.IntegrationID,
		ReceiptID:     w.record.ID,
		LaunchClaim:   claim,
	})
	if err != nil {
		return err
	}
	if len(owners) != 0 {
		return &IntegrationLaunchClaimError{ReceiptID: owners[0].ID, State: IntegrationInboxState(owners[0].State)}
	}
	return nil
}

func (w *IntegrationInboxLeaseTx) reserveIntegrationLaunchClaims(
	ctx context.Context, plan json.RawMessage, recipients map[string]json.RawMessage,
) error {
	identities, err := inboxLaunchIdentities(recipients, w.record.IntegrationID)
	if err != nil {
		return err
	}
	// Only a scheduler-accepted receipt can reserve the scheduled launch key;
	// provider payloads and selected profile choices do not confer that authority.
	if w.record.Source != IntegrationInboxSourceScheduled {
		for _, launchKeys := range identities {
			if launchKeys["scheduled"] {
				return storeerr.ErrUnauthorized
			}
		}
	}
	if err := lockInboxLaunchConversations(
		ctx,
		w.tx,
		w.record.ProjectID,
		w.record.IntegrationID,
		identities,
	); err != nil {
		return err
	}
	if len(identities) == 0 && w.record.Source != IntegrationInboxSourceScheduled {
		return nil
	}
	integration, err := getIntegration(ctx, w.q, w.record.ProjectID, w.record.IntegrationID)
	if err != nil {
		return fmt.Errorf("load selected integration: %w", err)
	}
	if integration.State != IntegrationStateActive {
		return storeerr.ErrUnauthorized
	}
	if w.record.Source == IntegrationInboxSourceScheduled {
		if err := w.record.ValidateScheduledPlan(integration, plan); err != nil {
			return err
		}
	} else {
		definition, ok := integrationdefinition.Lookup(integration.IntegrationKind)
		if !ok || definition.Launcher == nil {
			return storeerr.ErrUnauthorized
		}
	}
	// Lookup in separate statements after acquiring the gate, so a waiter sees
	// the previous planner's committed reservation at READ COMMITTED.
	for identity := range identities {
		if err := w.checkUnplannedIntegrationLaunch(ctx, identity.Address, false); err != nil {
			return err
		}
		claim, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		owners, err := w.q.FindInboxLaunchClaims(ctx, dbsqlc.FindInboxLaunchClaimsParams{
			ProjectID:     w.record.ProjectID,
			IntegrationID: identity.IntegrationID,
			ReceiptID:     w.record.ID,
			LaunchClaim:   claim,
		})
		if err != nil {
			return err
		}
		if len(owners) != 0 {
			return &IntegrationLaunchClaimError{ReceiptID: owners[0].ID, State: IntegrationInboxState(owners[0].State)}
		}
		targets, err := w.q.ListConversationLaunchOwners(ctx, dbsqlc.ListConversationLaunchOwnersParams{
			ProjectID:     w.record.ProjectID,
			IntegrationID: identity.IntegrationID,
			Kind:          identity.Address.Kind,
			Ref:           identity.Address.Ref,
		})
		if err != nil {
			return err
		}
		if len(targets) != 0 {
			return ErrIntegrationLaunchSettled
		}
	}
	return nil
}

func (w *IntegrationInboxLeaseTx) checkUnplannedIntegrationLaunch(
	ctx context.Context, address ConversationAddress, includeProvider bool,
) error {
	owner, err := w.q.FindUnplannedIntegrationLaunchClaim(ctx,
		dbsqlc.FindUnplannedIntegrationLaunchClaimParams{
			ProjectID: w.record.ProjectID, IntegrationID: w.record.IntegrationID,
			Kind: address.Kind, Ref: address.Ref,
			ReceiptID:       w.record.ID,
			IncludeProvider: includeProvider,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &IntegrationLaunchClaimError{ReceiptID: owner.ID, State: IntegrationInboxState(owner.State)}
}

func inboxLaunchIdentities(
	recipients map[string]json.RawMessage,
	integrationID uuid.UUID,
) (map[integrationLaunchIdentity]map[string]bool, error) {
	identities := map[integrationLaunchIdentity]map[string]bool{}
	for _, raw := range recipients {
		var envelope struct {
			LaunchClaim json.RawMessage `json:"launch_claim"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, inboxInvalid("invalid claim envelope")
		}
		if len(envelope.LaunchClaim) == 0 {
			continue
		}
		var claim InboxLaunchClaim
		decoder := json.NewDecoder(bytes.NewReader(envelope.LaunchClaim))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&claim); err != nil {
			return nil, inboxInvalid("invalid claim envelope")
		}
		canonical, err := json.Marshal(claim)
		if err != nil {
			return nil, err
		}
		if !jsoncanonical.Equal(canonical, envelope.LaunchClaim) {
			return nil, inboxInvalid("claim identities must use their canonical encoding")
		}
		if claim.IntegrationID == uuid.Nil || claim.IntegrationID != integrationID ||
			claim.LaunchKey == "" || len(claim.LaunchKey) > 64 ||
			strings.TrimSpace(claim.LaunchKey) != claim.LaunchKey {
			return nil, inboxInvalid("claim requires an integration, stable launch key and the receipt integration")
		}
		if err := claim.Address.Validate(); err != nil {
			return nil, err
		}
		identity := integrationLaunchIdentity{IntegrationID: claim.IntegrationID, Address: claim.Address}
		if identities[identity] == nil {
			identities[identity] = map[string]bool{}
		}
		if identities[identity][claim.LaunchKey] {
			return nil, inboxInvalid("duplicate integration launch key in plan")
		}
		identities[identity][claim.LaunchKey] = true
	}
	return identities, nil
}

func lockInboxLaunchConversations(ctx context.Context, tx pgx.Tx, projectID, integrationID uuid.UUID,
	identities map[integrationLaunchIdentity]map[string]bool) error {
	addresses := map[ConversationAddress]bool{}
	for identity := range identities {
		addresses[identity.Address] = true
	}
	ordered := make([]ConversationAddress, 0, len(addresses))
	for address := range addresses {
		ordered = append(ordered, address)
	}
	slices.SortFunc(ordered, func(a, b ConversationAddress) int {
		if order := strings.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return strings.Compare(a.Ref, b.Ref)
	})
	for _, address := range ordered {
		if err := LockConversationTx(ctx, tx, projectID, integrationID, address); err != nil {
			return err
		}
	}
	return nil
}
