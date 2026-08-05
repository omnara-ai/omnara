package skills

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
)

type fakeInbox struct {
	mu         sync.Mutex
	attempted  []capturedInboxCall
	published  []capturedInboxCall
	publishCB  func(call capturedInboxCall)
	publishErr func(call capturedInboxCall) error
	err        error
}

type machineWakerFunc func(context.Context, uuid.UUID, uuid.UUID) (bool, error)

func (f machineWakerFunc) WakeMachine(
	ctx context.Context,
	orgID, machineID uuid.UUID,
) (bool, error) {
	return f(ctx, orgID, machineID)
}

type capturedInboxCall struct {
	machineID    uuid.UUID
	kind         daemonprotocol.MessageType
	msg          daemonprotocol.Message
	replyChannel string
}

func (f *fakeInbox) Publish(
	_ context.Context,
	machineID uuid.UUID,
	kind daemonprotocol.MessageType,
	payload json.RawMessage,
	opts notifications.PublishOptions,
) error {
	var msg daemonprotocol.Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	call := capturedInboxCall{machineID: machineID, kind: kind, msg: msg, replyChannel: opts.ReplyChannel}
	f.mu.Lock()
	f.attempted = append(f.attempted, call)
	cb := f.publishCB
	publishErr := f.publishErr
	err := f.err
	f.mu.Unlock()
	if publishErr != nil {
		err = publishErr(call)
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.published = append(f.published, call)
	f.mu.Unlock()
	if cb != nil {
		cb(call)
	}
	return nil
}

func (f *fakeInbox) snapshot() []capturedInboxCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedInboxCall, len(f.published))
	copy(out, f.published)
	return out
}

func (f *fakeInbox) snapshotAttempts() []capturedInboxCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedInboxCall, len(f.attempted))
	copy(out, f.attempted)
	return out
}

type fakeReplyBus struct {
	mu          sync.Mutex
	subscribers map[string][]func(context.Context, []byte)
}

func newFakeReplyBus() *fakeReplyBus {
	return &fakeReplyBus{subscribers: map[string][]func(context.Context, []byte){}}
}

func (b *fakeReplyBus) SubscribeChannel(
	_ context.Context,
	channel string,
	handler func(context.Context, []byte),
) (notifications.Subscription, error) {
	b.mu.Lock()
	b.subscribers[channel] = append(b.subscribers[channel], handler)
	b.mu.Unlock()
	return fakeSubscription{cancel: func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		handlers := b.subscribers[channel]
		filtered := handlers[:0]
		for _, h := range handlers {
			if &h != &handler {
				filtered = append(filtered, h)
			}
		}
		b.subscribers[channel] = filtered
	}}, nil
}

func (b *fakeReplyBus) deliver(channel string, payload []byte) {
	b.mu.Lock()
	handlers := append([]func(context.Context, []byte){}, b.subscribers[channel]...)
	b.mu.Unlock()
	for _, h := range handlers {
		h(context.Background(), payload)
	}
}

type fakeSubscription struct{ cancel func() }

func (s fakeSubscription) Unsubscribe() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

const testArchiveDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var testDownloadSigningKey = []byte("0123456789abcdef0123456789abcdef")

func newTestBroadcaster(t *testing.T, inbox *fakeInbox, bus *fakeReplyBus) *Broadcaster {
	t.Helper()
	b, err := NewBroadcaster(
		inbox,
		bus,
		machineWakerFunc(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil }),
		testDownloadSigningKey,
	)
	if err != nil {
		t.Fatalf("new broadcaster: %v", err)
	}
	return b
}

