package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/redistore"
)

type DaemonPresence struct {
	PresenceOwner
}

type PresenceOwner struct {
	RuntimeID    uuid.UUID `json:"runtime_id"`
	ReplicaID    uuid.UUID `json:"replica_id"`
	ConnectionID uuid.UUID `json:"connection_id"`
}

var errInvalidPresence = errors.New("invalid daemon presence")

type DaemonPresenceStore interface {
	PutIfRuntime(ctx context.Context, machineID uuid.UUID, presence DaemonPresence, ttl time.Duration) error
	PutIfMissing(ctx context.Context, machineID uuid.UUID, presence DaemonPresence, ttl time.Duration) error
	Refresh(ctx context.Context, machineID uuid.UUID, owner PresenceOwner, ttl time.Duration) error
	Get(ctx context.Context, machineID uuid.UUID) (DaemonPresence, bool, error)
	PutRuntime(ctx context.Context, runtimeID uuid.UUID, presence DaemonPresence, ttl time.Duration) error
	PutRuntimeIfMissing(ctx context.Context, runtimeID uuid.UUID, presence DaemonPresence, ttl time.Duration) error
	RefreshRuntime(ctx context.Context, runtimeID uuid.UUID, owner PresenceOwner, ttl time.Duration) error
	GetRuntime(ctx context.Context, runtimeID uuid.UUID) (DaemonPresence, bool, error)
	DeleteIfOwned(ctx context.Context, machineID uuid.UUID, owner PresenceOwner) error
	DeleteRuntimeIfOwned(ctx context.Context, runtimeID uuid.UUID, owner PresenceOwner) error
}

type RedisPresenceStore struct {
	client *redistore.Client
}

func NewRedisPresenceStore(client *redistore.Client) (*RedisPresenceStore, error) {
	if client == nil {
		return nil, errors.New("redis coordination client is required")
	}
	return &RedisPresenceStore{client: client}, nil
}

func (s *RedisPresenceStore) PutIfRuntime(
	ctx context.Context,
	machineID uuid.UUID,
	presence DaemonPresence,
	ttl time.Duration,
) error {
	if s == nil || s.client == nil {
		return errors.New("redis presence store is closed")
	}
	body, err := marshalDaemonPresence(machineID, presence, ttl)
	if err != nil {
		return err
	}
	res, err := s.client.EvalInt(
		ctx,
		putPresenceIfRuntimeScript,
		[]string{daemonPresenceKey(machineID)},
		presence.RuntimeID.String(),
		string(body),
		int(ttl.Milliseconds()),
	)
	if err != nil {
		return err
	}
	if res == 0 {
		return ErrPresenceNotOwned
	}
	return nil
}

func (s *RedisPresenceStore) PutIfMissing(
	ctx context.Context,
	machineID uuid.UUID,
	presence DaemonPresence,
	ttl time.Duration,
) error {
	if s == nil || s.client == nil {
		return errors.New("redis presence store is closed")
	}
	body, err := marshalDaemonPresence(machineID, presence, ttl)
	if err != nil {
		return err
	}
	res, err := s.client.EvalInt(
		ctx,
		putPresenceIfMissingScript,
		[]string{daemonPresenceKey(machineID)},
		string(body),
		int(ttl.Milliseconds()),
	)
	if err != nil {
		return err
	}
	if res == 0 {
		return ErrPresenceNotOwned
	}
	return nil
}

func marshalDaemonPresence(keyID uuid.UUID, presence DaemonPresence, ttl time.Duration) ([]byte, error) {
	if keyID == uuid.Nil {
		return nil, errors.New("presence key is required")
	}
	if err := validateDaemonPresence(presence); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, errors.New("presence ttl must be positive")
	}
	return json.Marshal(presence)
}

func validateDaemonPresence(presence DaemonPresence) error {
	if presence.RuntimeID == uuid.Nil {
		return fmt.Errorf("%w: runtime id is nil", errInvalidPresence)
	}
	if presence.ReplicaID == uuid.Nil {
		return fmt.Errorf("%w: replica id is nil", errInvalidPresence)
	}
	if presence.ConnectionID == uuid.Nil {
		return fmt.Errorf("%w: connection id is nil", errInvalidPresence)
	}
	return nil
}

