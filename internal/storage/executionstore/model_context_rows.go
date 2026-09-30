package executionstore

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func modelCallContextRecordFromSQLC(row dbsqlc.GetModelCallContextRow) ModelCallContextRecord {
	providerMetadata := providerMetadataFromSQLC(row.ProviderMetadata)
	return ModelCallContextRecord{
		ParentNormalModelCallContextID:  storeutil.IDFromPtr(row.ParentNormalModelCallContextID),
		ReplacesCheckpointID:            storeutil.IDFromPtr(row.ReplacesCheckpointID),
		SourceExcerptBytes:              intFromInt32Ptr(row.SourceExcerptBytes),
		RecoveryMaxOutputTokens:         intFromInt32Ptr(row.RecoveryMaxOutputTokens),
		RecoveryCheckpointRetainedBytes: intFromInt32Ptr(row.RecoveryCheckpointRetainedBytes),
		OptionalInputTargetTokens:       intFromInt32Ptr(row.OptionalInputTargetTokens),
		OptionalCompactionOutcome:       OptionalCompactionOutcome(stringFromSQLCText(row.OptionalCompactionOutcome)),
		RequestInputIdentity: requestInputIdentityFromColumns(
			row.RequestInputFingerprint, row.RequestInputItemCount,
		),
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ProjectID:                 row.ProjectID,
		AgentID:                   row.AgentID,
		OperationKind:             ModelCallOperation(row.OperationKind),
		AttemptNumber:             int(row.AttemptNumber),
		AgentConfigID:             row.AgentConfigID,
		ConfiguredModelRevisionID: row.ConfiguredModelRevisionID,
		InputEventSequence:        row.InputEventSequence,
		SourceEventSequenceEnd:    storeutil.ClonePtr(row.SourceEventSequenceEnd),
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
		ProviderReportedCostUSD: providerReportedCostUSDFromSQLC(row.ProviderReportedCostUsd),
		ProviderMetadata:        providerMetadata,
		CreatedAt:               row.CreatedAt,
		CompletedAt:             row.CompletedAt,
	}
}

func providerMetadataFromSQLC(raw json.RawMessage) modelenvelope.ProviderMetadata {
	var metadata modelenvelope.ProviderMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return modelenvelope.ProviderMetadata{}
	}
	return metadata
}

func intFromInt32Ptr(value *int32) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

func int32FromIntPtr(value *int) *int32 {
	if value == nil {
		return nil
	}
	converted := int32(*value)
	return &converted
}

func requestInputIdentityFromColumns(
	fingerprint *string,
	count *int32,
) *modelenvelope.RequestInputIdentity {
	if fingerprint == nil {
		return nil
	}
	return &modelenvelope.RequestInputIdentity{Fingerprint: *fingerprint, ItemCount: int(*count)}
}
