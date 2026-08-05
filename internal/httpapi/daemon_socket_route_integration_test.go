//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

type daemonSocketRouteTestSubscription struct {
	cancel func()
}

func (s daemonSocketRouteTestSubscription) Unsubscribe() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

type daemonWakeupHandler func(context.Context, notifications.WakeupMessage)

type daemonSocketRouteKeyWrapper struct {
	secrets.KeyWrapper
	failNextUnwrap bool
}

func (w *daemonSocketRouteKeyWrapper) UnwrapDataKey(
	ctx context.Context,
	wrapped secrets.WrappedDataKey,
	associatedData []byte,
) ([]byte, error) {
	if w.failNextUnwrap {
		w.failNextUnwrap = false
		return nil, errors.New("secret key unwrap unavailable")
	}
	return w.KeyWrapper.UnwrapDataKey(ctx, wrapped, associatedData)
}

type daemonSocketRouteTestBus struct {
	mu       sync.Mutex
	handlers map[uuid.UUID]map[*daemonWakeupHandler]daemonWakeupHandler
}

var (
	daemonSocketRouteReplicaID = uuid.MustParse("20000000-0000-0000-0000-000000000001")
	daemonSocketLiveReplicaID  = uuid.MustParse("20000000-0000-0000-0000-000000000002")
)

func newDaemonSocketRouteTestBus() *daemonSocketRouteTestBus {
	return &daemonSocketRouteTestBus{
		handlers: map[uuid.UUID]map[*daemonWakeupHandler]daemonWakeupHandler{},
	}
}

func (b *daemonSocketRouteTestBus) PublishDaemonReplicaWakeup(
	_ context.Context,
	replicaID uuid.UUID,
	wakeup notifications.WakeupMessage,
) error {
	b.mu.Lock()
	handlers := make(
		[]daemonWakeupHandler,
		0,
		len(b.handlers[replicaID]),
	)
	for _, handler := range b.handlers[replicaID] {
		handlers = append(handlers, handler)
	}
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(context.Background(), wakeup)
	}
	return nil
}

func (b *daemonSocketRouteTestBus) PublishAgentEventWakeup(context.Context, uuid.UUID) error {
	return nil
}

func (b *daemonSocketRouteTestBus) PublishWorkerControl(
	context.Context,
	uuid.UUID,
	notifications.WorkerControl,
) error {
	return nil
}

func (b *daemonSocketRouteTestBus) SubscribeDaemonReplicaWakeups(
	_ context.Context,
	replicaID uuid.UUID,
	handler func(context.Context, notifications.WakeupMessage),
) (notifications.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handlers == nil {
		b.handlers = map[uuid.UUID]map[*daemonWakeupHandler]daemonWakeupHandler{}
	}
	if b.handlers[replicaID] == nil {
		b.handlers[replicaID] = map[*daemonWakeupHandler]daemonWakeupHandler{}
	}
	typedHandler := daemonWakeupHandler(handler)
	key := &typedHandler
	b.handlers[replicaID][key] = typedHandler
	return daemonSocketRouteTestSubscription{cancel: func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.handlers[replicaID], key)
	}}, nil
}

func (b *daemonSocketRouteTestBus) SubscribeDaemonReplicaInbox(context.Context, uuid.UUID, func(context.Context, []byte)) (notifications.Subscription, error) {
	return daemonSocketRouteTestSubscription{}, nil
}

type daemonSocketRouteFailingPublishBus struct{}

func (daemonSocketRouteFailingPublishBus) PublishDaemonReplicaWakeup(
	context.Context,
	uuid.UUID,
	notifications.WakeupMessage,
) error {
	return errors.New("redis publish unavailable")
}

func (daemonSocketRouteFailingPublishBus) PublishAgentEventWakeup(context.Context, uuid.UUID) error {
	return nil
}

func (daemonSocketRouteFailingPublishBus) PublishWorkerControl(
	context.Context,
	uuid.UUID,
	notifications.WorkerControl,
) error {
	return nil
}

func (daemonSocketRouteFailingPublishBus) SubscribeDaemonReplicaWakeups(
	context.Context,
	uuid.UUID,
	func(context.Context, notifications.WakeupMessage),
) (notifications.Subscription, error) {
	return daemonSocketRouteTestSubscription{}, nil
}
func (daemonSocketRouteFailingPublishBus) SubscribeDaemonReplicaInbox(context.Context, uuid.UUID, func(context.Context, []byte)) (notifications.Subscription, error) {
	return daemonSocketRouteTestSubscription{}, nil
}

type daemonSocketRouteTestPresence struct {
	mu             sync.Mutex
	records        map[uuid.UUID]notifications.DaemonPresence
	runtimeRecords map[uuid.UUID]notifications.DaemonPresence
}

func newDaemonSocketRouteTestPresence() *daemonSocketRouteTestPresence {
	return &daemonSocketRouteTestPresence{
		records:        map[uuid.UUID]notifications.DaemonPresence{},
		runtimeRecords: map[uuid.UUID]notifications.DaemonPresence{},
	}
}

