package executionstore

import (
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func modelCallContextRecordFromSQLC(row dbsqlc.ModelCallContext) ModelCallContextRecord {
	return ModelCallContextRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ProjectID:                 row.ProjectID,
		AgentID:                   row.AgentID,
		OperationKind:             ModelCallOperation(row.OperationKind),
		AttemptNumber:             int(row.AttemptNumber),
		AgentConfigID:             row.AgentConfigID,
		ConfiguredModelRevisionID: row.ConfiguredModelRevisionID,
		InputEventSequence:        row.InputEventSequence,
		SourceEventSequenceEnd:    int64PtrFromSQLC(row.SourceEventSequenceEnd),
		RuntimeLockID:             row.RuntimeLockID,
		State:                     ModelCallState(row.State),
		RecoveryKind:              ModelCallRecoveryKind(stringFromSQLCText(row.RecoveryKind)),
		APIFormat:                 modelprotocol.APIFormat(row.ApiFormat),
		APIVariant:                modelprotocol.APIVariant(row.ApiVariant),
		ProviderRequestID:         row.ProviderRequestID,
		ProviderResponseID:        row.ProviderResponseID,
		ErrorKind:                 modelprotocol.ErrorKind(row.ErrorKind),
		ErrorCode:                 row.ErrorCode,
		ErrorMessage:              row.ErrorMessage,
		ErrorDetails:              row.ErrorDetails,
		RetryAt:                   row.RetryAt,
		Usage: modelUsageFromSQLC(
			row.InputTokensTotal,
			row.UncachedInputTokens,
			row.CacheReadInputTokens,
			row.CacheWriteInputTokens,
			row.OutputTokensTotal,
			row.ReasoningOutputTokens,
		),
		CreatedAt:   row.CreatedAt,
		CompletedAt: row.CompletedAt,
	}
}

func int64PtrFromSQLC(value *int64) *int64 {
	if value == nil {
		return nil
	}
	valueCopy := *value
	return &valueCopy
}
