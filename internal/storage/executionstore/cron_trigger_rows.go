package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func cronTriggerRecordFromInsertSQLC(row dbsqlc.InsertCronTriggerRow, orgID ID) CronTriggerRecord {
	return CronTriggerRecord{
		ID:              row.ID,
		OrgID:           orgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		Target:          cronTriggerTargetFromColumns(row.AgentProfileID, row.AgentID),
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func cronTriggerRecordFromGetSQLC(row dbsqlc.GetCronTriggerRow) CronTriggerRecord {
	return CronTriggerRecord{
		ID:              row.ID,
		OrgID:           row.OrgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		Target:          cronTriggerTargetFromColumns(row.AgentProfileID, row.AgentID),
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func cronTriggerRecordFromGetByIdempotencySQLC(
	row dbsqlc.GetCronTriggerByIdempotencyKeyRow,
) CronTriggerRecord {
	return CronTriggerRecord{
		ID:              row.ID,
		OrgID:           row.OrgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		Target:          cronTriggerTargetFromColumns(row.AgentProfileID, row.AgentID),
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func cronTriggerRecordFromListSQLC(row dbsqlc.ListCronTriggersForProjectRow) CronTriggerRecord {
	return CronTriggerRecord{
		ID:              row.ID,
		OrgID:           row.OrgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		Target:          cronTriggerTargetFromColumns(row.AgentProfileID, row.AgentID),
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func cronTriggerRecordFromUpdateSQLC(row dbsqlc.UpdateCronTriggerRow, orgID ID) CronTriggerRecord {
	return CronTriggerRecord{
		ID:              row.ID,
		OrgID:           orgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		Target:          cronTriggerTargetFromColumns(row.AgentProfileID, row.AgentID),
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}
