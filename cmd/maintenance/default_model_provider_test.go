package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestDefaultModelProviderProvisioningTickRecoversPanic(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	store := &defaultModelProviderProvisioningStoreStub{claim: claim, found: true}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			context.Context,
			modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			panic("provider panic")
		}),
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, _, err := runDefaultModelProviderProvisioningTick(
		context.Background(),
		log,
		worker,
	); err == nil {
		t.Fatal("provisioning tick panic error = nil")
	}
}

type defaultModelProviderProvisioningStoreStub struct {
	claim    orglifecycle.DefaultModelProviderProvisioningClaim
	found    bool
	claimErr error
	complete func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error
	retry    func(orglifecycle.RetryDefaultModelProviderProvisioningInput) error
}

func (stub *defaultModelProviderProvisioningStoreStub) ClaimDefaultModelProviderProvisioning(
	context.Context,
) (orglifecycle.DefaultModelProviderProvisioningClaim, bool, error) {
	return stub.claim, stub.found, stub.claimErr
}

func (stub *defaultModelProviderProvisioningStoreStub) CompleteDefaultModelProviderProvisioning(
	_ context.Context,
	input orglifecycle.CompleteDefaultModelProviderProvisioningInput,
) error {
	return stub.complete(input)
}

func (stub *defaultModelProviderProvisioningStoreStub) RetryDefaultModelProviderProvisioning(
	_ context.Context,
	input orglifecycle.RetryDefaultModelProviderProvisioningInput,
) error {
	return stub.retry(input)
}

type defaultModelProviderProvisionerStub func(
	context.Context,
	modelprovider.HostedCredentialRequest,
) (modelprovider.ProvisionHostedCredentialResponse, error)

func (stub defaultModelProviderProvisionerStub) ProvisionHostedCredential(
	ctx context.Context,
	request modelprovider.HostedCredentialRequest,
) (modelprovider.ProvisionHostedCredentialResponse, error) {
	return stub(ctx, request)
}

func TestDefaultModelProviderProvisioningWorkerCompletesIssuedCredential(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	store := &defaultModelProviderProvisioningStoreStub{claim: claim, found: true}
	store.complete = func(input orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		if input.Claim.ClaimToken != claim.ClaimToken || input.CredentialValue != "issued-credential" {
			t.Fatalf("unexpected completion input: %+v", input)
		}
		return nil
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected retry: %+v", input)
		return nil
	}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			_ context.Context,
			request modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			wantOrgID, _ := publicid.Encode(publicid.KindOrganization, claim.OrgID)
			wantCreatorID, _ := publicid.Encode(publicid.KindUser, claim.CreatorUserID)
			if request.OrgID != wantOrgID || request.CreatorUserID != wantCreatorID ||
				request.Template.Name != claim.Template.Name {
				t.Fatalf("unexpected provision request: %+v", request)
			}
			return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
		}),
	}

	attempted, orgID, err := worker.runOnce(context.Background())
	if err != nil || !attempted || orgID == "" {
		t.Fatalf("run once = attempted=%t org=%q err=%v", attempted, orgID, err)
	}
}

func TestDefaultModelProviderProvisioningWorkerSchedulesPendingRetry(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	var retry orglifecycle.RetryDefaultModelProviderProvisioningInput
	store := &defaultModelProviderProvisioningStoreStub{claim: claim, found: true}
	store.complete = func(input orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected completion: %+v", input)
		return nil
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		retry = input
		return nil
	}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			context.Context,
			modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			return modelprovider.ProvisionHostedCredentialResponse{}, modelprovider.ErrHostedCredentialPending
		}),
	}

	attempted, _, err := worker.runOnce(context.Background())
	if !attempted || !errors.Is(err, modelprovider.ErrHostedCredentialPending) {
		t.Fatalf("run once = attempted=%t err=%v", attempted, err)
	}
	if retry.Claim.ClaimToken != claim.ClaimToken || retry.Delay != defaultModelProviderPendingRetryDelay {
		t.Fatalf("retry = %+v, want pending delay", retry)
	}
}

func TestDefaultModelProviderProvisioningWorkerRetriesClaimDecodeFailure(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	claimErr := errors.New("decode stored provider template")
	var retry orglifecycle.RetryDefaultModelProviderProvisioningInput
	store := &defaultModelProviderProvisioningStoreStub{
		claim: claim, found: true, claimErr: claimErr,
	}
	store.complete = func(input orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected completion: %+v", input)
		return nil
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		retry = input
		return nil
	}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			context.Context,
			modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			t.Fatal("unexpected credential provisioning")
			return modelprovider.ProvisionHostedCredentialResponse{}, nil
		}),
	}

	attempted, orgID, err := worker.runOnce(context.Background())
	if !attempted || orgID == "" || !errors.Is(err, claimErr) {
		t.Fatalf("run once = attempted=%t org=%q err=%v", attempted, orgID, err)
	}
	if retry.Claim.ClaimToken != claim.ClaimToken || retry.Delay != defaultModelProviderInitialRetryDelay {
		t.Fatalf("retry = %+v, want initial retry delay", retry)
	}
}