func (p *daemonSocketRouteTestPresence) PutIfRuntime(
	_ context.Context,
	machineID uuid.UUID,
	presence notifications.DaemonPresence,
	ttl time.Duration,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, ok := p.records[machineID]
	if ok && current.RuntimeID != presence.RuntimeID {
		return notifications.ErrPresenceNotOwned
	}
	p.records[machineID] = presence
	return nil
}

func (p *daemonSocketRouteTestPresence) PutIfMissing(
	_ context.Context,
	machineID uuid.UUID,
	presence notifications.DaemonPresence,
	ttl time.Duration,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.records[machineID]; ok {
		return notifications.ErrPresenceNotOwned
	}
	p.records[machineID] = presence
	return nil
}

func (p *daemonSocketRouteTestPresence) Refresh(
	ctx context.Context,
	machineID uuid.UUID,
	owner notifications.PresenceOwner,
	ttl time.Duration,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, ok := p.records[machineID]
	if !ok || current.RuntimeID != owner.RuntimeID ||
		current.ReplicaID != owner.ReplicaID ||
		current.ConnectionID != owner.ConnectionID {
		return notifications.ErrPresenceNotOwned
	}
	p.records[machineID] = current
	return nil
}

func (p *daemonSocketRouteTestPresence) Get(
	_ context.Context,
	machineID uuid.UUID,
) (notifications.DaemonPresence, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record, ok := p.records[machineID]
	return record, ok, nil
}

func (p *daemonSocketRouteTestPresence) PutRuntime(
	_ context.Context,
	runtimeID uuid.UUID,
	presence notifications.DaemonPresence,
	ttl time.Duration,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runtimeRecords[runtimeID] = presence
	return nil
}

func (p *daemonSocketRouteTestPresence) PutRuntimeIfMissing(
	_ context.Context,
	runtimeID uuid.UUID,
	presence notifications.DaemonPresence,
	ttl time.Duration,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.runtimeRecords[runtimeID]; ok {
		return notifications.ErrPresenceNotOwned
	}
	p.runtimeRecords[runtimeID] = presence
	return nil
}

func (p *daemonSocketRouteTestPresence) RefreshRuntime(
	_ context.Context,
	runtimeID uuid.UUID,
	owner notifications.PresenceOwner,
	ttl time.Duration,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, ok := p.runtimeRecords[runtimeID]
	if !ok || current.RuntimeID != owner.RuntimeID ||
		current.ReplicaID != owner.ReplicaID ||
		current.ConnectionID != owner.ConnectionID {
		return notifications.ErrPresenceNotOwned
	}
	p.runtimeRecords[runtimeID] = current
	return nil
}

func (p *daemonSocketRouteTestPresence) GetRuntime(
	_ context.Context,
	runtimeID uuid.UUID,
) (notifications.DaemonPresence, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record, ok := p.runtimeRecords[runtimeID]
	return record, ok, nil
}

func (p *daemonSocketRouteTestPresence) DeleteIfOwned(
	_ context.Context,
	machineID uuid.UUID,
	owner notifications.PresenceOwner,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, ok := p.records[machineID]
	if ok && current.RuntimeID == owner.RuntimeID &&
		current.ReplicaID == owner.ReplicaID &&
		current.ConnectionID == owner.ConnectionID {
		delete(p.records, machineID)
	}
	return nil
}

func (p *daemonSocketRouteTestPresence) DeleteRuntimeIfOwned(
	_ context.Context,
	runtimeID uuid.UUID,
	owner notifications.PresenceOwner,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, ok := p.runtimeRecords[runtimeID]
	if ok && current.RuntimeID == owner.RuntimeID &&
		current.ReplicaID == owner.ReplicaID &&
		current.ConnectionID == owner.ConnectionID {
		delete(p.runtimeRecords, runtimeID)
	}
	return nil
}

