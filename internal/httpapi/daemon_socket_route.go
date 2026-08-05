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
	"github.com/omnara-ai/omnara/internal/storage"
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
	runtimeID storage.ID
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
	runtimeID storage.ID,
) {
	orgID, machineID, tokenID := scope.OrgID, scope.MachineID, scope.DaemonTokenID
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(daemonSocketReadLimitBytes)
	connectionID := uuid.New()
	if err := s.putDaemonRuntimePresence(r, orgID, machineID, runtimeID, tokenID, connectionID); err != nil {
		if errors.Is(err, notifications.ErrPresenceNotOwned) {
			_ = conn.Close(websocket.StatusNormalClosure, "presence replaced")
			return
		}
		_ = conn.Close(websocket.StatusInternalError, "presence unavailable")
		return
	}
	daemonVersion, registered, err := s.store.Execution().RegisteredDaemonRuntimeVersion(
		r.Context(),
		executionstore.DaemonRuntimeAuthority{
			OrgID: orgID, MachineID: machineID, DaemonRuntimeID: runtimeID, DaemonTokenID: tokenID,
		},
	)
	if err != nil {
		_ = s.deleteDaemonRuntimePresence(context.WithoutCancel(r.Context()), machineID, runtimeID, connectionID)
		_ = conn.Close(websocket.StatusInternalError, "runtime registration unavailable")
		return
	}
	if !registered {
		_ = s.deleteDaemonRuntimePresence(context.WithoutCancel(r.Context()), machineID, runtimeID, connectionID)
		_ = conn.Close(websocket.StatusNormalClosure, "runtime ended")
		return
	}
	online, err := s.store.Execution().OnlineDaemonRuntimeExists(
		r.Context(),
		executionstore.DaemonRuntimeAuthority{
			OrgID: orgID, MachineID: machineID, DaemonRuntimeID: runtimeID, DaemonTokenID: tokenID,
		},
	)
	if err != nil {
		_ = s.deleteDaemonRuntimePresence(context.WithoutCancel(r.Context()), machineID, runtimeID, connectionID)
		_ = conn.Close(websocket.StatusInternalError, "runtime registration unavailable")
		return
	}
	wire := daemonprotocol.NewBackendSocket(conn, daemonVersion)
	socket := newDaemonSocket(s, wire, connectionID, orgID, machineID, runtimeID, tokenID, !online)
	s.daemonHub.register(socket)
	socket.run(r.Context())
}

func (s *Server) putDaemonRuntimePresence(
	r *http.Request,
	orgID, machineID, runtimeID, tokenID storage.ID,
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
	err := s.putDaemonRuntimePresenceRecords(r.Context(), machineID, runtimeID, presence, presenceTTL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, notifications.ErrPresenceNotOwned) {
		return err
	}
	current, ok, getErr := s.daemonHub.presence.Get(r.Context(), machineID)
	if getErr != nil {
		return getErr
	}
	if !ok {
		return s.putDaemonRuntimePresenceRecords(r.Context(), machineID, runtimeID, presence, presenceTTL)
	}
	if current.RuntimeID == runtimeID {
		return s.putDaemonRuntimePresenceRecords(r.Context(), machineID, runtimeID, presence, presenceTTL)
	}
	registered, checkErr := s.store.Execution().RegisteredDaemonRuntimeExists(
		r.Context(),
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
		context.WithoutCancel(r.Context()),
		machineID,
		current.PresenceOwner,
	); err != nil {
		return err
	}
	// Leave the old runtime presence for its owning socket to delete or for TTL
	// expiry. A runtime-ended wakeup may already be queued and still needs this
	// runtime-scoped routing hint; the machine key is the only key that blocks
	// the replacement runtime from connecting.
	return s.putDaemonRuntimePresenceRecords(r.Context(), machineID, runtimeID, presence, presenceTTL)
}

func (s *Server) putDaemonRuntimePresenceRecords(
	ctx context.Context,
	machineID, runtimeID storage.ID,
	presence notifications.DaemonPresence,
	ttl time.Duration,
) error {
	if err := s.daemonHub.presence.PutIfRuntime(ctx, machineID, presence, ttl); err != nil {
		return err
	}
	if err := s.daemonHub.presence.PutRuntime(ctx, runtimeID, presence, ttl); err != nil {
		_ = s.daemonHub.presence.DeleteIfOwned(context.WithoutCancel(ctx), machineID, presence.PresenceOwner)
		return err
	}
	return nil
}

func (s *Server) deleteDaemonRuntimePresence(
	ctx context.Context,
	machineID, runtimeID storage.ID,
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
