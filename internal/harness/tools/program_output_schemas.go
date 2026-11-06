package tools

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/programoutput"
)

type (
	webSearchProgramResult    = programoutput.WebSearch
	webFetchProgramResult     = programoutput.WebFetch
	githubPendingReviewResult = programoutput.GitHubPendingReview
)

// programOutputSchema returns the schema of the JSON value commands get from
// spec, or nil when commands cannot call it.
func programOutputSchema(spec modelcontext.ToolSpec) (json.RawMessage, error) {
	return modelcontext.ProgramOutputSchema(spec)
}
