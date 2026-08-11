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

func MachineFailureReport(ctx context.Context, input executionstore.MachineFailureReportInput) {
	fields := log.Fields{
		"org.id":                                  input.OrgID,
		"machine.id":                              input.MachineID,
		"machine.failure_report.stage":            input.Stage,
		"machine.failure_report.output_tail":      string(input.OutputTail),
		"machine.failure_report.output_truncated": input.OutputTruncated,
	}
	if input.ExitStatus != nil {
		fields["machine.failure_report.exit_status"] = *input.ExitStatus
	}
	if input.DaemonVersion != "" {
		fields["machine.failure_report.daemon_version"] = input.DaemonVersion
	}
	if input.TargetVersion != "" {
		fields["machine.failure_report.target_version"] = input.TargetVersion
	}
	log.Attach(ctx, fields)
	if input.Stage != executionstore.MachineFailureStageDaemonUninstalled {
		log.Level(ctx, log.WarnLevel)
	}
}
