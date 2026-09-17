package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const uploadArtifactProcessTimeoutSeconds = 30

type uploadArtifactAuthorization struct {
	AgentMachineBindingID string `json:"agent_machine_binding_id"`
	Path                  string `json:"path"`
}

type downloadArtifactAuthorization struct {
	AgentMachineBindingID string `json:"agent_machine_binding_id"`
	ArtifactID            string `json:"artifact_id"`
	Path                  string `json:"path"`
}

func uploadArtifactAuthorizationInput(
	bindingID uuid.UUID,
	path string,
) (json.RawMessage, error) {
	input, err := marshalJSON(uploadArtifactAuthorization{
		AgentMachineBindingID: bindingID.String(),
		Path:                  path,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal artifact upload authorization: %w", err)
	}
	return input, nil
}

func downloadArtifactAuthorizationInput(
	bindingID uuid.UUID,
	artifactID string,
	path string,
) (json.RawMessage, error) {
	input, err := marshalJSON(downloadArtifactAuthorization{
		AgentMachineBindingID: bindingID.String(),
		ArtifactID:            artifactID,
		Path:                  path,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal artifact download authorization: %w", err)
	}
	return input, nil
}

func uploadArtifactProcessInput(
	toolCallID string,
	path string,
) executionstore.CreateProcessInput {
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))
	command := fmt.Sprintf(
		`"$OMNARA_HOME/bin/omnarad" __omnara_upload_artifact %s %s`,
		toolCallID,
		encodedPath,
	)
	return executionstore.CreateProcessInput{
		IOMode:         processcmd.IOModePipe,
		Command:        command,
		ShellSelector:  processcmd.ShellDefault,
		InitialWaitMS:  processaction.MaxWaitMilliseconds,
		TimeoutSeconds: uploadArtifactProcessTimeoutSeconds,
	}
}

func downloadArtifactProcessInput(
	toolCallID string,
	artifactID string,
	path string,
) executionstore.CreateProcessInput {
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))
	command := fmt.Sprintf(
		`"$OMNARA_HOME/bin/omnarad" __omnara_download_artifact %s %s %s`,
		toolCallID,
		artifactID,
		encodedPath,
	)
	return executionstore.CreateProcessInput{
		IOMode:         processcmd.IOModePipe,
		Command:        command,
		ShellSelector:  processcmd.ShellDefault,
		InitialWaitMS:  processaction.MaxWaitMilliseconds,
		TimeoutSeconds: 0,
	}
}