func TestDaemonSocketRouteOfferAcceptReportJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	testBus := newDaemonSocketRouteTestBus()
	presence := newDaemonSocketRouteTestPresence()
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     testBus,
			AgentEventWakeups: testBus,
			WorkerControls:    testBus,
		},
		presence,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create routed publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	keyWrapper := &daemonSocketRouteKeyWrapper{KeyWrapper: integrationKeyWrapper()}
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(keyWrapper),
		storage.WithPostCommitPublisher(publisher),
	)
	server := mustNewServer(
		t,
		store,
		WithDaemonNotifications(testBus, presence, daemonSocketRouteReplicaID),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-socket-route",
	)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	process := createDaemonProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-socket-route",
		"run_command",
		[]model.ToolCall{
			{
				ID:    "call_socket_route_read",
				Name:  "read_process",
				Input: json.RawMessage(`{}`),
			},
			{
				ID:    "call_socket_route_retry",
				Name:  "run_command",
				Input: json.RawMessage(`{}`),
			},
			{
				ID:    "call_socket_route_valid",
				Name:  "run_command",
				Input: json.RawMessage(`{}`),
			},
		},
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		process.authority(),
		process.ProcessUUID,
	); err != nil || !found {
		t.Fatalf("accept running process found=%v err=%v", found, err)
	}
	if _, err := store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID:       project.ProjectUUID,
		AgentID:         process.AgentUUID,
		ID:              process.ProcessUUID,
		Authority:       process.authority(),
		SourceStartedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("mark running process started: %v", err)
	}
	actionToolCall := process.toolCall(t, "call_socket_route_read")
	action, err := storagetest.CreateProcessActionForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       process.AgentUUID,
			ToolCallID:    actionToolCall.ID,
			RuntimeLockID: process.RuntimeLock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ProcessUUID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	actionID := testPublicID(t, publicid.KindProcessAction, action.ID)
	startProcess := func(name string, at time.Time) executionstore.ProcessRecord {
		t.Helper()
		toolCall := process.toolCall(t, "call_socket_route_"+name)
		started, err := storagetest.StartProcessForToolCall(
			ctx,
			store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     project.ProjectUUID,
				AgentID:       process.AgentUUID,
				ToolCallID:    toolCall.ID,
				RuntimeLockID: process.RuntimeLock.ID,
			},
			executionstore.CreateProcessInput{
				AgentMachineBindingID: process.BindingUUID,
				Command:               "echo " + name,
				ShellSelector:         "sh",
				Cwd:                   "/work",
			},
		)
		if err != nil {
			t.Fatalf("start %s process: %v", name, err)
		}
		return started
	}
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          project.OrgUUID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: project.ProjectUUID,
		Name:           "daemon-socket-route-retry",
		Material:       secrets.GenericMaterial{Value: "secret-value"},
		Actor:          httpUserPrincipal(project.AdminUserUUID),
	})
	if err != nil {
		t.Fatalf("create retryable process secret: %v", err)
	}
	secretID := testPublicID(t, publicid.KindSecret, secret.ID)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_machine_bindings
		SET secret_env_overlay = jsonb_build_object('API_TOKEN', $1::text)
		WHERE id = $2
	`, secretID, process.BindingUUID); err != nil {
		t.Fatalf("set retryable process environment: %v", err)
	}
	startProcess("retry", now.Add(5*time.Second))
	validProcess := startProcess("valid", now.Add(7*time.Second))
	validProcessID := testPublicID(t, publicid.KindProcess, validProcess.ID)
	keyWrapper.failNextUnwrap = true

	socketURL := "ws" + strings.TrimPrefix(
		httpServer.URL,
		"http",
	) + "/api/v1/daemon/runtimes/" + process.RuntimeID + "/socket"
	conn, resp, err := websocket.Dial(
		ctx,
		socketURL,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": {"Bearer " + process.Token},
			},
		},
	)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	conn.SetReadLimit(daemonSocketReadLimitBytes)

	retryError := readSocketMessage(t, ctx, conn)
	if retryError.Type != "error" || !strings.Contains(retryError.Error, "secret key unwrap unavailable") {
		t.Fatalf("retryable process error = %+v", retryError)
	}
	processOffer := readSocketMessage(t, ctx, conn)
	if processOffer.Type != "process_offer" || processOffer.ProcessID != validProcessID {
		t.Fatalf("process offer = %+v, want process %s", processOffer, validProcessID)
	}
	actionOffer := readSocketMessage(t, ctx, conn)
	if actionOffer.Type != "action_offer" ||
		actionOffer.ProcessID != process.ProcessID ||
		actionOffer.ProcessActionID != actionID {
		t.Fatalf("action offer = %+v, want action %s", actionOffer, actionID)
	}
	writeCtx, writeCancel := context.WithTimeout(ctx, 2*time.Second)
	err = conn.Write(
		writeCtx,
		websocket.MessageText,
		[]byte(`{"type":"future_message","payload":{"value":true}}`),
	)
	writeCancel()
	if err != nil {
		t.Fatalf("write unknown socket message: %v", err)
	}
	unknownError := readSocketMessage(t, ctx, conn)
	if unknownError.Type != "error" || unknownError.ErrorCode != daemonprotocol.ErrorCodeValidationFailed {
		t.Fatalf("unknown message response = %+v, want validation error", unknownError)
	}
	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:      "process_accept",
			ProcessID: processOffer.ProcessID,
		},
	)
	processAccept := readSocketMessage(t, ctx, conn)
	if processAccept.Type != "process_accept_ack" ||
		processAccept.ProcessID != validProcessID {
		t.Fatalf("process accept ack = %+v", processAccept)
	}
	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:     "report",
			ReportID: "rpt_socket_route_process_started",
			Event: &daemonprotocol.ReportedEvent{
				Type:       "process_started",
				ProcessID:  validProcessID,
				StartedAt:  now.Add(9 * time.Second),
				ObservedAt: now.Add(10 * time.Second),
			},
		},
	)
	processReportAck := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageReportAck)
	if processReportAck.Type != "report_ack" ||
		processReportAck.AckStatus != daemonprotocol.AckStatusCommitted {
		t.Fatalf("process report ack = %+v", processReportAck)
	}

	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:            "action_accept",
			ProcessID:       process.ProcessID,
			ProcessActionID: actionID,
		},
	)
	actionAccept := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageActionAcceptAck)
	if actionAccept.Type != "action_accept_ack" ||
		actionAccept.ProcessActionID != actionID {
		t.Fatalf("action accept ack = %+v", actionAccept)
	}
	actionReport := daemonprotocol.Message{
		Type:     "report",
		ReportID: "rpt_socket_route_action_applied",
		Event: &daemonprotocol.ReportedEvent{
			Type:            "process_action_applied",
			ProcessID:       process.ProcessID,
			ProcessActionID: actionID,
			Result: json.RawMessage(
				`{"process_id":"` + process.ProcessID +
					`","state":"running","output":"","cursor":0,"next_cursor":0,` +
					`"truncated":false,"done":false,"error":""}`,
			),
		},
	}
	writeSocketMessage(t, ctx, conn, actionReport)
	actionReportAck := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageReportAck)
	if actionReportAck.Type != "report_ack" ||
		actionReportAck.ReportID != actionReport.ReportID ||
		actionReportAck.AckStatus != daemonprotocol.AckStatusCommitted {
		t.Fatalf("action report ack = %+v", actionReportAck)
	}
	writeSocketMessage(t, ctx, conn, actionReport)
	replayedReportAck := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageReportAck)
	if replayedReportAck.ReportID != actionReport.ReportID ||
		replayedReportAck.AckStatus != daemonprotocol.AckStatusCommitted {
		t.Fatalf("replayed action report ack = %+v", replayedReportAck)
	}
	updated, found, err := store.Execution().GetProcessActionByToolCall(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		actionToolCall.ID,
	)
	if err != nil {
		t.Fatalf("load action by tool call: %v", err)
	}
	if !found || updated.State != executionstore.ProcessActionStateApplied {
		t.Fatalf(
			"updated action found=%v state=%s, want applied",
			found,
			updated.State,
		)
	}
}

