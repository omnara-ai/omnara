package executionstore

type ContentBlockKind string

const (
	ContentBlockKindText           ContentBlockKind = "text"
	ContentBlockKindStructuredData ContentBlockKind = "structured_data"
	ContentBlockKindArtifact       ContentBlockKind = "artifact"
	ContentBlockKindReasoning      ContentBlockKind = "reasoning"
	ContentBlockKindToolCall       ContentBlockKind = "tool_call"
	ContentBlockKindError          ContentBlockKind = "error"
)
