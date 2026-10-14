package tools

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
)

func (e Executor) loadMessageAttachment(ctx context.Context, turn Turn, filePath string) (string, []byte, error) {
	var filename string
	var content []byte
	var err error
	if strings.HasPrefix(filePath, memorystore.Root+"/") {
		_, content, err = e.readMemoryFile(ctx, turn, filePath)
		filename = path.Base(filePath)
	} else {
		artifactID, parseErr := resolveArtifactPath(filePath)
		if parseErr != nil {
			return "", nil, parseErr
		}
		artifactContent, artifact, readErr := e.Store.Artifacts().
			GetArtifactBlob(ctx, turn.ProjectID, turn.AgentID, artifactID)
		content, err = artifactContent, readErr
		filename = modelcontext.MediaFilename(artifact.Filename, artifact.ContentType)
	}
	if err != nil {
		return "", nil, fmt.Errorf("load attachment %s: %w", filePath, err)
	}
	if len(content) == 0 {
		return "", nil, fmt.Errorf("attachment %s is empty", filePath)
	}
	return filename, content, nil
}
