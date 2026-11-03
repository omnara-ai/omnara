package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

func executionContent(blocks []CreateContentBlockInput) ([]agentexecution.Content, error) {
	result := make([]agentexecution.Content, len(blocks))
	for i, block := range blocks {
		metadata, err := block.Metadata.JSON()
		if err != nil {
			return nil, err
		}
		result[i] = agentexecution.Content{
			Ordinal:    block.Ordinal,
			Kind:       string(block.BlockKind),
			Text:       block.TextContent,
			Data:       block.StructuredData,
			ArtifactID: block.ArtifactID,
			ToolID:     block.ToolCallID,
			Exclude:    block.ExcludeFromModelContext,
			Metadata:   metadata,
		}
	}
	return result, nil
}
