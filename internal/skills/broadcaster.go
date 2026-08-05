package skills

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const (
	BroadcastStateReady       = "ready"
	BroadcastStateFailed      = "failed"
	BroadcastStateUnreachable = "unreachable"
	BroadcastStateTimedOut    = "timed_out"
	BroadcastStateOffline     = "offline"
	BroadcastStateTransport   = "transport"
	publishRetryInitialDelay  = time.Second
	publishRetryMaxDelay      = 5 * time.Second
)

type DaemonInboxPublisher interface {
	Publish(
		ctx context.Context,
		machineID uuid.UUID,
		kind daemonprotocol.MessageType,
		payload json.RawMessage,
		opts notifications.PublishOptions,
	) error
}

type ReplyBus interface {
	SubscribeChannel(
		ctx context.Context,
		channel string,
		handler func(context.Context, []byte),
	) (notifications.Subscription, error)
}

type MachineWaker interface {
	WakeMachine(ctx context.Context, orgID, machineID uuid.UUID) (bool, error)
}

// BroadcastTarget identifies a single machine for a skill offer.
type BroadcastTarget struct {
	OrgID      uuid.UUID
	MachineID  uuid.UUID
	MachineRef string
}

// BroadcastOutcome is one machine's terminal skill install state.
type BroadcastOutcome struct {
	Target    BroadcastTarget
	State     string
	ErrorCode string
	Error     string
}

func (o BroadcastOutcome) IsReady() bool {
	return o.State == BroadcastStateReady
}

// Broadcaster sends skill offers and awaits daemon reports.
type Broadcaster struct {
	inbox               DaemonInboxPublisher
	reply               ReplyBus
	waker               MachineWaker
	signingKey          []byte
	publishRetryInitial time.Duration
}

// NewBroadcaster wires a broadcaster.
func NewBroadcaster(
	inbox DaemonInboxPublisher,
	reply ReplyBus,
	waker MachineWaker,
	signingKey []byte,
) (*Broadcaster, error) {
	if inbox == nil {
		return nil, errors.New("skill broadcaster requires a daemon inbox publisher")
	}
	if reply == nil {
		return nil, errors.New("skill broadcaster requires a reply bus")
	}
	if waker == nil {
		return nil, errors.New("skill broadcaster requires a machine waker")
	}
	if len(signingKey) == 0 {
		return nil, errors.New("skill broadcaster requires a download signing key")
	}
	keyCopy := append([]byte(nil), signingKey...)
	return &Broadcaster{
		inbox:               inbox,
		reply:               reply,
		waker:               waker,
		signingKey:          keyCopy,
		publishRetryInitial: publishRetryInitialDelay,
	}, nil
}