func TestBroadcastAndAwaitRetriesWakeAcrossSleepTransition(t *testing.T) {
	inbox := &fakeInbox{}
	bus := newFakeReplyBus()
	orgID := uuid.New()
	machineID := uuid.New()
	var attempts atomic.Int64
	wakeCalls := make(chan [2]uuid.UUID, 2)
	inbox.publishErr = func(capturedInboxCall) error {
		if attempts.Add(1) <= 2 {
			return notifications.ErrDaemonOffline
		}
		return nil
	}
	inbox.publishCB = func(call capturedInboxCall) {
		bus.deliver(call.replyChannel, mustMarshalSkillReply(t, skillReportReply{
			RequestID: call.msg.SkillOffer.RequestID,
			SkillID:   call.msg.SkillOffer.SkillID,
			State:     daemonprotocol.SkillStateReady,
		}))
	}
	broadcaster, err := NewBroadcaster(
		inbox,
		bus,
		machineWakerFunc(func(_ context.Context, gotOrgID, gotMachineID uuid.UUID) (bool, error) {
			wakeCalls <- [2]uuid.UUID{gotOrgID, gotMachineID}
			return true, nil
		}),
		testDownloadSigningKey,
	)
	if err != nil {
		t.Fatalf("new broadcaster: %v", err)
	}
	broadcaster.publishRetryInitial = time.Millisecond
	outcomes, err := broadcaster.BroadcastAndAwait(
		context.Background(),
		"skl_test",
		"skr_test",
		testArchiveDigest,
		[]BroadcastTarget{{
			OrgID:      orgID,
			MachineID:  machineID,
			MachineRef: "mchr-Z",
		}},
		time.Second,
	)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(outcomes) != 1 || !outcomes[0].IsReady() {
		t.Fatalf("outcomes = %+v, want ready", outcomes)
	}
	publishAttempts := inbox.snapshotAttempts()
	if len(publishAttempts) != 3 {
		t.Fatalf("publish attempts = %d, want 3", len(publishAttempts))
	}
	for range 2 {
		select {
		case wakeCall := <-wakeCalls:
			if wakeCall != [2]uuid.UUID{orgID, machineID} {
				t.Fatalf("wake call = %v, want [%s %s]", wakeCall, orgID, machineID)
			}
		default:
			t.Fatal("offline target was not woken after each failed publish")
		}
	}
	firstOffer := publishAttempts[0].msg.SkillOffer
	secondOffer := publishAttempts[1].msg.SkillOffer
	if firstOffer.RequestID != secondOffer.RequestID || firstOffer.DownloadToken != secondOffer.DownloadToken {
		t.Fatalf("retried offer changed identity: first=%+v second=%+v", firstOffer, secondOffer)
	}
	if publishAttempts[0].replyChannel != publishAttempts[1].replyChannel {
		t.Fatalf("retried offer changed reply channel")
	}
}

func TestBroadcastAndAwaitDoesNotRetryOfflineTargetWithoutWake(t *testing.T) {
	inbox := &fakeInbox{err: notifications.ErrDaemonOffline}
	bus := newFakeReplyBus()
	var wakeCalls atomic.Int64
	broadcaster, err := NewBroadcaster(
		inbox,
		bus,
		machineWakerFunc(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			wakeCalls.Add(1)
			return false, nil
		}),
		testDownloadSigningKey,
	)
	if err != nil {
		t.Fatalf("new broadcaster: %v", err)
	}
	outcomes, err := broadcaster.BroadcastAndAwait(
		context.Background(),
		"skl_test",
		"skr_test",
		testArchiveDigest,
		[]BroadcastTarget{{OrgID: uuid.New(), MachineID: uuid.New(), MachineRef: "mchr-Z"}},
		time.Second,
	)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].State != BroadcastStateOffline {
		t.Fatalf("outcomes = %+v, want offline", outcomes)
	}
	if attempts := inbox.snapshotAttempts(); len(attempts) != 1 {
		t.Fatalf("publish attempts = %d, want 1", len(attempts))
	}
	if got := wakeCalls.Load(); got != 1 {
		t.Fatalf("wake calls = %d, want 1", got)
	}
}

func TestBroadcastAndAwaitEmptyTargetsDoesNotPublish(t *testing.T) {
	inbox := &fakeInbox{}
	bus := newFakeReplyBus()
	outcomes, err := newTestBroadcaster(t, inbox, bus).BroadcastAndAwait(
		context.Background(),
		"skl_test",
		"skr_test",
		testArchiveDigest,
		nil,
		time.Second,
	)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if outcomes != nil {
		t.Fatalf("outcomes = %+v, want nil", outcomes)
	}
	if attempts := inbox.snapshotAttempts(); len(attempts) != 0 {
		t.Fatalf("publish attempts = %d, want 0", len(attempts))
	}
}

func TestPublishUntilOnlineRetriesUntilContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int64
	inbox := &fakeInbox{}
	inbox.publishErr = func(capturedInboxCall) error {
		if attempts.Add(1) == 6 {
			cancel()
		}
		return notifications.ErrDaemonOffline
	}
	broadcaster := newTestBroadcaster(t, inbox, newFakeReplyBus())
	broadcaster.publishRetryInitial = time.Nanosecond

	err := broadcaster.publishUntilOnline(
		ctx,
		BroadcastTarget{OrgID: uuid.New(), MachineID: uuid.New()},
		json.RawMessage(`{"type":"skill_offer","payload":{}}`),
		"omnara:skillreply:test",
	)

	if !errors.Is(err, notifications.ErrDaemonOffline) {
		t.Fatalf("error = %v, want daemon offline", err)
	}
	if got := attempts.Load(); got != 6 {
		t.Fatalf("publish attempts = %d, want 6", got)
	}
}

func TestPublishUntilOnlineContextErrorIsNotTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inbox := &fakeInbox{}
	inbox.publishErr = func(capturedInboxCall) error {
		cancel()
		return context.Canceled
	}
	broadcaster := newTestBroadcaster(t, inbox, newFakeReplyBus())
	broadcaster.publishRetryInitial = time.Millisecond

	err := broadcaster.publishUntilOnline(
		ctx,
		BroadcastTarget{OrgID: uuid.New(), MachineID: uuid.New()},
		json.RawMessage(`{"type":"skill_offer","payload":{}}`),
		"omnara:skillreply:test",
	)

	if !errors.Is(err, notifications.ErrDaemonOffline) {
		t.Fatalf("error = %v, want daemon offline", err)
	}
}

func TestBroadcastAndAwaitHappyPathMatchesReportsToTargets(t *testing.T) {
	inbox := &fakeInbox{}
	bus := newFakeReplyBus()
	machineA := uuid.New()
	machineB := uuid.New()
	orgID := uuid.New()
	inbox.publishCB = func(call capturedInboxCall) {
		bus.deliver(call.replyChannel, mustMarshalSkillReply(t, skillReportReply{
			RequestID: call.msg.SkillOffer.RequestID,
			SkillID:   call.msg.SkillOffer.SkillID,
			State:     daemonprotocol.SkillStateReady,
		}))
	}
	outcomes, err := newTestBroadcaster(t, inbox, bus).BroadcastAndAwait(
		context.Background(),
		"skl_test",
		"skr_test",
		testArchiveDigest,
		[]BroadcastTarget{
			{OrgID: orgID, MachineID: machineA, MachineRef: "mchr-A"},
			{OrgID: orgID, MachineID: machineB, MachineRef: "mchr-B"},
		},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("expected 2 outcomes, got %d", len(outcomes))
	}
	if !outcomes[0].IsReady() || !outcomes[1].IsReady() {
		t.Fatalf("expected ready outcomes, got %+v", outcomes)
	}
	pubs := inbox.snapshot()
	if len(pubs) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(pubs))
	}
	if pubs[0].msg.SkillOffer.RevisionID != "skr_test" || pubs[1].msg.SkillOffer.RevisionID != "skr_test" {
		t.Fatalf("offers must carry the revision public id: %+v", pubs)
	}
	if pubs[0].msg.SkillOffer.DownloadToken == pubs[1].msg.SkillOffer.DownloadToken {
		t.Fatal("offers for different machines must have distinct download capabilities")
	}
	for _, pub := range pubs {
		machineID, err := publicid.Encode(publicid.KindMachine, pub.machineID)
		if err != nil {
			t.Fatalf("encode machine id: %v", err)
		}
		offer := pub.msg.SkillOffer
		if err := VerifyDownloadToken(
			testDownloadSigningKey,
			offer.DownloadToken,
			offer.SkillID,
			offer.RevisionID,
			machineID,
			offer.DownloadExpiresAt,
			time.Now(),
		); err != nil {
			t.Fatalf("verify machine-bound offer: %v", err)
		}
	}
	if pubs[0].replyChannel != pubs[1].replyChannel {
		t.Fatalf(
			"publishes should share the per-invocation reply channel, got %q vs %q",
			pubs[0].replyChannel,
			pubs[1].replyChannel,
		)
	}
}

