package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
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

// DiscordRuntime owns provider sessions, not agents or app behavior. Each shard
// can live on a different worker. The database checkpoint advances together with
// raw inbox receipt admission; AppConsumer handles routing and launches later.
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
	active := map[string]bool{}
	done := make(chan string, capacity)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	cursor := uuid.Nil
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case key := <-done:
			delete(active, key)
		case <-timer.C:
			if len(active) < capacity {
				scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				refs, err := r.Integrations.ListPersistentApps(scanCtx, cursor, 100)
				if err != nil {
					log.Warn("list Discord apps", "error", err)
				} else {
					for _, ref := range refs {
						cursor = ref.AppID
						if len(active) >= capacity {
							break
						}
						appSetup, err := r.Integrations.GetProjectApp(
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
						var config struct {
							ShardCount int `json:"shard_count"`
						}
						if json.Unmarshal(appSetup.ProviderConfig, &config) != nil {
							continue
						}
						if config.ShardCount == 0 {
							config.ShardCount = 1
						}
						for shard := 0; shard < config.ShardCount && len(active) < capacity; shard++ {
							key := appSetup.ID.String() + ":" + strconv.Itoa(shard)
							if active[key] {
								continue
							}
							revision := integrationstore.AppRuntimeRevision{
								ProjectID:           appSetup.ProjectID,
								AppID:               appSetup.ID,
								Key:                 "discord/shard/" + strconv.Itoa(shard),
								SetupRevision:       appSetup.SetupRevision,
								CredentialVersionID: secret.Secret.CurrentVersionID,
							}
							started := time.Now()
							claim, found, err := r.Integrations.ClaimAppRuntime(
								scanCtx,
								revision,
								discordRuntimeLease,
							)
							if err != nil {
								log.Warn(
									"claim Discord shard",
									"app_id",
									appSetup.ID,
									"shard",
									shard,
									"error",
									err,
								)
								continue
							}
							if !found {
								continue
							}
							active[key] = true
							jobs.Add(1)
							go func() {
								defer jobs.Done()
								r.run(ctx, appSetup, claim, shard, config.ShardCount, started, log)
								done <- key
							}()
						}
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
	appSetup integrationstore.ProjectAppRecord,
	claim integrationstore.AppRuntimeClaim,
	shard, count int,
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
		failure := ""
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			failure = runErr.Error()
			log.Warn("Discord shard stopped", "app_id", appSetup.ID, "shard", shard, "error", runErr)
		}
		if len(failure) > 4000 {
			failure = failure[:4000]
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer releaseCancel()
		if err := r.Integrations.ReleaseAppRuntime(
			releaseCtx,
			claim.Lease,
			delay,
			failure,
		); err != nil &&
			!errors.Is(err, integrationstore.ErrAppRuntimeLeaseLost) {
			log.Warn("release Discord shard", "app_id", appSetup.ID, "error", err)
		}
	}()
	runErr = r.connect(ctx, appSetup, claim, shard, count)
}

func (r *DiscordRuntime) connect(
	ctx context.Context,
	appSetup integrationstore.ProjectAppRecord,
	claim integrationstore.AppRuntimeClaim,
	shard, count int,
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
		return integrationstore.ErrAppRuntimeLeaseLost
	}
	credentials := discord.Credentials{
		ApplicationID: appSetup.ProviderTenantID,
		BotUserID:     appSetup.ProviderAccountRef,
		BotToken:      credential.Payload[secrets.KeyValue],
	}
	recheck := func(ctx context.Context) error {
		return r.Integrations.RenewAppRuntime(ctx, claim.Lease, discordRuntimeLease)
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
			ShardID:       shard,
			ShardCount:    count,
			GatewayURL:    info.URL,
			HTTPClient:    r.HTTPClient,
			BeforeConnect: recheck,
			BeforeIdentify: func(ctx context.Context) error {
				return acquireDiscordIdentify(ctx, r.Redis, client, credentials.ApplicationID, shard)
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
				if ok && !event.Self && !event.Automated {
					raw, err := json.Marshal(dispatch)
					if err != nil {
						return err
					}
					receipt = &integrationstore.VerifiedIntegrationReceipt{
						ProjectID:  appSetup.ProjectID,
						AppID:      appSetup.ID,
						ReceiptKey: "discord:" + event.Message.ID,
						Payload:    raw,
					}
				}
			}
			return r.Integrations.CommitAppRuntime(ctx, claim.Lease, rawCheckpoint, receipt)
		},
	)
	var gatewayError *discord.GatewayError
	if errors.As(err, &gatewayError) && gatewayError.ResetSession {
		if resetErr := r.Integrations.CommitAppRuntime(ctx, claim.Lease, nil, nil); resetErr != nil {
			return resetErr
		}
	}
	return err
}

func (r *DiscordRuntime) heartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	lease integrationstore.AppRuntimeLease,
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
		err := r.Integrations.RenewAppRuntime(callCtx, lease, discordRuntimeLease)
		done()
		if err == nil {
			deadline = started.Add(discordRuntimeLease)
			delay = discordRuntimeLease / 3
			continue
		}
		if errors.Is(err, integrationstore.ErrAppRuntimeLeaseLost) ||
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
	switch {
	case errors.As(err, &gatewayError):
		if gatewayError.Fatal {
			return time.Hour
		}
		delay = max(delay, gatewayError.RetryAfter)
	case errors.As(err, &apiError):
		if apiError.Code == discord.PermanentFailure {
			// A bad credential or setup must not hammer shared provider egress.
			// Updating the app or credential bypasses this delay.
			return time.Hour
		}
		delay = max(delay, apiError.RetryAfter)
	case errors.As(err, &permitWait):
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
