package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s strictOpenAPIServer) SocketMachineDaemonRuntime(
	ctx context.Context,
	request openapi.SocketMachineDaemonRuntimeRequestObject,
) (openapi.SocketMachineDaemonRuntimeResponseObject, error) {
	r, ok := openAPIHTTPRequest(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "daemon websocket request is unavailable")
	}
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return nil, *scopeErr
	}
	if s.server.daemonHub == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "daemon websocket unavailable")
	}
	runtimeID, ok := parseOpenAPIPublicID(publicid.KindDaemonRuntime, request.RuntimeID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	registered, err := s.server.store.Execution().RegisteredDaemonRuntimeExists(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           scope.OrgID,
			MachineID:       scope.MachineID,
			DaemonRuntimeID: runtimeID,
			DaemonTokenID:   scope.DaemonTokenID,
		},
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	if !registered {
		return daemonRuntimeUnregisteredResponse(), nil
	}
	return socketMachineDaemonRuntimeLiveResponse{server: s.server, request: r, scope: scope, runtimeID: runtimeID}, nil
}

func daemonRuntimeUnregisteredResponse() openapi.SocketMachineDaemonRuntime410JSONResponse {
	return openapi.SocketMachineDaemonRuntime410JSONResponse{
		GoneJSONResponse: openapi.GoneJSONResponse(
			apierror.Body(openapi.ErrorCodeDaemonRuntimeUnregistered),
		),
	}
}

type socketMachineDaemonRuntimeLiveResponse struct {
	server    *Server
	request   *http.Request
	scope     machineDaemonScope
	runtimeID uuid.UUID
}

func (response socketMachineDaemonRuntimeLiveResponse) VisitSocketMachineDaemonRuntimeResponse(
	w http.ResponseWriter,
) error {
	response.server.socketMachineDaemonRuntime(w, response.request, response.scope, response.runtimeID)
	return nil
}

func (s *Server) socketMachineDaemonRuntime(
	w http.ResponseWriter,
	r *http.Request,
	scope machineDaemonScope,
	runtimeID uuid.UUID,
) {
	orgID, machineID, tokenID := scope.OrgID, scope.MachineID, scope.DaemonTokenID
	ctx, finish, ok := s.daemonHub.beginHandler(r.Context())
	if !ok {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable)
		return
	}
	defer finish()
	conn, err := acceptDaemonSocket(ctx, w, r)
	if err != nil {
		return
	}
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		_ = conn.CloseNow()
		close(closed)
	})
	defer func() {
		if !stopClose() {
			<-closed
		}
		_ = conn.CloseNow()
	}()
	conn.SetReadLimit(daemonSocketReadLimitBytes)
	connectionID := uuid.New()
	defer s.cleanupDaemonRuntimePresence(ctx, machineID, runtimeID, connectionID)
	setupCtx, setupCancel := context.WithTimeout(ctx, 10*time.Second)
	defer setupCancel()
	if err := s.putDaemonRuntimePresence(setupCtx, orgID, machineID, runtimeID, tokenID, connectionID); err != nil {
		if errors.Is(err, notifications.ErrPresenceNotOwned) {
			_ = conn.Close(websocket.StatusNormalClosure, "presence replaced")
			return
		}
		_ = conn.Close(websocket.StatusInternalError, "presence unavailable")
		return
	}
	daemonVersion, registered, err := s.store.Execution().RegisteredDaemonRuntimeVersion(
		setupCtx,
		executionstore.DaemonRuntimeAuthority{
			OrgID: orgID, MachineID: machineID, DaemonRuntimeID: runtimeID, DaemonTokenID: tokenID,
		},
	)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "runtime registration unavailable")
		return
	}
	if !registered {
		_ = conn.Close(websocket.StatusNormalClosure, "runtime ended")
		return
	}
	online, err := s.store.Execution().OnlineDaemonRuntimeExists(
		setupCtx,
		executionstore.DaemonRuntimeAuthority{
			OrgID: orgID, MachineID: machineID, DaemonRuntimeID: runtimeID, DaemonTokenID: tokenID,
		},
	)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "runtime registration unavailable")
		return
	}
	setupCancel()
	wire := daemonprotocol.NewBackendSocket(conn, daemonVersion)
	socket := newDaemonSocket(s, wire, connectionID, orgID, machineID, runtimeID, tokenID, !online)
	socket.run(ctx)
}