func TestDaemonSocketRouteReplacesStaleDifferentRuntimePresence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
	)
	presence := newDaemonSocketRouteTestPresence()
	server := mustNewServer(
		t,
		store,
		WithDaemonNotifications(
			newDaemonSocketRouteTestBus(),
			presence,
			daemonSocketRouteReplicaID,
		),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-socket-route-presence",
	)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	process := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-socket-route-presence",
		"run_command",
	)
	livePresence := notifications.DaemonPresence{
		PresenceOwner: notifications.PresenceOwner{
			ReplicaID:    daemonSocketLiveReplicaID,
			RuntimeID:    uuid.New(),
			ConnectionID: uuid.New(),
		},
	}
	if err := presence.PutIfRuntime(ctx, process.MachineUUID, livePresence, time.Minute); err != nil {
		t.Fatalf("seed live presence: %v", err)
	}
	if err := presence.PutRuntime(ctx, livePresence.RuntimeID, livePresence, time.Minute); err != nil {
		t.Fatalf("seed live runtime presence: %v", err)
	}

	socketURL := "ws" + strings.TrimPrefix(
		httpServer.URL,
		"http",
	) + "/api/v1/daemon/runtimes/" + process.RuntimeID + "/socket"
	conn, resp, err := websocket.Dial(
		ctx,
		socketURL,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": {"Bearer " + process.Token},
			},
		},
	)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err == nil {
		defer conn.Close(websocket.StatusNormalClosure, "test done")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, ok, err := presence.Get(ctx, process.MachineUUID)
		if err != nil {
			t.Fatalf("presence after stale socket: %v", err)
		}
		if ok && got.RuntimeID == process.RuntimeUUID &&
			got.ReplicaID == daemonSocketRouteReplicaID &&
			got.ConnectionID != livePresence.ConnectionID {
			if runtimePresence, runtimeOK, err := presence.GetRuntime(ctx, livePresence.RuntimeID); err != nil ||
				!runtimeOK ||
				runtimePresence.ConnectionID != livePresence.ConnectionID {
				t.Fatalf(
					"old runtime presence got=%+v ok=%v err=%v, want retained routing hint %+v",
					runtimePresence,
					runtimeOK,
					err,
					livePresence,
				)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"stale presence was not replaced by current runtime: got %+v ok=%v old %+v",
				got,
				ok,
				livePresence,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDaemonSocketRouteHeartbeatRenewsPostgresLeaseCoarsely(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
	)
	leaseDuration := 4 * time.Second
	presence := newDaemonSocketRouteTestPresence()
	server := mustNewServer(
		t,
		store,
		WithDaemonNotifications(
			newDaemonSocketRouteTestBus(),
			presence,
			daemonSocketRouteReplicaID,
		),
		func(server *Server) {
			server.daemonRuntimeLeaseDuration = leaseDuration
		},
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-socket-route-heartbeat",
	)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	process := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-socket-route-heartbeat",
		"run_command",
	)

	socketURL := "ws" + strings.TrimPrefix(
		httpServer.URL,
		"http",
	) + "/api/v1/daemon/runtimes/" + process.RuntimeID + "/socket"
	conn, resp, err := websocket.Dial(
		ctx,
		socketURL,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": {"Bearer " + process.Token},
			},
		},
	)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	conn.SetReadLimit(daemonSocketReadLimitBytes)

	processOffer := readSocketMessage(t, ctx, conn)
	if processOffer.Type != "process_offer" {
		t.Fatalf("first socket message = %+v, want process_offer", processOffer)
	}

	heartbeat := daemonprotocol.Message{
		Type:             "heartbeat",
		DaemonInstanceID: httpTestID("daemon-http-machine-routes"),
		ObservedPlatform: json.RawMessage(`{"os":"darwin","arch":"arm64"}`),
	}
	writeSocketMessage(t, ctx, conn, heartbeat)
	firstAck := readSocketMessage(t, ctx, conn)
	if firstAck.Type != "heartbeat_ack" || firstAck.NextHeartbeatAfterMS <= 0 {
		t.Fatalf("first heartbeat ack = %+v", firstAck)
	}
	firstLease := loadDaemonRuntimeLeaseForTest(
		t,
		ctx,
		pool,
		process.RuntimeUUID,
	)
	if got := firstLease.leaseExpiresAt.Sub(firstLease.lastSeenAt); got != leaseDuration {
		t.Fatalf("first lease duration = %s, want %s", got, leaseDuration)
	}
	currentPresence, ok, err := presence.Get(ctx, process.MachineUUID)
	if err != nil || !ok {
		t.Fatalf("presence after first heartbeat ok=%v err=%v", ok, err)
	}
	if err := presence.DeleteIfOwned(ctx, process.MachineUUID, currentPresence.PresenceOwner); err != nil {
		t.Fatalf("delete presence before second heartbeat: %v", err)
	}

	writeSocketMessage(t, ctx, conn, heartbeat)
	secondAck := readSocketMessage(t, ctx, conn)
	if secondAck.Type != "heartbeat_ack" {
		t.Fatalf("second heartbeat ack = %+v", secondAck)
	}
	secondLease := loadDaemonRuntimeLeaseForTest(
		t,
		ctx,
		pool,
		process.RuntimeUUID,
	)
	if !secondLease.lastSeenAt.Equal(firstLease.lastSeenAt) ||
		!secondLease.leaseExpiresAt.Equal(firstLease.leaseExpiresAt) {
		t.Fatalf(
			"heartbeat before renewal threshold rewrote lease: first=%+v second=%+v",
			firstLease,
			secondLease,
		)
	}
	if _, ok, err := presence.Get(ctx, process.MachineUUID); err != nil || !ok {
		t.Fatalf(
			"presence was not rehydrated by heartbeat ok=%v err=%v",
			ok,
			err,
		)
	}

	time.Sleep(leaseDuration/2 + 150*time.Millisecond)
	writeSocketMessage(t, ctx, conn, heartbeat)
	thirdAck := readSocketMessage(t, ctx, conn)
	if thirdAck.Type != "heartbeat_ack" {
		t.Fatalf("third heartbeat ack = %+v", thirdAck)
	}
	thirdLease := loadDaemonRuntimeLeaseForTest(
		t,
		ctx,
		pool,
		process.RuntimeUUID,
	)
	if !thirdLease.lastSeenAt.After(secondLease.lastSeenAt) ||
		!thirdLease.leaseExpiresAt.After(secondLease.leaseExpiresAt) {
		t.Fatalf(
			"heartbeat after renewal threshold did not extend lease: second=%+v third=%+v",
			secondLease,
			thirdLease,
		)
	}
	if got := thirdLease.leaseExpiresAt.Sub(thirdLease.lastSeenAt); got != leaseDuration {
		t.Fatalf("renewed lease duration = %s, want %s", got, leaseDuration)
	}
}

