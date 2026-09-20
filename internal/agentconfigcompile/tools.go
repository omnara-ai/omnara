package agentconfigcompile

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
)

// ToolsFromSource previews selected capabilities using the same project-scoped
// app and secret resolution as config compilation. It still permits
// incomplete instruction/model fields while editing.
func ToolsFromSource(
	ctx context.Context,
	store *storage.Store,
	orgID, projectID uuid.UUID,
	base agentconfig.CompileOptions,
	sourceFormat agentconfig.SourceFormat,
	source string,
) ([]agentconfig.ResolvedTool, error) {
	return agentconfig.ToolsFromSourceWithOptions(
		sourceFormat, []byte(source), options(ctx, store, orgID, projectID, base),
	)
}