func acceptDaemonSocket(ctx context.Context, w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable)
		return nil, err
	}
	// Accept can block while flushing the HTTP upgrade response, before it
	// returns a socket we can close. Interrupt that write on shutdown as well.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = controller.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if !stop() {
		<-interrupted
	}
	// Join the cancellation callback before clearing the deadline so it cannot
	// install an expired deadline on a successfully upgraded connection.
	deadlineErr := controller.SetWriteDeadline(time.Time{})
	if err != nil {
		return nil, err
	}
	if deadlineErr != nil {
		_ = conn.CloseNow()
		return nil, deadlineErr
	}
	return conn, nil
}

func (s *Server) putDaemonRuntimePresence(
	ctx context.Context,
	orgID, machineID, runtimeID, tokenID uuid.UUID,
	connectionID uuid.UUID,
) error {
	presence := notifications.DaemonPresence{
		PresenceOwner: notifications.PresenceOwner{
			ReplicaID:    s.daemonHub.replicaID,
			RuntimeID:    runtimeID,
			ConnectionID: connectionID,
		},
	}
	presenceTTL := s.daemonRuntimePresenceTTL()
	err := s.putDaemonRuntimePresenceRecords(ctx, machineID, runtimeID, presence, presenceTTL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, notifications.ErrPresenceNotOwned) {
		return err
	}
	current, ok, getErr := s.daemonHub.presence.Get(ctx, machineID)
	if getErr != nil {
		return getErr
	}
	if !ok {
		return s.putDaemonRuntimePresenceRecords(ctx, machineID, runtimeID, presence, presenceTTL)
	}
	if current.RuntimeID == runtimeID {
		return s.putDaemonRuntimePresenceRecords(ctx, machineID, runtimeID, presence, presenceTTL)
	}
	registered, checkErr := s.store.Execution().RegisteredDaemonRuntimeExists(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           orgID,
			MachineID:       machineID,
			DaemonRuntimeID: current.RuntimeID,
			DaemonTokenID:   tokenID,
		},
	)
	if checkErr != nil {
		return checkErr
	}
	if registered {
		return notifications.ErrPresenceNotOwned
	}
	if err := s.daemonHub.presence.DeleteIfOwned(
		ctx,
		machineID,
		current.PresenceOwner,
	); err != nil {
		return err
	}
	// Leave the old runtime presence for its owning socket to delete or for TTL
	// expiry. A runtime-ended wakeup may already be queued and still needs this
	// runtime-scoped routing hint; the machine key is the only key that blocks
	// the replacement runtime from connecting.
	return s.putDaemonRuntimePresenceRecords(ctx, machineID, runtimeID, presence, presenceTTL)
}

func (s *Server) putDaemonRuntimePresenceRecords(
	ctx context.Context,
	machineID, runtimeID uuid.UUID,
	presence notifications.DaemonPresence,
	ttl time.Duration,
) error {
	if err := s.daemonHub.presence.PutIfRuntime(ctx, machineID, presence, ttl); err != nil {
		return err
	}
	if err := s.daemonHub.presence.PutRuntime(ctx, runtimeID, presence, ttl); err != nil {
		return err
	}
	return nil
}

func (s *Server) cleanupDaemonRuntimePresence(ctx context.Context, machineID, runtimeID, connectionID uuid.UUID) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.deleteDaemonRuntimePresence(cleanupCtx, machineID, runtimeID, connectionID)
}

func (s *Server) deleteDaemonRuntimePresence(
	ctx context.Context,
	machineID, runtimeID uuid.UUID,
	connectionID uuid.UUID,
) error {
	owner := notifications.PresenceOwner{
		RuntimeID:    runtimeID,
		ReplicaID:    s.daemonHub.replicaID,
		ConnectionID: connectionID,
	}
	machineErr := s.daemonHub.presence.DeleteIfOwned(ctx, machineID, owner)
	runtimeErr := s.daemonHub.presence.DeleteRuntimeIfOwned(ctx, runtimeID, owner)
	if machineErr != nil {
		return machineErr
	}
	return runtimeErr
}