func TestDaemonSocketRouteHeartbeatRenewalDrainsQueuedWorkAfterExpiredReconnect(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
	)
	server := mustNewServer(
		t,
		store,
		WithDaemonNotifications(
			newDaemonSocketRouteTestBus(),
			newDaemonSocketRouteTestPresence(),
			daemonSocketRouteReplicaID,
		),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-socket-route-expired-reconnect",
	)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	process := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-socket-route-expired-reconnect",
		"run_command",
	)
	expireDaemonRuntimeForSocketRouteTest(t, ctx, pool, process.RuntimeUUID)

	socketURL := "ws" + strings.TrimPrefix(
		httpServer.URL,
		"http",
	) + "/api/v1/daemon/runtimes/" + process.RuntimeID + "/socket"
	conn, resp, err := websocket.Dial(
		ctx,
		socketURL,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": {"Bearer " + process.Token},
			},
		},
	)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	conn.SetReadLimit(daemonSocketReadLimitBytes)

	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:             "heartbeat",
			DaemonInstanceID: httpTestID("daemon-http-machine-routes"),
			ObservedPlatform: json.RawMessage(`{"os":"darwin","arch":"arm64"}`),
		},
	)
	for i := 0; i < 3; i++ {
		msg := readSocketMessage(t, ctx, conn)
		if msg.Type == "process_offer" && msg.ProcessID == process.ProcessID {
			return
		}
	}
	t.Fatal(
		"heartbeat renewal did not drain queued process offer after expired reconnect",
	)
}

