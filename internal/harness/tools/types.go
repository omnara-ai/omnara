package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/sigv4"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

type SkillBroadcaster interface {
	BroadcastAndAwait(
		ctx context.Context,
		skillPublicID string,
		revisionPublicID string,
		archiveDigest string,
		targets []skills.BroadcastTarget,
		timeout time.Duration,
	) ([]skills.BroadcastOutcome, error)
}

type SkillStore interface {
	GetSkillForDispatch(
		ctx context.Context,
		projectID uuid.UUID,
		skillID uuid.UUID,
	) (skillstore.SkillRecord, error)
}

type Turn struct {
	OrgID              uuid.UUID
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	SourceEventID      uuid.UUID
	RuntimeLockID      uuid.UUID
	ModelCallContextID uuid.UUID
	Tools              map[string]ToolSpec
}

type ToolSpec struct {
	Type        string
	Permission  toolpermission.Selection
	Description string
	InputSchema json.RawMessage
	Deferred    bool
}

type Executor struct {
	Store                *storage.Store
	Skills               SkillStore
	MCP                  mcp.Client
	AppHTTPClient        *http.Client
	MCPAuthHTTPClient    *http.Client
	SigV4CredentialCache *sigv4.CredentialCache
	WebSearch            webaccess.SearchProvider
	WebFetcher           *webaccess.Fetcher
	MachinePoolManager   machinePoolManager
	BackgroundRunner     BackgroundRunner

	MCPInitializationBackoff func(attempt int) time.Duration
	SkillBroadcaster         SkillBroadcaster
	AgentConfigOptions       agentconfig.CompileOptions
	Log                      *slog.Logger
}

func (e Executor) logger() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e Executor) skillStore() SkillStore {
	if e.Skills != nil {
		return e.Skills
	}
	if e.Store == nil {
		return nil
	}
	skills := e.Store.Skills()
	if skills == nil {
		return nil
	}
	return skills
}

type machinePoolManager interface {
	ProvisionMachine(ctx context.Context, orgID, machineID uuid.UUID) error
	StartLaunchProvisioning(parent context.Context, logger *slog.Logger, orgID uuid.UUID, machineIDs []uuid.UUID)
	DeleteMachine(ctx context.Context, candidate executionstore.PoolMachineCleanupCandidate) error
	DeleteMachines(ctx context.Context, machines []executionstore.MachineRecord) (int, error)
	WakeMachine(ctx context.Context, orgID, machineID uuid.UUID) (bool, error)
}

type DispatchDisposition uint8

const (
	dispatchDispositionInvalid DispatchDisposition = iota
	DispatchCompleted
	DispatchDeferred
)

type Result struct {
	ToolCallID   string              `json:"tool_call_id"`
	Name         string              `json:"name"`
	ContentParts json.RawMessage     `json:"content_parts"`
	Disposition  DispatchDisposition `json:"-"`
}