func (s *RedisPresenceStore) Refresh(
	ctx context.Context,
	machineID uuid.UUID,
	owner PresenceOwner,
	ttl time.Duration,
) error {
	if s == nil || s.client == nil {
		return errors.New("redis presence store is closed")
	}
	presence := DaemonPresence{PresenceOwner: owner}
	body, err := marshalDaemonPresence(machineID, presence, ttl)
	if err != nil {
		return err
	}
	res, err := s.client.EvalInt(
		ctx,
		refreshPresenceScript,
		[]string{daemonPresenceKey(machineID)},
		owner.RuntimeID.String(),
		owner.ReplicaID.String(),
		owner.ConnectionID.String(),
		string(body),
		int(ttl.Milliseconds()),
	)
	if err != nil {
		return err
	}
	if res == 0 {
		return ErrPresenceNotOwned
	}
	return nil
}

func (s *RedisPresenceStore) Get(ctx context.Context, machineID uuid.UUID) (DaemonPresence, bool, error) {
	return s.getByKey(ctx, machineID, daemonPresenceKey(machineID))
}

func (s *RedisPresenceStore) PutRuntime(
	ctx context.Context,
	runtimeID uuid.UUID,
	presence DaemonPresence,
	ttl time.Duration,
) error {
	if s == nil || s.client == nil {
		return errors.New("redis presence store is closed")
	}
	if runtimeID != presence.RuntimeID {
		return errors.New("runtime presence key must match presence owner")
	}
	body, err := marshalDaemonPresence(runtimeID, presence, ttl)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, daemonRuntimePresenceKey(runtimeID), body, ttl)
}

func (s *RedisPresenceStore) PutRuntimeIfMissing(
	ctx context.Context,
	runtimeID uuid.UUID,
	presence DaemonPresence,
	ttl time.Duration,
) error {
	if s == nil || s.client == nil {
		return errors.New("redis presence store is closed")
	}
	if runtimeID != presence.RuntimeID {
		return errors.New("runtime presence key must match presence owner")
	}
	body, err := marshalDaemonPresence(runtimeID, presence, ttl)
	if err != nil {
		return err
	}
	ok, err := s.client.SetNX(ctx, daemonRuntimePresenceKey(runtimeID), body, ttl)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPresenceNotOwned
	}
	return nil
}

func (s *RedisPresenceStore) RefreshRuntime(
	ctx context.Context,
	runtimeID uuid.UUID,
	owner PresenceOwner,
	ttl time.Duration,
) error {
	if s == nil || s.client == nil {
		return errors.New("redis presence store is closed")
	}
	if runtimeID != owner.RuntimeID {
		return errors.New("runtime presence key must match presence owner")
	}
	presence := DaemonPresence{PresenceOwner: owner}
	body, err := marshalDaemonPresence(runtimeID, presence, ttl)
	if err != nil {
		return err
	}
	res, err := s.client.EvalInt(
		ctx,
		refreshPresenceScript,
		[]string{daemonRuntimePresenceKey(runtimeID)},
		owner.RuntimeID.String(),
		owner.ReplicaID.String(),
		owner.ConnectionID.String(),
		string(body),
		int(ttl.Milliseconds()),
	)
	if err != nil {
		return err
	}
	if res == 0 {
		return ErrPresenceNotOwned
	}
	return nil
}

func (s *RedisPresenceStore) GetRuntime(ctx context.Context, runtimeID uuid.UUID) (DaemonPresence, bool, error) {
	presence, ok, err := s.getByKey(ctx, runtimeID, daemonRuntimePresenceKey(runtimeID))
	if err != nil || !ok {
		return DaemonPresence{}, ok, err
	}
	if presence.RuntimeID != runtimeID {
		return DaemonPresence{}, false, fmt.Errorf("%w: runtime id does not match key", errInvalidPresence)
	}
	return presence, true, nil
}

func (s *RedisPresenceStore) DeleteIfOwned(ctx context.Context, machineID uuid.UUID, owner PresenceOwner) error {
	if machineID == uuid.Nil {
		return errors.New("machine id is required")
	}
	return s.deleteKeyIfOwned(ctx, daemonPresenceKey(machineID), owner)
}

