package model

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrDeferredPending reports that a deferred response is not available yet and
// the caller should look again later.
var ErrDeferredPending = errors.New("deferred model response is still pending")

// DeferredClient is a Client whose provider can accept a request now and
// deliver the response later, so no caller has to stay blocked on the call.
// Respond remains a synchronous call for work that cannot be deferred.
type DeferredClient interface {
	Client
	SubmitDeferred(ctx context.Context, input DeferredRequest) (DeferredSubmission, error)
	// CollectDeferred returns the response of a submitted request, or an error
	// matching ErrDeferredPending while the provider has not finished.
	CollectDeferred(ctx context.Context, handle DeferredHandle) (Response, error)
}

type DeferredRequest struct {
	// ProviderRequest is the body authorized by Prepare.
	ProviderRequest json.RawMessage
	// CustomID identifies the request inside the provider's deferred job.
	CustomID string
	// CallbackURL, when set, is called by the provider once the job is final.
	CallbackURL string
}

type DeferredSubmission struct {
	ProviderHandle string
	// ExpiresAt is zero when the provider reports no hard deadline.
	ExpiresAt time.Time
}

type DeferredHandle struct {
	ProviderHandle string
	CustomID       string
}