type daemonRuntimeLeaseForTest struct {
	lastSeenAt     time.Time
	leaseExpiresAt time.Time
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadDaemonRuntimeLeaseForTest(
	t *testing.T,
	ctx context.Context,
	pool queryRower,
	runtimeID storage.ID,
) daemonRuntimeLeaseForTest {
	t.Helper()
	var record daemonRuntimeLeaseForTest
	if err := pool.QueryRow(
		ctx,
		`SELECT last_seen_at, lease_expires_at FROM daemon_runtimes WHERE id = $1`,
		runtimeID,
	).Scan(&record.lastSeenAt, &record.leaseExpiresAt); err != nil {
		t.Fatalf("load daemon runtime lease: %v", err)
	}
	return record
}

func expireDaemonRuntimeForSocketRouteTest(
	t *testing.T,
	ctx context.Context,
	pool queryRower,
	runtimeID storage.ID,
) {
	t.Helper()
	var updated storage.ID
	if err := pool.QueryRow(ctx, `UPDATE daemon_runtimes `+
		`SET last_seen_at = statement_timestamp() - INTERVAL '2 seconds', `+
		`lease_expires_at = statement_timestamp() - INTERVAL '1 second', `+
		`updated_at = statement_timestamp() WHERE id = $1 RETURNING id`, runtimeID).Scan(&updated); err != nil {
		t.Fatalf("expire daemon runtime: %v", err)
	}
}

func TestDaemonSocketRouteReceivesRealRoutedRedisWakes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	redisClient := integrationredis.OpenClient(t)
	presence, err := notifications.NewRedisPresenceStore(redisClient)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}
	bus, err := notifications.NewRedisBus(redisClient, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}
	replicaID := uuid.New()
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     bus,
			AgentEventWakeups: bus,
			WorkerControls:    bus,
		},
		presence,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create routed publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
		storage.WithPostCommitPublisher(publisher),
	)
	server := mustNewServer(
		t,
		store,
		WithDaemonNotifications(bus, presence, replicaID),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.CloseDaemonSockets()
		httpServer.Close()
	})
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-socket-route-real-wakes",
	)
	now := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	process := createDaemonProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-socket-route-real-wakes",
		"run_command",
		[]model.ToolCall{{
			ID:    "call_socket_route_real_wake_read",
			Name:  "read_process",
			Input: json.RawMessage(`{}`),
		}},
	)

	socketURL := "ws" + strings.TrimPrefix(
		httpServer.URL,
		"http",
	) + "/api/v1/daemon/runtimes/" + process.RuntimeID + "/socket"
	conn, resp, err := websocket.Dial(
		ctx,
		socketURL,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": {"Bearer " + process.Token},
			},
		},
	)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	conn.SetReadLimit(daemonSocketReadLimitBytes)

	processOffer := readSocketMessage(t, ctx, conn)
	if processOffer.Type != "process_offer" ||
		processOffer.ProcessID != process.ProcessID {
		t.Fatalf(
			"process offer = %+v, want process %s",
			processOffer,
			process.ProcessID,
		)
	}
	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:      "process_accept",
			ProcessID: processOffer.ProcessID,
		},
	)
	processAccept := readSocketMessage(t, ctx, conn)
	if processAccept.Type != "process_accept_ack" ||
		processAccept.ProcessID != process.ProcessID {
		t.Fatalf("process accept ack = %+v", processAccept)
	}
	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:     "report",
			ReportID: "rpt_socket_real_wake_process_started",
			Event: &daemonprotocol.ReportedEvent{
				Type:       "process_started",
				ProcessID:  process.ProcessID,
				StartedAt:  now.Add(500 * time.Millisecond),
				ObservedAt: now.Add(time.Second),
			},
		},
	)
	processReportAck := readSocketMessage(t, ctx, conn)
	if processReportAck.Type != "report_ack" ||
		processReportAck.AckStatus != daemonprotocol.AckStatusCommitted {
		t.Fatalf("process report ack = %+v", processReportAck)
	}

	actionToolCall := process.toolCall(t, "call_socket_route_real_wake_read")
	action, err := storagetest.CreateProcessActionForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       process.AgentUUID,
			ToolCallID:    actionToolCall.ID,
			RuntimeLockID: process.RuntimeLock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ProcessUUID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	actionID := testPublicID(t, publicid.KindProcessAction, action.ID)
	actionOffer := readSocketMessage(t, ctx, conn)
	if actionOffer.Type != "action_offer" ||
		actionOffer.ProcessID != process.ProcessID ||
		actionOffer.ProcessActionID != actionID {
		t.Fatalf(
			"real Redis action offer = %+v, want action %s",
			actionOffer,
			actionID,
		)
	}

	publisher.PublishPostCommit(
		ctx,
		notifications.DaemonRuntimeEndedCommitted{
			RuntimeID: process.RuntimeUUID,
			MachineID: process.MachineUUID,
			Cause:     notifications.DaemonRuntimeEndReconnect,
		},
	)
	runtimeEnded := readSocketMessage(t, ctx, conn)
	if runtimeEnded.Type != "runtime_ended" {
		t.Fatalf("real Redis runtime-ended message = %+v", runtimeEnded)
	}
}

