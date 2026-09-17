package kernel

import (
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type ModelWorkExecution struct {
	Kind                     executionstore.ModelWorkKind
	OrgID                    uuid.UUID
	ProjectID                uuid.UUID
	AgentID                  uuid.UUID
	ModelCallContextID       uuid.UUID
	SourceModelCallContextID uuid.UUID
	SourceModelOutputID      uuid.UUID
	TurnID                   uuid.UUID
	InputIDs                 []uuid.UUID
	OpeningEventSequence     int64
	RuntimeLockID            uuid.UUID
	Now                      time.Time
}

type ToolWorkExecution struct {
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	TurnID             uuid.UUID
	ModelCallContextID uuid.UUID
	ModelOutputID      uuid.UUID
	SourceEventID      uuid.UUID
	RuntimeLockID      uuid.UUID
	Now                time.Time
}