func TestBroadcastAndAwaitPartialTimeoutFoldsUnreporters(t *testing.T) {
	inbox := &fakeInbox{}
	bus := newFakeReplyBus()
	machineA := uuid.New()
	machineB := uuid.New()
	orgID := uuid.New()
	inbox.publishCB = func(call capturedInboxCall) {
		if call.machineID == machineA {
			bus.deliver(call.replyChannel, mustMarshalSkillReply(t, skillReportReply{
				RequestID: call.msg.SkillOffer.RequestID,
				SkillID:   call.msg.SkillOffer.SkillID,
				State:     daemonprotocol.SkillStateReady,
			}))
		}
	}
	outcomes, err := newTestBroadcaster(t, inbox, bus).BroadcastAndAwait(
		context.Background(),
		"skl_test",
		"skr_test",
		testArchiveDigest,
		[]BroadcastTarget{
			{OrgID: orgID, MachineID: machineA, MachineRef: "mchr-A"},
			{OrgID: orgID, MachineID: machineB, MachineRef: "mchr-B"},
		},
		120*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	var sawReady, sawTimedOut bool
	for _, o := range outcomes {
		switch o.Target.MachineID {
		case machineA:
			if o.State != BroadcastStateReady {
				t.Fatalf("machineA outcome = %+v, want ready", o)
			}
			sawReady = true
		case machineB:
			if o.State != BroadcastStateTimedOut {
				t.Fatalf("machineB outcome = %+v, want timed_out", o)
			}
			sawTimedOut = true
		}
	}
	if !sawReady || !sawTimedOut {
		t.Fatalf("expected both ready and timed_out outcomes, got %+v", outcomes)
	}
}

func TestBroadcastAndAwaitTranslatesDaemonOffline(t *testing.T) {
	inbox := &fakeInbox{err: notifications.ErrDaemonOffline}
	bus := newFakeReplyBus()
	wakeErr := errors.New("provider wake failed")
	broadcaster, err := NewBroadcaster(
		inbox,
		bus,
		machineWakerFunc(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, wakeErr
		}),
		testDownloadSigningKey,
	)
	if err != nil {
		t.Fatalf("new broadcaster: %v", err)
	}
	outcomes, err := broadcaster.BroadcastAndAwait(
		context.Background(),
		"skl_test",
		"skr_test",
		testArchiveDigest,
		[]BroadcastTarget{{OrgID: uuid.New(), MachineID: uuid.New(), MachineRef: "mchr-Z"}},
		200*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].State != BroadcastStateOffline {
		t.Fatalf("expected single offline outcome, got %+v", outcomes)
	}
	if !strings.Contains(outcomes[0].Error, wakeErr.Error()) {
		t.Fatalf("offline outcome error = %q, want wake failure", outcomes[0].Error)
	}
}

func TestBroadcastAndAwaitTransportErrorBecomesPerMachineFailure(t *testing.T) {
	inbox := &fakeInbox{err: errors.New("redis down")}
	bus := newFakeReplyBus()
	outcomes, err := newTestBroadcaster(t, inbox, bus).BroadcastAndAwait(
		context.Background(),
		"skl_test",
		"skr_test",
		testArchiveDigest,
		[]BroadcastTarget{{OrgID: uuid.New(), MachineID: uuid.New(), MachineRef: "mchr-Q"}},
		200*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].State != BroadcastStateTransport {
		t.Fatalf("expected single transport outcome, got %+v", outcomes)
	}
	if !strings.Contains(outcomes[0].Error, "redis down") {
		t.Fatalf("expected error to include transport detail, got %q", outcomes[0].Error)
	}
}

func TestNewBroadcasterRejectsMissingConfig(t *testing.T) {
	bus := newFakeReplyBus()
	waker := machineWakerFunc(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
		return true, nil
	})
	if _, err := NewBroadcaster(nil, bus, waker, testDownloadSigningKey); err == nil {
		t.Fatal("nil inbox should error")
	}
	if _, err := NewBroadcaster(&fakeInbox{}, nil, waker, testDownloadSigningKey); err == nil {
		t.Fatal("nil reply bus should error")
	}
	if _, err := NewBroadcaster(&fakeInbox{}, bus, nil, testDownloadSigningKey); err == nil {
		t.Fatal("nil machine waker should error")
	}
	if _, err := NewBroadcaster(&fakeInbox{}, bus, waker, nil); err == nil {
		t.Fatal("nil signing key should error")
	}
}

func mustMarshalSkillReply(t *testing.T, r skillReportReply) []byte {
	t.Helper()
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal skill reply: %v", err)
	}
	return out
}