func TestDaemonSocketRouteFallbackDrainDeliversAfterMissedRedisWakeup(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	presence := newDaemonSocketRouteTestPresence()
	bus := daemonSocketRouteFailingPublishBus{}
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     bus,
			AgentEventWakeups: bus,
			WorkerControls:    bus,
		},
		presence,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create routed publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
		storage.WithPostCommitPublisher(publisher),
	)
	server := mustNewServer(
		t,
		store,
		WithDaemonNotifications(bus, presence, daemonSocketRouteReplicaID),
		WithDaemonSocketFallbackDrainTiming(250*time.Millisecond, 0),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.CloseDaemonSockets()
		httpServer.Close()
	})
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-socket-route-missed-wakeup",
	)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	process := createDaemonProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-socket-route-missed-wakeup",
		"run_command",
		[]model.ToolCall{{
			ID:    "call_socket_route_missed_wakeup_read",
			Name:  "read_process",
			Input: json.RawMessage(`{}`),
		}},
	)

	socketURL := "ws" + strings.TrimPrefix(
		httpServer.URL,
		"http",
	) + "/api/v1/daemon/runtimes/" + process.RuntimeID + "/socket"
	conn, resp, err := websocket.Dial(
		ctx,
		socketURL,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": {"Bearer " + process.Token},
			},
		},
	)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	conn.SetReadLimit(daemonSocketReadLimitBytes)

	processOffer := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageProcessOffer)
	if processOffer.ProcessID != process.ProcessID {
		t.Fatalf(
			"process offer = %+v, want process %s",
			processOffer,
			process.ProcessID,
		)
	}
	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:      "process_accept",
			ProcessID: processOffer.ProcessID,
		},
	)
	processAccept := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageProcessAcceptAck)
	if processAccept.ProcessID != process.ProcessID {
		t.Fatalf("process accept ack = %+v", processAccept)
	}
	writeSocketMessage(
		t,
		ctx,
		conn,
		daemonprotocol.Message{
			Type:     "report",
			ReportID: "rpt_socket_missed_wake_process_started",
			Event: &daemonprotocol.ReportedEvent{
				Type:       "process_started",
				ProcessID:  process.ProcessID,
				StartedAt:  now.Add(500 * time.Millisecond),
				ObservedAt: now.Add(time.Second),
			},
		},
	)
	processReportAck := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageReportAck)
	if processReportAck.AckStatus != daemonprotocol.AckStatusCommitted {
		t.Fatalf("process report ack = %+v", processReportAck)
	}

	actionToolCall := process.toolCall(t, "call_socket_route_missed_wakeup_read")
	action, err := storagetest.CreateProcessActionForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       process.AgentUUID,
			ToolCallID:    actionToolCall.ID,
			RuntimeLockID: process.RuntimeLock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ProcessUUID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	actionID := testPublicID(t, publicid.KindProcessAction, action.ID)
	actionOffer := readSocketMessageOfType(t, ctx, conn, daemonprotocol.MessageActionOffer)
	if actionOffer.ProcessID != process.ProcessID ||
		actionOffer.ProcessActionID != actionID {
		t.Fatalf(
			"fallback action offer = %+v, want action %s",
			actionOffer,
			actionID,
		)
	}
}

