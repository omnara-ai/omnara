package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func DaemonRuntime(ctx context.Context, r executionstore.DaemonRuntimeRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":                            r.OrgID,
		"machine.id":                        r.MachineID,
		"daemon_runtime.id":                 r.ID,
		"daemon_runtime.daemon_instance_id": r.DaemonInstanceID,
		"daemon_runtime.daemon_version":     r.DaemonVersion,
		"daemon_runtime.state":              string(r.State),
		"daemon_runtime.state_reason_code":  r.StateReasonCode,
		"daemon_runtime.last_seen_at":       r.LastSeenAt,
		"daemon_runtime.ended_at":           r.EndedAt,
	})
}
func DaemonRuntimeRegistration(ctx context.Context, r executionstore.DaemonRuntimeRegistrationRecord) {
	DaemonRuntime(ctx, r.Runtime)
	log.Attach(ctx, log.Fields{
		"daemon_runtime.reconciliation.process_count": len(r.Reconciliation.Processes),
	})
}