func (s *RedisPresenceStore) DeleteRuntimeIfOwned(ctx context.Context, runtimeID uuid.UUID, owner PresenceOwner) error {
	if runtimeID != owner.RuntimeID {
		return errors.New("runtime presence key must match presence owner")
	}
	return s.deleteKeyIfOwned(ctx, daemonRuntimePresenceKey(runtimeID), owner)
}

func daemonPresenceKey(machineID uuid.UUID) string {
	return "omnara:daemon:presence:" + machineID.String()
}

func daemonRuntimePresenceKey(runtimeID uuid.UUID) string {
	return "omnara:daemon:runtime_presence:" + runtimeID.String()
}

func (s *RedisPresenceStore) getByKey(ctx context.Context, id uuid.UUID, key string) (DaemonPresence, bool, error) {
	if s == nil || s.client == nil {
		return DaemonPresence{}, false, errors.New("redis presence store is closed")
	}
	if id == uuid.Nil {
		return DaemonPresence{}, false, errors.New("presence id is required")
	}
	raw, ok, err := s.client.GetBytes(ctx, key)
	if err != nil {
		return DaemonPresence{}, false, err
	}
	if !ok {
		return DaemonPresence{}, false, nil
	}
	var record struct {
		RuntimeID    string `json:"runtime_id"`
		ReplicaID    string `json:"replica_id"`
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return DaemonPresence{}, false, fmt.Errorf("%w: decode daemon presence: %w", errInvalidPresence, err)
	}
	runtimeID, err := parsePresenceUUID("runtime id", record.RuntimeID)
	if err != nil {
		return DaemonPresence{}, false, err
	}
	replicaID, err := parsePresenceUUID("replica id", record.ReplicaID)
	if err != nil {
		return DaemonPresence{}, false, err
	}
	connectionID, err := parsePresenceUUID("connection id", record.ConnectionID)
	if err != nil {
		return DaemonPresence{}, false, err
	}
	return DaemonPresence{PresenceOwner: PresenceOwner{
		RuntimeID:    runtimeID,
		ReplicaID:    replicaID,
		ConnectionID: connectionID,
	}}, true, nil
}

func parsePresenceUUID(name, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: invalid %s: %w", errInvalidPresence, name, err)
	}
	if id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s is nil", errInvalidPresence, name)
	}
	if value != id.String() {
		return uuid.Nil, fmt.Errorf("%w: %s is not canonical", errInvalidPresence, name)
	}
	return id, nil
}

func (s *RedisPresenceStore) deleteKeyIfOwned(ctx context.Context, key string, owner PresenceOwner) error {
	if s == nil || s.client == nil {
		return errors.New("redis presence store is closed")
	}
	if err := validateDaemonPresence(DaemonPresence{PresenceOwner: owner}); err != nil {
		return err
	}
	_, err := s.client.EvalInt(
		ctx,
		deletePresenceScript,
		[]string{key},
		owner.RuntimeID.String(),
		owner.ReplicaID.String(),
		owner.ConnectionID.String(),
	)
	return err
}

var ErrPresenceNotOwned = errors.New("daemon presence is owned by another runtime or replica")

const refreshPresenceScript = `
local current = redis.call("GET", KEYS[1])
if not current then
  return 0
end
local decoded = cjson.decode(current)
if decoded["runtime_id"] ~= ARGV[1] or decoded["replica_id"] ~= ARGV[2] or decoded["connection_id"] ~= ARGV[3] then
  return 0
end
redis.call("SET", KEYS[1], ARGV[4], "PX", ARGV[5])
return 1
`

const putPresenceIfRuntimeScript = `
local current = redis.call("GET", KEYS[1])
if current then
  local decoded = cjson.decode(current)
  if decoded["runtime_id"] ~= ARGV[1] then
    return 0
  end
end
redis.call("SET", KEYS[1], ARGV[2], "PX", ARGV[3])
return 1
`

const putPresenceIfMissingScript = `
local current = redis.call("GET", KEYS[1])
if current then
  return 0
end
redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
return 1
`

const deletePresenceScript = `
local current = redis.call("GET", KEYS[1])
if not current then
  return 1
end
local decoded = cjson.decode(current)
if decoded["runtime_id"] == ARGV[1] and decoded["replica_id"] == ARGV[2] and decoded["connection_id"] == ARGV[3] then
  redis.call("DEL", KEYS[1])
end
return 1
`
