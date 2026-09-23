package apps

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
	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const discordRuntimeLease = 30 * time.Second

type DiscordRuntime struct {
	Apps       *appstore.Store
	Secrets    *secretstore.Store
	Redis      identifyRedis
	HTTPClient *http.Client
	Log        *slog.Logger
	Capacity   int
	runShard   func(context.Context, discord.ShardConfig, *discord.Checkpoint, discord.CommitDispatch) error
}

func (r *DiscordRuntime) Run(ctx context.Context) error {
	if r.Apps == nil || r.Secrets == nil || r.Redis == nil {
		return errors.New("discord runtime requires app, secret and Redis stores")
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
		case appID := <-done:
			delete(active, appID)
		case <-timer.C:
			if len(active) < capacity {
				scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				refs, err := r.Apps.ListPersistentApps(scanCtx, cursor, 100)
				if err != nil {
					log.Warn("list Discord apps", "error", err)
				} else {
					for _, ref := range refs {
						cursor = ref.AppID
						if len(active) >= capacity {
							break
						}
						if active[ref.AppID] {
							continue
						}
						appSetup, err := r.Apps.GetProjectApp(
							scanCtx,
							ref.ProjectID,
							ref.AppID,
						)
						if err != nil {
							continue
						}
						secret, err := r.Secrets.GetProjectAvailableSecret(
							scanCtx,
							appSetup.OrgID,
							appSetup.ProjectID,
							appSetup.CredentialSecretID,
						)
						if err != nil {
							log.Warn("Discord credential unavailable", "app_id", appSetup.ID, "error", err)
							continue
						}
						revision := appstore.AppRuntimeRevision{
							ProjectID: appSetup.ProjectID, AppID: appSetup.ID,
							Key: "discord/shard/0", SetupRevision: appSetup.SetupRevision,
							CredentialVersionID: secret.Secret.CurrentVersionID,
						}
						started := time.Now()
						claim, found, err := r.Apps.ClaimAppRuntime(scanCtx, revision, discordRuntimeLease)
						if err != nil {
							log.Warn("claim Discord connection", "app_id", appSetup.ID, "error", err)
							continue
						}
						if !found {
							continue
						}
						active[appSetup.ID] = true
						jobs.Add(1)
						go func() {
							defer jobs.Done()
							r.run(ctx, appSetup, claim, started, log)
							done <- appSetup.ID
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
	appSetup appstore.ProjectAppRecord,
	claim appstore.AppRuntimeClaim,
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
			log.Warn("Discord connection stopped", "app_id", appSetup.ID, "error", runErr)
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer releaseCancel()
		if err := r.Apps.ReleaseAppRuntime(
			releaseCtx,
			claim.Lease,
			delay,
			failure,
		); err != nil &&
			!errors.Is(err, appstore.ErrAppRuntimeLeaseLost) {
			log.Warn("release Discord connection", "app_id", appSetup.ID, "error", err)
		}
	}()
	runErr = r.connect(ctx, appSetup, claim)
}

// Runtime failures are exposed by GET app. Persist only bounded, fixed messages;
// provider payloads, transport URLs and internal error details belong in the log.
func discordRuntimeFailureMessage(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	const credentials = "Discord rejected the bot token. Check this app's credentials."
	const rateLimited = "Discord is limiting connection requests. Omnara will retry later."
	var apiError *discord.APIError
	if errors.As(err, &apiError) {
		switch {
		case apiError.Code == discord.ScopeMismatch:
			return "Discord bot identity does not match this app's setup. Check the application ID, bot user ID and token."
		case apiError.Code == discord.RateLimited:
			return rateLimited
		case apiError.Code == discord.PermanentFailure && apiError.StatusCode == http.StatusUnauthorized:
			return credentials
		case apiError.Code == discord.PermanentFailure && apiError.StatusCode == http.StatusForbidden:
			return "Discord denied access. Check the bot's permissions and app setup."
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
	appSetup appstore.ProjectAppRecord,
	claim appstore.AppRuntimeClaim,
) error {
	credential, err := r.Secrets.ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     appSetup.OrgID,
			ProjectID: appSetup.ProjectID,
			SecretID:  appSetup.CredentialSecretID,
			Kind:      secrets.KindGeneric,
		},
	)
	if err != nil {
		return err
	}
	if credential.CurrentVersionID != claim.Lease.CredentialVersionID {
		return appstore.ErrAppRuntimeLeaseLost
	}
	credentials := discord.Credentials{
		ApplicationID: appSetup.ProviderTenantID,
		BotUserID:     appSetup.ProviderAccountRef,
		BotToken:      credential.Payload[secrets.KeyValue],
	}
	recheck := func(ctx context.Context) error {
		return r.Apps.RenewAppRuntime(ctx, claim.Lease, discordRuntimeLease)
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
			var receipt *appstore.VerifiedAppReceipt
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
					receipt = &appstore.VerifiedAppReceipt{
						ProjectID:  appSetup.ProjectID,
						AppID:      appSetup.ID,
						ReceiptKey: "discord:" + event.Message.ID,
						Payload:    raw,
					}
				}
			}
			return r.Apps.CommitAppRuntime(ctx, claim.Lease, rawCheckpoint, receipt)
		},
	)
	var gatewayError *discord.GatewayError
	if errors.As(err, &gatewayError) && gatewayError.ResetSession {
		if resetErr := r.Apps.CommitAppRuntime(ctx, claim.Lease, nil, nil); resetErr != nil {
			return resetErr
		}
	}
	return err
}

func (r *DiscordRuntime) heartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	lease appstore.AppRuntimeLease,
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
		err := r.Apps.RenewAppRuntime(callCtx, lease, discordRuntimeLease)
		done()
		if err == nil {
			deadline = started.Add(discordRuntimeLease)
			delay = discordRuntimeLease / 3
			continue
		}
		if errors.Is(err, appstore.ErrAppRuntimeLeaseLost) ||
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
