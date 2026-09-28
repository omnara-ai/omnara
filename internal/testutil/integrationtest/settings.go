package integrationtest

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

// ChatSettings is fixture data for the two shipped chat definitions.
func ChatSettings(channel string, profiles ...uuid.UUID) json.RawMessage {
	ids := make([]string, len(profiles))
	for i, id := range profiles {
		ids[i] = ProfileID(id)
	}
	launcher := map[string]any{"profiles": ids}
	if channel != "" {
		launcher["channel_id"] = channel
	}
	return encode(map[string]any{"launcher": launcher})
}
func GitHubSettings(profile uuid.UUID, trigger, repository string) json.RawMessage {
	launcher := map[string]any{"profile": ProfileID(profile), "trigger": trigger}
	if repository != "" {
		launcher["repository_id"] = repository
	}
	return encode(map[string]any{"launcher": launcher})
}
func ProfileID(id uuid.UUID) string {
	value, err := publicid.Encode(publicid.KindAgentProfile, id)
	if err != nil {
		panic(err)
	}
	return value
}
func encode(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