func TestDaemonSocketAcceptRejectsCanceledOffersExplicitly(
	t *testing.T,
) {
	t.Parallel()

	t.Run("process", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		store := newIntegrationStore(pool)
		server := mustNewServer(t, store)
		handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
		project := bootstrapPublicHTTPProject(
			t,
			handler,
			"daemon-socket-rejected-process",
		)
		now := time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC)
		process := createDaemonProcessFixture(
			t,
			ctx,
			pool,
			store,
			project,
			now,
			"daemon-socket-rejected-process",
			"run_command",
		)
		if _, err := store.Execution().CancelAgent(
			ctx,
			executionstore.CancelAgentInput{
				ProjectID: project.ProjectUUID,
				AgentID:   process.AgentUUID,
				Actor: httpOmnaraActorParams(
					t,
					project.OrgUUID,
					project.AdminUserUUID,
				),
			},
		); err != nil {
			t.Fatal(err)
		}
		socket := &daemonSocket{
			server:    server,
			orgID:     project.OrgUUID,
			machineID: process.MachineUUID,
			runtimeID: process.RuntimeUUID,
			tokenID:   process.TokenUUID,
		}
		err := socket.handleProcessAccept(
			ctx,
			daemonprotocol.Message{
				Type:      "process_accept",
				ProcessID: process.ProcessID,
			},
		)
		if !errors.Is(err, errDaemonProcessOfferUnavailable) {
			t.Fatalf(
				"canceled process accept error = %v, want explicit rejection",
				err,
			)
		}
	})

	t.Run("action", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		store := newIntegrationStore(pool)
		server := mustNewServer(t, store)
		handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
		project := bootstrapPublicHTTPProject(
			t,
			handler,
			"daemon-socket-rejected-action",
		)
		now := time.Date(2026, 6, 1, 14, 30, 0, 0, time.UTC)
		process := createDaemonProcessFixtureWithToolCalls(
			t,
			ctx,
			pool,
			store,
			project,
			now,
			"daemon-socket-rejected-action",
			"run_command",
			[]model.ToolCall{
				{
					ID:    "call_daemon_socket_rejected_action",
					Name:  "write_process",
					Input: json.RawMessage(`{}`),
				},
			},
		)
		if _, found, err := acceptDaemonProcessOfferForTest(
			ctx,
			store,
			process.authority(),
			process.ProcessUUID,
		); err != nil || !found {
			t.Fatalf("accept process found=%t err=%v", found, err)
		}
		if _, err := store.Execution().MarkProcessStarted(
			ctx,
			executionstore.MarkProcessStartedInput{
				ProjectID:       project.ProjectUUID,
				AgentID:         process.AgentUUID,
				ID:              process.ProcessUUID,
				Authority:       process.authority(),
				SourceStartedAt: now.Add(2 * time.Second),
			},
		); err != nil {
			t.Fatal(err)
		}
		toolCall := process.toolCall(t, "call_daemon_socket_rejected_action")
		action, err := storagetest.CreateProcessActionForToolCall(
			ctx,
			store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     project.ProjectUUID,
				AgentID:       process.AgentUUID,
				ToolCallID:    toolCall.ID,
				RuntimeLockID: process.RuntimeLock.ID,
			},
			executionstore.CreateProcessActionInput{
				ProcessID:  process.ProcessUUID,
				ActionKind: executionstore.ProcessActionKindWrite,
				Payload:    json.RawMessage(`{"data":"x"}`),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Execution().CancelAgent(
			ctx,
			executionstore.CancelAgentInput{
				ProjectID: project.ProjectUUID,
				AgentID:   process.AgentUUID,
				Actor: httpOmnaraActorParams(
					t,
					project.OrgUUID,
					project.AdminUserUUID,
				),
			},
		); err != nil {
			t.Fatal(err)
		}
		actionID := testPublicID(
			t,
			publicid.KindProcessAction,
			action.ID,
		)
		socket := &daemonSocket{
			server:    server,
			orgID:     project.OrgUUID,
			machineID: process.MachineUUID,
			runtimeID: process.RuntimeUUID,
			tokenID:   process.TokenUUID,
		}
		err = socket.handleActionAccept(
			ctx,
			daemonprotocol.Message{
				Type:            "action_accept",
				ProcessID:       process.ProcessID,
				ProcessActionID: actionID,
			},
		)
		if !errors.Is(err, errDaemonActionOfferUnavailable) {
			t.Fatalf(
				"canceled action accept error = %v, want explicit rejection",
				err,
			)
		}
	})
}

func writeSocketMessage(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	msg daemonprotocol.Message,
) {
	t.Helper()
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := wsjson.Write(writeCtx, conn, &msg); err != nil {
		t.Fatalf("write socket message %s: %v", msg.Type, err)
	}
}

func readSocketMessage(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
) daemonprotocol.Message {
	t.Helper()
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var msg daemonprotocol.Message
	if err := wsjson.Read(readCtx, conn, &msg); err != nil {
		t.Fatalf("read socket message: %v", err)
	}
	return msg
}

func readSocketMessageOfType(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	wantType daemonprotocol.MessageType,
) daemonprotocol.Message {
	t.Helper()
	for {
		msg := readSocketMessage(t, ctx, conn)
		if msg.Type == wantType {
			return msg
		}
		if msg.Type == daemonprotocol.MessageProcessOffer ||
			msg.Type == daemonprotocol.MessageActionOffer {
			t.Logf("skipping redelivered %s for %s while waiting for %s", msg.Type, msg.ProcessID, wantType)
			continue
		}
		t.Fatalf("unexpected %s while waiting for %s: %+v", msg.Type, wantType, msg)
	}
}
