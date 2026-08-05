package executionstore

import (
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type contextCheckpointFields struct {
	id                             ID
	projectID                      ID
	agentID                        ID
	summarizedThroughEventSequence int64
	producerModelCallContextID     ID
	checkpointEventID              ID
	summary                        string
	createdAt                      time.Time
	checkpointEventSequence        int64
}

func contextCheckpointRecordFromGetSQLC(row dbsqlc.GetContextCheckpointRow) ContextCheckpointRecord {
	return contextCheckpointRecordFromFields(contextCheckpointFields{
		id: row.ID, projectID: row.ProjectID, agentID: row.AgentID,
		summarizedThroughEventSequence: row.SummarizedThroughEventSequence,
		producerModelCallContextID:     row.ProducerModelCallContextID,
		checkpointEventID:              row.CheckpointEventID, summary: row.Summary,
		createdAt: row.CreatedAt, checkpointEventSequence: row.CheckpointEventSequence,
	})
}

func contextCheckpointRecordFromLatestSQLC(
	row dbsqlc.GetLatestApplicableContextCheckpointRow,
) ContextCheckpointRecord {
	return contextCheckpointRecordFromFields(contextCheckpointFields{
		id: row.ID, projectID: row.ProjectID, agentID: row.AgentID,
		summarizedThroughEventSequence: row.SummarizedThroughEventSequence,
		producerModelCallContextID:     row.ProducerModelCallContextID,
		checkpointEventID:              row.CheckpointEventID, summary: row.Summary,
		createdAt: row.CreatedAt, checkpointEventSequence: row.CheckpointEventSequence,
	})
}

func contextCheckpointRecordFromProducerContextSQLC(
	row dbsqlc.GetContextCheckpointByProducerContextRow,
) ContextCheckpointRecord {
	return contextCheckpointRecordFromFields(contextCheckpointFields{
		id: row.ID, projectID: row.ProjectID, agentID: row.AgentID,
		summarizedThroughEventSequence: row.SummarizedThroughEventSequence,
		producerModelCallContextID:     row.ProducerModelCallContextID,
		checkpointEventID:              row.CheckpointEventID, summary: row.Summary,
		createdAt: row.CreatedAt, checkpointEventSequence: row.CheckpointEventSequence,
	})
}

func contextCheckpointRecordFromFields(row contextCheckpointFields) ContextCheckpointRecord {
	return ContextCheckpointRecord{
		ID:                             row.id,
		ProjectID:                      row.projectID,
		AgentID:                        row.agentID,
		SummarizedThroughEventSequence: row.summarizedThroughEventSequence,
		ProducerModelCallContextID:     row.producerModelCallContextID,
		CheckpointEventID:              row.checkpointEventID,
		CheckpointEventSequence:        row.checkpointEventSequence,
		Summary:                        row.summary,
		CreatedAt:                      row.createdAt,
	}
}
