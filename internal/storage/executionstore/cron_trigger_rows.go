package executionstore

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
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
	var lastRun *CronTriggerLastRun
	if len(row.LastRun) > 0 {
		if err := json.Unmarshal(row.LastRun, &lastRun); err != nil {
			return CronTriggerRecord{}, fmt.Errorf("decode cron trigger last run: %w", err)
		}
	}
	return CronTriggerRecord{
		ID:        row.ID,
		OrgID:     row.OrgID,
		ProjectID: row.ProjectID,
		Name:      row.Name,
		Target: cronTriggerTargetFromColumns(
			row.AgentProfileID,
			row.AgentID,
			row.DeliveryMode,
			row.AppID,
			row.AppSettings,
		),
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		FailureReport:   failureReport,
		LastRun:         lastRun,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

func cronTriggerRecordFromWriteSQLC(
	row dbsqlc.InsertCronTriggerRow,
	orgID uuid.UUID,
) (CronTriggerRecord, error) {
	return cronTriggerRecordFromSQLC(dbsqlc.GetCronTriggerRow{
		ID:              row.ID,
		OrgID:           orgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		AgentProfileID:  row.AgentProfileID,
		AgentID:         row.AgentID,
		AppID:           row.AppID,
		AppSettings:     row.AppSettings,
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		DeliveryMode:    row.DeliveryMode,
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
		LastRun:         row.LastRun,
		ID:              row.ID,
		OrgID:           row.OrgID,
		ProjectID:       row.ProjectID,
		Name:            row.Name,
		AgentProfileID:  row.AgentProfileID,
		AgentID:         row.AgentID,
		AppID:           row.AppID,
		AppSettings:     row.AppSettings,
		CronExpression:  row.CronExpression,
		Timezone:        row.Timezone,
		MessageTemplate: row.MessageTemplate,
		DeliveryMode:    row.DeliveryMode,
		Enabled:         row.Enabled,
		LastFiredAt:     row.LastFiredAt,
		NextFireAfter:   row.NextFireAfter,
		FailureReport:   row.FailureReport,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	})
}
