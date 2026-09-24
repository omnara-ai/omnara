package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const discordRuntimeLease = 30 * time.Second

type DiscordRuntime struct {
	Integrations *integrationstore.Store
	Secrets      *secretstore.Store
	Redis        identifyRedis
	HTTPClient   *http.Client
	Log          *slog.Logger
	Capacity     int
	runShard     func(context.Context, discord.ShardConfig, *discord.Checkpoint, discord.CommitDispatch) error
}

func (r *DiscordRuntime) Run(ctx context.Context) error {
	if r.Integrations == nil || r.Secrets == nil || r.Redis == nil {
		return errors.New("discord runtime requires integration, secret and Redis stores")
	}
	capacity := r.Capacity
	if capacity <= 0 {
		capacity = 64
	}
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	active := map[uuid.UUID]bool{}
	done := make(chan uuid.UUID, capacity)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	cursor := uuid.Nil
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case integrationID := <-done:
			delete(active, integrationID)
		case <-timer.C:
			if len(active) < capacity {
				scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				refs, err := r.Integrations.ListPersistentIntegrations(scanCtx, cursor, 100)
				if err != nil {
					log.Warn("list Discord integrations", "error", err)
				} else {
					for _, ref := range refs {
						cursor = ref.IntegrationID
						if len(active) >= capacity {
							break
						}
						if active[ref.IntegrationID] {
							continue
						}
						integrationSetup, err := r.Integrations.GetProjectIntegration(
							scanCtx,
							ref.ProjectID,
							ref.IntegrationID,
						)
						if err != nil {
							continue
						}
						secret, err := r.Secrets.GetProjectAvailableSecret(
							scanCtx,
							integrationSetup.OrgID,
							integrationSetup.ProjectID,
							integrationSetup.CredentialSecretID,
						)
						if err != nil {
							log.Warn("Discord credential unavailable", "integration_id", integrationSetup.ID, "error", err)
							continue
						}
						revision := integrationstore.IntegrationRuntimeRevision{
							ProjectID: integrationSetup.ProjectID, IntegrationID: integrationSetup.ID,
							Key: "discord/shard/0", SetupRevision: integrationSetup.SetupRevision,
							CredentialVersionID: secret.Secret.CurrentVersionID,
						}
						started := time.Now()
						claim, found, err := r.Integrations.ClaimIntegrationRuntime(scanCtx, revision, discordRuntimeLease)
						if err != nil {
							log.Warn("claim Discord connection", "integration_id", integrationSetup.ID, "error", err)
							continue
						}
						if !found {
							continue
						}
						active[integrationSetup.ID] = true
						jobs.Add(1)
						go func() {
							defer jobs.Done()
							r.run(ctx, integrationSetup, claim, started, log)
							done <- integrationSetup.ID
						}()
					}
					if len(refs) < 100 {
						cursor = uuid.Nil
					}
				}
				cancel()
			}
			timer.Reset(2 * time.Second)
		}
	}
}

func (r *DiscordRuntime) run(
	parent context.Context,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	claim integrationstore.IntegrationRuntimeClaim,
	claimedAt time.Time,
	log *slog.Logger,
) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		r.heartbeat(ctx, cancel, claim.Lease, claimedAt.Add(discordRuntimeLease))
	}()
	var runErr error
	defer func() {
		cancel()
		<-heartbeatDone
		delay := discordReconnectDelay(runErr)
		failure := discordRuntimeFailureMessage(runErr)
		if failure != "" {
			log.Warn("Discord connection stopped", "integration_id", integrationSetup.ID, "error", runErr)
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer releaseCancel()
		if err := r.Integrations.ReleaseIntegrationRuntime(
			releaseCtx,
			claim.Lease,
			delay,
			failure,
		); err != nil &&
			!errors.Is(err, integrationstore.ErrIntegrationRuntimeLeaseLost) {
			log.Warn("release Discord connection", "integration_id", integrationSetup.ID, "error", err)
		}
	}()
	runErr = r.connect(ctx, integrationSetup, claim)
}

// Runtime failures are exposed by GET integration. Persist only bounded, fixed messages;
// provider payloads, transport URLs and internal error details belong in the log.
func discordRuntimeFailureMessage(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	const credentials = "Discord rejected the bot token. Check this integration's credentials."
	const rateLimited = "Discord is limiting connection requests. Omnara will retry later."
	var apiError *discord.APIError
	if errors.As(err, &apiError) {
		switch {
		case apiError.Code == discord.ScopeMismatch:
			return "Discord bot identity does not match this integration's setup. Check the application ID, bot user ID and token."
		case apiError.Code == discord.RateLimited:
			return rateLimited
		case apiError.Code == discord.PermanentFailure && apiError.StatusCode == http.StatusUnauthorized:
			return credentials
		case apiError.Code == discord.PermanentFailure && apiError.StatusCode == http.StatusForbidden:
			return "Discord denied access. Check the bot's permissions and integration setup."
		}
	}
	var gatewayError *discord.GatewayError
	if errors.As(err, &gatewayError) {
		switch gatewayError.CloseCode {
		case 4004:
			return credentials
		case 4008:
			return rateLimited
		case 4014:
			return "Discord denied a required gateway intent. Check the bot's enabled intents in the Discord Developer Portal."
		}
		if gatewayError.Fatal {
			return "Discord rejected the gateway configuration. Contact your Omnara administrator."
		}
	}
	var permitWait discordIdentifyWaitError
	if errors.As(err, &permitWait) {
		return rateLimited
	}
	return "Omnara could not maintain the Discord connection. It will retry automatically."
}

