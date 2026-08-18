package executionstore

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func cronTriggerRecordFromSQLC(row dbsqlc.GetCronTriggerRow) (CronTriggerRecord, error) {
	var failureReport *CronTriggerFailureReport
	if row.FailureReport != nil {
		failureReport = &CronTriggerFailureReport{}
		if err := json.Unmarshal(*row.FailureReport, failureReport); err != nil {
			return CronTriggerRecord{}, fmt.Errorf("decode cron trigger failure report: %w", err)
		}
	}
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
		FailureReport:   failureReport,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

func cronTriggerRecordFromWriteSQLC(
	row dbsqlc.InsertCronTriggerRow,
	orgID ID,
) (CronTriggerRecord, error) {
	return cronTriggerRecordFromSQLC(dbsqlc.GetCronTriggerRow{
		ID:              row.ID,
		OrgID:           orgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		AgentProfileID:  row.AgentProfileID,
		AgentID:         row.AgentID,
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		FailureReport:   row.FailureReport,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	})
}

func cronTriggerRecordFromListSQLC(
	row dbsqlc.ListCronTriggersForProjectRow,
) (CronTriggerRecord, error) {
	return cronTriggerRecordFromSQLC(dbsqlc.GetCronTriggerRow{
		ID:              row.ID,
		OrgID:           row.OrgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		AgentProfileID:  row.AgentProfileID,
		AgentID:         row.AgentID,
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		FailureReport:   row.FailureReport,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	})
}
