package defaultprovider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type provisioningStoreStub struct {
	claim                 orglifecycle.DefaultModelProviderProvisioningClaim
	found                 bool
	claimedOrganizationID uuid.UUID
	complete              func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error
	retry                 func(orglifecycle.RetryDefaultModelProviderProvisioningInput) error
}

func (stub *provisioningStoreStub) ClaimDefaultModelProviderProvisioning(
	context.Context,
) (orglifecycle.DefaultModelProviderProvisioningClaim, bool, error) {
	return stub.claim, stub.found, nil
}

func (stub *provisioningStoreStub) ClaimDefaultModelProviderProvisioningForOrganization(
	_ context.Context,
	organizationID uuid.UUID,
) (orglifecycle.DefaultModelProviderProvisioningClaim, bool, error) {
	stub.claimedOrganizationID = organizationID
	return stub.claim, stub.found, nil
}

func (stub *provisioningStoreStub) CompleteDefaultModelProviderProvisioning(
	_ context.Context,
	input orglifecycle.CompleteDefaultModelProviderProvisioningInput,
) error {
	return stub.complete(input)
}

func (stub *provisioningStoreStub) RetryDefaultModelProviderProvisioning(
	_ context.Context,
	input orglifecycle.RetryDefaultModelProviderProvisioningInput,
) error {
	return stub.retry(input)
}

type hostedCredentialProvisionerStub func(
	context.Context,
	modelprovider.HostedCredentialRequest,
) (modelprovider.ProvisionHostedCredentialResponse, error)

func (stub hostedCredentialProvisionerStub) ProvisionHostedCredential(
	ctx context.Context,
	request modelprovider.HostedCredentialRequest,
) (modelprovider.ProvisionHostedCredentialResponse, error) {
	return stub(ctx, request)
}

func TestRunnerCompletesIssuedCredential(t *testing.T) {
	claim := testProvisioningClaim()
	template := testProvisioningTemplate()
	store := &provisioningStoreStub{claim: claim, found: true}
	store.complete = func(input orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		if input.Claim.ClaimToken != claim.ClaimToken || input.Template.Name != template.Name ||
			input.CredentialValue != "issued-credential" {
			t.Fatalf("unexpected completion input: %+v", input)
		}
		return nil
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected retry: %+v", input)
		return nil
	}
	runner := NewRunner(store, hostedCredentialProvisionerStub(func(
		_ context.Context,
		request modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		wantOrgID, _ := publicid.Encode(publicid.KindOrganization, claim.OrgID)
		wantCreatorID, _ := publicid.Encode(publicid.KindUser, claim.CreatorUserID)
		if request.OrgID != wantOrgID || request.CreatorUserID != wantCreatorID ||
			request.Template.Name != template.Name {
			t.Fatalf("unexpected provision request: %+v", request)
		}
		return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
	}), template)

	attempted, organizationID, err := runner.RunNext(context.Background())
	if err != nil || !attempted || organizationID == "" {
		t.Fatalf("run next = attempted=%t org=%q err=%v", attempted, organizationID, err)
	}
}

func TestRunnerClaimsRequestedOrganization(t *testing.T) {
	claim := testProvisioningClaim()
	store := &provisioningStoreStub{claim: claim, found: true}
	store.complete = func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error { return nil }
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected retry: %+v", input)
		return nil
	}
	runner := NewRunner(store, hostedCredentialProvisionerStub(func(
		context.Context,
		modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
	}), testProvisioningTemplate())

	attempted, _, err := runner.RunOrganization(context.Background(), claim.OrgID)
	if err != nil || !attempted || store.claimedOrganizationID != claim.OrgID {
		t.Fatalf(
			"run organization = attempted=%t claimed=%s err=%v",
			attempted,
			store.claimedOrganizationID,
			err,
		)
	}
}

func TestRunnerSchedulesPendingRetry(t *testing.T) {
	claim := testProvisioningClaim()
	var retry orglifecycle.RetryDefaultModelProviderProvisioningInput
	store := &provisioningStoreStub{claim: claim, found: true}
	store.complete = func(input orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected completion: %+v", input)
		return nil
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		retry = input
		return nil
	}
	runner := NewRunner(store, hostedCredentialProvisionerStub(func(
		context.Context,
		modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		return modelprovider.ProvisionHostedCredentialResponse{}, modelprovider.ErrHostedCredentialPending
	}), testProvisioningTemplate())

	attempted, _, err := runner.RunNext(context.Background())
	if !attempted || !errors.Is(err, modelprovider.ErrHostedCredentialPending) {
		t.Fatalf("run next = attempted=%t err=%v", attempted, err)
	}
	if retry.Claim.ClaimToken != claim.ClaimToken || retry.Delay != pendingRetryDelay {
		t.Fatalf("retry = %+v, want pending delay", retry)
	}
}

