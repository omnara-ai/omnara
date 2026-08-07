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

const artifactRefPartKind = "artifact_ref"

type ContentBlockOwnerKind string

const (
	ContentBlockOwnerAgentInput     ContentBlockOwnerKind = "agent_input"
	ContentBlockOwnerModelOutput    ContentBlockOwnerKind = "model_output"
	ContentBlockOwnerToolCallResult ContentBlockOwnerKind = "tool_call_result"
)
