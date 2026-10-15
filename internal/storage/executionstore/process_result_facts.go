package executionstore

import "github.com/omnara-ai/omnara/internal/processresult"

func (p ProcessRecord) resultFacts() processresult.Process {
	return processresult.Process{
		ID:                  p.ID,
		State:               string(p.State),
		DefaultOutputCursor: p.DefaultOutputCursor,
		SourceStartedAt:     p.SourceStartedAt,
		SourceEndedAt:       p.SourceEndedAt,
		ExitCode:            p.ExitCode,
		ExitSignal:          p.ExitSignal,
		StateReasonCode:     p.StateReasonCode,
		StateReasonMessage:  p.StateReasonMessage,
		ExecutionSpec:       p.ExecutionSpec,
	}
}
func (a ProcessActionRecord) resultFacts() processresult.Action {
	return processresult.Action{ID: a.ID, Payload: a.Payload}
}