func TestRunnerSurfacesRetryScheduleFailure(t *testing.T) {
	claim := testProvisioningClaim()
	retryErr := errors.New("database unavailable")
	store := &provisioningStoreStub{claim: claim, found: true}
	store.complete = func(input orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected completion: %+v", input)
		return nil
	}
	store.retry = func(orglifecycle.RetryDefaultModelProviderProvisioningInput) error { return retryErr }
	runner := NewRunner(store, hostedCredentialProvisionerStub(func(
		context.Context,
		modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		return modelprovider.ProvisionHostedCredentialResponse{}, modelprovider.ErrHostedCredentialPending
	}), testProvisioningTemplate())

	attempted, _, err := runner.RunNext(context.Background())
	if !attempted || !errors.Is(err, modelprovider.ErrHostedCredentialPending) ||
		!errors.Is(err, ErrRetrySchedule) || !errors.Is(err, retryErr) {
		t.Fatalf("run next = attempted=%t err=%v", attempted, err)
	}
}

func TestRunnerRetriesCompletionFailure(t *testing.T) {
	claim := testProvisioningClaim()
	completeErr := errors.New("database unavailable")
	var retry orglifecycle.RetryDefaultModelProviderProvisioningInput
	store := &provisioningStoreStub{claim: claim, found: true}
	store.complete = func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error { return completeErr }
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		retry = input
		return nil
	}
	runner := NewRunner(store, hostedCredentialProvisionerStub(func(
		context.Context,
		modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
	}), testProvisioningTemplate())

	attempted, _, err := runner.RunNext(context.Background())
	if !attempted || !errors.Is(err, completeErr) {
		t.Fatalf("run next = attempted=%t err=%v", attempted, err)
	}
	if retry.Claim.ClaimToken != claim.ClaimToken || retry.Delay != initialRetryDelay {
		t.Fatalf("retry = %+v, want initial retry delay", retry)
	}
}

func TestRunnerDoesNotRetryTerminalCompletion(t *testing.T) {
	for _, completionErr := range []error{
		orglifecycle.ErrDefaultModelProviderProvisioningSuperseded,
		storeerr.ErrStateTransitionConflict,
		storeerr.ErrNotFound,
	} {
		t.Run(completionErr.Error(), func(t *testing.T) {
			claim := testProvisioningClaim()
			store := &provisioningStoreStub{claim: claim, found: true}
			store.complete = func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
				return completionErr
			}
			store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
				t.Fatalf("unexpected retry: %+v", input)
				return nil
			}
			runner := NewRunner(store, hostedCredentialProvisionerStub(func(
				context.Context,
				modelprovider.HostedCredentialRequest,
			) (modelprovider.ProvisionHostedCredentialResponse, error) {
				return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
			}), testProvisioningTemplate())

			attempted, _, err := runner.RunNext(context.Background())
			if !attempted || !errors.Is(err, completionErr) {
				t.Fatalf("run next = attempted=%t err=%v", attempted, err)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	for _, test := range []struct {
		name    string
		attempt int32
		err     error
		want    time.Duration
	}{
		{name: "initial", attempt: 1, err: errors.New("unavailable"), want: 5 * time.Second},
		{name: "exponential", attempt: 4, err: errors.New("unavailable"), want: 40 * time.Second},
		{name: "capped", attempt: 100, err: errors.New("unavailable"), want: 15 * time.Minute},
		{name: "pending", attempt: 100, err: modelprovider.ErrHostedCredentialPending, want: 2 * time.Minute},
		{name: "conflict", attempt: 1, err: modelprovider.ErrHostedCredentialConflict, want: 24 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := retryDelay(test.attempt, test.err); got != test.want {
				t.Fatalf("retry delay = %s, want %s", got, test.want)
			}
		})
	}
}

func testProvisioningClaim() orglifecycle.DefaultModelProviderProvisioningClaim {
	return orglifecycle.DefaultModelProviderProvisioningClaim{
		OrgID:         uuid.MustParse("01922e74-9d00-7000-8000-000000000001"),
		CreatorUserID: uuid.MustParse("01922e74-9d00-7000-8000-000000000002"),
		ClaimToken:    uuid.MustParse("01922e74-9d00-7000-8000-000000000003"),
		Attempt:       1,
	}
}

func testProvisioningTemplate() modelstore.DefaultModelProviderTemplate {
	return modelstore.DefaultModelProviderTemplate{Provisioner: "test", Name: "default-provider"}
}
