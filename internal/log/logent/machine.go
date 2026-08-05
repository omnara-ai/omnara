package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func Machine(ctx context.Context, r executionstore.MachineRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":                   r.OrgID,
		"machine.id":               r.ID,
		"machine_pool.id":          r.MachinePoolID,
		"machine.display_name":     r.DisplayName,
		"machine.source_kind":      string(r.SourceKind),
		"machine.provider":         r.Provider,
		"machine.lifecycle_state":  string(r.LifecycleState),
		"machine.connection_state": string(r.ConnectionState),
		"machine.last_observed_at": r.LastObservedAt,
	})
}

func MachineBootstrap(ctx context.Context, r executionstore.MachineBootstrapRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":          r.OrgID,
		"installation.id": r.InstallationID,
		"machine.id":      r.MachineID,
	})
}