func (r *DiscordRuntime) connect(
	ctx context.Context,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	claim integrationstore.IntegrationRuntimeClaim,
) error {
	credential, err := r.Secrets.ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     integrationSetup.OrgID,
			ProjectID: integrationSetup.ProjectID,
			SecretID:  integrationSetup.CredentialSecretID,
			Kind:      secrets.KindGeneric,
		},
	)
	if err != nil {
		return err
	}
	if credential.CurrentVersionID != claim.Lease.CredentialVersionID {
		return integrationstore.ErrIntegrationRuntimeLeaseLost
	}
	credentials := discord.Credentials{
		ApplicationID: integrationSetup.ProviderTenantID,
		BotUserID:     integrationSetup.ProviderAccountRef,
		BotToken:      credential.Payload[secrets.KeyValue],
	}
	recheck := func(ctx context.Context) error {
		return r.Integrations.RenewIntegrationRuntime(ctx, claim.Lease, discordRuntimeLease)
	}
	client, err := discord.NewClient(
		discord.Config{Credentials: credentials, HTTPClient: r.HTTPClient, BeforeRequest: recheck},
	)
	if err != nil {
		return err
	}
	info, err := client.GetGatewayBot(ctx)
	if err != nil {
		return err
	}
	var checkpoint *discord.Checkpoint
	if len(claim.Checkpoint) > 0 {
		checkpoint = &discord.Checkpoint{}
		if err := json.Unmarshal(claim.Checkpoint, checkpoint); err != nil {
			return fmt.Errorf("invalid persisted Discord checkpoint: %w", err)
		}
	}
	runner := r.runShard
	if runner == nil {
		runner = discord.RunShard
	}
	err = runner(
		ctx,
		discord.ShardConfig{
			Credentials:   credentials,
			ShardID:       0,
			ShardCount:    1,
			GatewayURL:    info.URL,
			HTTPClient:    r.HTTPClient,
			BeforeConnect: recheck,
			BeforeIdentify: func(ctx context.Context) error {
				return acquireDiscordIdentify(ctx, r.Redis, client, credentials.ApplicationID, 0)
			},
		},
		checkpoint,
		func(ctx context.Context, dispatch discord.Dispatch, next discord.Checkpoint) error {
			rawCheckpoint, err := json.Marshal(next)
			if err != nil {
				return err
			}
			var receipt *integrationstore.VerifiedIntegrationReceipt
			if dispatch.Type == "MESSAGE_CREATE" {
				event, ok, err := discord.NormalizeMessage(dispatch, credentials.BotUserID)
				if err != nil {
					return err
				}
				if ok && discordConversationalMessage(event) {
					raw, err := json.Marshal(dispatch)
					if err != nil {
						return err
					}
					receipt = &integrationstore.VerifiedIntegrationReceipt{
						ProjectID:     integrationSetup.ProjectID,
						IntegrationID: integrationSetup.ID,
						ReceiptKey:    "discord:" + event.Message.ID,
						Payload:       raw,
					}
				}
			}
			return r.Integrations.CommitIntegrationRuntime(ctx, claim.Lease, rawCheckpoint, receipt)
		},
	)
	var gatewayError *discord.GatewayError
	if errors.As(err, &gatewayError) && gatewayError.ResetSession {
		if resetErr := r.Integrations.CommitIntegrationRuntime(ctx, claim.Lease, nil, nil); resetErr != nil {
			return resetErr
		}
	}
	return err
}

func (r *DiscordRuntime) heartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	lease integrationstore.IntegrationRuntimeLease,
	deadline time.Time,
) {
	delay := discordRuntimeLease / 3
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			cancel()
			return
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		started := time.Now()
		if !started.Before(deadline) {
			cancel()
			return
		}
		callCtx, done := context.WithDeadline(ctx, minTime(deadline, started.Add(5*time.Second)))
		err := r.Integrations.RenewIntegrationRuntime(callCtx, lease, discordRuntimeLease)
		done()
		if err == nil {
			deadline = started.Add(discordRuntimeLease)
			delay = discordRuntimeLease / 3
			continue
		}
		if errors.Is(err, integrationstore.ErrIntegrationRuntimeLeaseLost) ||
			errors.Is(err, storeerr.ErrUnauthorized) ||
			errors.Is(err, storeerr.ErrNotFound) {
			cancel()
			return
		}
		delay = min(time.Second, delay) + time.Duration(rand.IntN(250))*time.Millisecond
	}
}

func discordReconnectDelay(err error) time.Duration {
	delay := time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
	var gatewayError *discord.GatewayError
	var apiError *discord.APIError
	var permitWait discordIdentifyWaitError
	if errors.As(err, &gatewayError) {
		if gatewayError.Fatal {
			return time.Hour
		}
		delay = max(delay, gatewayError.RetryAfter)
	}
	if errors.As(err, &apiError) {
		if apiError.Code == discord.PermanentFailure || apiError.Code == discord.ScopeMismatch {
			return time.Hour
		}
		delay = max(delay, apiError.RetryAfter)
	}
	if errors.As(err, &permitWait) {
		delay = max(delay, permitWait.After)
	}
	return min(24*time.Hour, delay)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
