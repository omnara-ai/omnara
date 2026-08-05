package kernel

import (
	"time"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type ModelWorkExecution struct {
	Kind                     executionstore.ModelWorkKind
	OrgID                    storage.ID
	ProjectID                storage.ID
	AgentID                  storage.ID
	ModelCallContextID       storage.ID
	SourceModelCallContextID storage.ID
	SourceModelOutputID      storage.ID
	TurnID                   storage.ID
	InputIDs                 []storage.ID
	OpeningEventSequence     int64
	RuntimeLockID            storage.ID
	Now                      time.Time
}

type ToolWorkExecution struct {
	ProjectID          storage.ID
	AgentID            storage.ID
	TurnID             storage.ID
	ModelCallContextID storage.ID
	ModelOutputID      storage.ID
	SourceEventID      storage.ID
	RuntimeLockID      storage.ID
	Now                time.Time
}