// BroadcastAndAwait sends a skill offer to each target and waits for reports.
//
// revisionPublicID selects which archive revision each daemon downloads.
// archiveDigest is that revision's expected "sha256:<hex>" content digest as
// recorded by the control plane. It travels in the SkillOffer payload (which
// moves over the authenticated daemon WS channel) so each daemon can verify
// the bytes it downloads match what the control plane intended.
func (b *Broadcaster) BroadcastAndAwait(
	ctx context.Context,
	skillPublicID string,
	revisionPublicID string,
	archiveDigest string,
	targets []BroadcastTarget,
	timeout time.Duration,
) ([]BroadcastOutcome, error) {
	if b == nil {
		return nil, errors.New("skill broadcaster is not configured")
	}
	if skillPublicID == "" {
		return nil, errors.New("skill public id is required")
	}
	if revisionPublicID == "" {
		return nil, errors.New("skill revision public id is required")
	}
	if archiveDigest == "" {
		return nil, errors.New("skill archive digest is required")
	}
	if timeout <= 0 {
		return nil, errors.New("broadcast timeout must be positive")
	}

	type pending struct {
		target    BroadcastTarget
		requestID string
		published bool
	}
	pendings := make([]pending, 0, len(targets))
	seen := make(map[uuid.UUID]struct{}, len(targets))
	for _, t := range targets {
		if t.OrgID == uuid.Nil {
			return nil, errors.New("broadcast target org id is required")
		}
		if t.MachineID == uuid.Nil {
			return nil, errors.New("broadcast target machine id is required")
		}
		if _, dup := seen[t.MachineID]; dup {
			continue
		}
		seen[t.MachineID] = struct{}{}
		rid, err := newBroadcastRequestID()
		if err != nil {
			return nil, err
		}
		pendings = append(pendings, pending{target: t, requestID: rid})
	}
	if len(pendings) == 0 {
		return nil, nil
	}

	replyToken, err := newReplyToken()
	if err != nil {
		return nil, err
	}
	replyChannel := "omnara:skillreply:" + replyToken
	broadcastCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reports := make(chan skillReportReply, len(pendings))
	sub, err := b.reply.SubscribeChannel(broadcastCtx, replyChannel, func(_ context.Context, payload []byte) {
		var msg skillReportReply
		if err := json.Unmarshal(payload, &msg); err != nil {
			return
		}
		select {
		case reports <- msg:
		default:
		}
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe reply channel: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	outcomes := make(map[uuid.UUID]BroadcastOutcome, len(pendings))
	byRequestID := make(map[string]*pending, len(pendings))
	for i := range pendings {
		p := &pendings[i]
		byRequestID[p.requestID] = p
	}
	retryResults := make(chan publishRetryResult, len(pendings))
	for i := range pendings {
		p := &pendings[i]
		machinePublicID, err := publicid.Encode(publicid.KindMachine, p.target.MachineID)
		if err != nil {
			outcomes[p.target.MachineID] = BroadcastOutcome{
				Target: p.target,
				State:  BroadcastStateTransport,
				Error:  fmt.Sprintf("encode machine id: %v", err),
			}
			continue
		}
		downloadToken, downloadExpiresAt, err := MintDownloadToken(
			b.signingKey,
			skillPublicID,
			revisionPublicID,
			machinePublicID,
			time.Now(),
		)
		if err != nil {
			outcomes[p.target.MachineID] = BroadcastOutcome{
				Target: p.target,
				State:  BroadcastStateTransport,
				Error:  err.Error(),
			}
			continue
		}
		msg := daemonprotocol.Message{
			Type: daemonprotocol.MessageSkillOffer,
			SkillOffer: &daemonprotocol.SkillOffer{
				RequestID:         p.requestID,
				SkillID:           skillPublicID,
				RevisionID:        revisionPublicID,
				DownloadToken:     downloadToken,
				DownloadExpiresAt: downloadExpiresAt,
				Digest:            archiveDigest,
			},
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			outcomes[p.target.MachineID] = BroadcastOutcome{
				Target: p.target,
				State:  BroadcastStateTransport,
				Error:  fmt.Sprintf("encode skill_offer: %v", err),
			}
			continue
		}
		go func(index int, target BroadcastTarget, offer json.RawMessage) {
			retryResults <- publishRetryResult{
				index: index,
				err: b.publishUntilOnline(
					broadcastCtx,
					target,
					offer,
					replyChannel,
				),
			}
		}(i, p.target, payload)
	}

awaitLoop:
	for len(outcomes) < len(pendings) {
		select {
		case <-broadcastCtx.Done():
			break awaitLoop
		case result := <-retryResults:
			p := &pendings[result.index]
			if _, already := outcomes[p.target.MachineID]; already {
				continue
			}
			if result.err == nil {
				p.published = true
				continue
			}
			state := BroadcastStateTransport
			message := result.err.Error()
			if errors.Is(result.err, notifications.ErrDaemonOffline) {
				state = BroadcastStateOffline
			}
			outcomes[p.target.MachineID] = BroadcastOutcome{
				Target: p.target,
				State:  state,
				Error:  message,
			}
		case rep := <-reports:
			p, ok := byRequestID[rep.RequestID]
			if !ok || p == nil {
				continue
			}
			if _, already := outcomes[p.target.MachineID]; already {
				continue
			}
			outcomes[p.target.MachineID] = BroadcastOutcome{
				Target:    p.target,
				State:     normalizeReportState(rep.State),
				ErrorCode: rep.ErrorCode,
				Error:     rep.Error,
			}
		}
	}

	result := make([]BroadcastOutcome, 0, len(pendings))
	for i := range pendings {
		p := &pendings[i]
		if o, ok := outcomes[p.target.MachineID]; ok {
			result = append(result, o)
			continue
		}
		if !p.published {
			result = append(result, BroadcastOutcome{
				Target: p.target,
				State:  BroadcastStateOffline,
				Error:  "daemon did not come online before timeout",
			})
			continue
		}
		result = append(result, BroadcastOutcome{
			Target: p.target,
			State:  BroadcastStateTimedOut,
			Error:  "no skill_report received before timeout",
		})
	}
	return result, nil
}

type publishRetryResult struct {
	index int
	err   error
}

func (b *Broadcaster) publishUntilOnline(
	ctx context.Context,
	target BroadcastTarget,
	payload json.RawMessage,
	replyChannel string,
) error {
	delay := b.publishRetryInitial
	for {
		if ctx.Err() != nil {
			return notifications.ErrDaemonOffline
		}
		err := b.inbox.Publish(
			ctx,
			target.MachineID,
			"skill_offer",
			payload,
			notifications.PublishOptions{ReplyChannel: replyChannel},
		)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return notifications.ErrDaemonOffline
		}
		if !errors.Is(err, notifications.ErrDaemonOffline) {
			return err
		}
		shouldRetry, err := b.waker.WakeMachine(ctx, target.OrgID, target.MachineID)
		if err != nil {
			return fmt.Errorf("%w: wake machine: %w", notifications.ErrDaemonOffline, err)
		}
		if !shouldRetry {
			return notifications.ErrDaemonOffline
		}
		select {
		case <-ctx.Done():
			return notifications.ErrDaemonOffline
		case <-time.After(delay):
		}
		delay = min(delay*2, publishRetryMaxDelay)
	}
}

type skillReportReply struct {
	MachineID string                    `json:"machine_id"`
	RequestID string                    `json:"request_id"`
	SkillID   string                    `json:"skill_id"`
	State     daemonprotocol.SkillState `json:"state"`
	ErrorCode string                    `json:"error_code,omitempty"`
	Error     string                    `json:"error,omitempty"`
}

func normalizeReportState(state daemonprotocol.SkillState) string {
	switch state {
	case daemonprotocol.SkillStateReady:
		return BroadcastStateReady
	case daemonprotocol.SkillStateFailed:
		return BroadcastStateFailed
	case daemonprotocol.SkillStateUnreachable:
		return BroadcastStateUnreachable
	case daemonprotocol.SkillStateTimeout:
		return BroadcastStateTimedOut
	}
	if state == "" {
		return BroadcastStateFailed
	}
	return string(state)
}

func newBroadcastRequestID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate skill_offer request id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func newReplyToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate reply channel token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