func TestDefaultModelProviderProvisioningWorkerSurfacesRetryScheduleFailure(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	retryErr := errors.New("database unavailable")
	store := &defaultModelProviderProvisioningStoreStub{claim: claim, found: true}
	store.complete = func(input orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected completion: %+v", input)
		return nil
	}
	store.retry = func(orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		return retryErr
	}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			context.Context,
			modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			return modelprovider.ProvisionHostedCredentialResponse{}, modelprovider.ErrHostedCredentialPending
		}),
	}

	attempted, _, err := worker.runOnce(context.Background())
	if !attempted || !errors.Is(err, modelprovider.ErrHostedCredentialPending) ||
		!errors.Is(err, errDefaultModelProviderRetrySchedule) || !errors.Is(err, retryErr) {
		t.Fatalf("run once = attempted=%t err=%v", attempted, err)
	}
}

func TestDefaultModelProviderProvisioningWorkerRetriesCompletionFailure(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	completeErr := errors.New("database unavailable")
	var retry orglifecycle.RetryDefaultModelProviderProvisioningInput
	store := &defaultModelProviderProvisioningStoreStub{claim: claim, found: true}
	store.complete = func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		return completeErr
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		retry = input
		return nil
	}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			context.Context,
			modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
		}),
	}

	attempted, _, err := worker.runOnce(context.Background())
	if !attempted || !errors.Is(err, completeErr) {
		t.Fatalf("run once = attempted=%t err=%v", attempted, err)
	}
	if retry.Claim.ClaimToken != claim.ClaimToken || retry.Delay != defaultModelProviderInitialRetryDelay {
		t.Fatalf("retry = %+v, want initial retry delay", retry)
	}
}

func TestDefaultModelProviderProvisioningWorkerDoesNotRetrySupersededJob(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	store := &defaultModelProviderProvisioningStoreStub{claim: claim, found: true}
	store.complete = func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		return orglifecycle.ErrDefaultModelProviderProvisioningSuperseded
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected retry: %+v", input)
		return nil
	}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			context.Context,
			modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
		}),
	}

	attempted, _, err := worker.runOnce(context.Background())
	if !attempted || !errors.Is(err, orglifecycle.ErrDefaultModelProviderProvisioningSuperseded) {
		t.Fatalf("run once = attempted=%t err=%v", attempted, err)
	}
}

func TestDefaultModelProviderProvisioningWorkerDoesNotRetryDeletedOrganization(t *testing.T) {
	claim := testDefaultModelProviderProvisioningClaim()
	store := &defaultModelProviderProvisioningStoreStub{claim: claim, found: true}
	store.complete = func(orglifecycle.CompleteDefaultModelProviderProvisioningInput) error {
		return storeerr.ErrNotFound
	}
	store.retry = func(input orglifecycle.RetryDefaultModelProviderProvisioningInput) error {
		t.Fatalf("unexpected retry: %+v", input)
		return nil
	}
	worker := defaultModelProviderProvisioningWorker{
		store: store,
		provisioner: defaultModelProviderProvisionerStub(func(
			context.Context,
			modelprovider.HostedCredentialRequest,
		) (modelprovider.ProvisionHostedCredentialResponse, error) {
			return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "issued-credential"}, nil
		}),
	}

	attempted, _, err := worker.runOnce(context.Background())
	if !attempted || !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("run once = attempted=%t err=%v", attempted, err)
	}
}

func TestDefaultModelProviderRetryDelay(t *testing.T) {
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
			if got := defaultModelProviderRetryDelay(test.attempt, test.err); got != test.want {
				t.Fatalf("retry delay = %s, want %s", got, test.want)
			}
		})
	}
}

func testDefaultModelProviderProvisioningClaim() orglifecycle.DefaultModelProviderProvisioningClaim {
	return orglifecycle.DefaultModelProviderProvisioningClaim{
		OrgID:         uuid.MustParse("01922e74-9d00-7000-8000-000000000001"),
		CreatorUserID: uuid.MustParse("01922e74-9d00-7000-8000-000000000002"),
		ClaimToken:    uuid.MustParse("01922e74-9d00-7000-8000-000000000003"),
		Attempt:       1,
		Template: modelstore.DefaultModelProviderTemplate{
			Provisioner: "test",
			Name:        "default-provider",
		},
	}
}
