package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func publicCompiledDefinition(raw json.RawMessage) (json.RawMessage, error) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	model, ok := root["model"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("compiled config model must be an object")
	}
	if err := publicCompiledID(model, "configured_model_id", publicid.KindConfiguredModel); err != nil {
		return nil, err
	}
	if err := publicCompiledObjects(root, "machine_sources", func(machine map[string]any) error {
		for key, kind := range map[string]publicid.Kind{
			"machine_id":      publicid.KindMachine,
			"machine_pool_id": publicid.KindMachinePool,
		} {
			if err := publicCompiledID(machine, key, kind); err != nil {
				return err
			}
		}
		if refs, ok := machine["secret_env_overlay"].(map[string]any); ok {
			for key, value := range refs {
				if value != nil {
					if err := publicCompiledID(refs, key, publicid.KindSecret); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := publicCompiledObjects(root, "skills", func(skill map[string]any) error {
		if err := publicCompiledID(skill, "id", publicid.KindSkill); err != nil {
			return err
		}
		skill["public_id"] = skill["id"]
		delete(skill, "id")
		return nil
	}); err != nil {
		return nil, err
	}
	if err := publicCompiledObjects(root, "subagents", func(subagent map[string]any) error {
		return publicCompiledID(subagent, "profile_id", publicid.KindAgentProfile)
	}); err != nil {
		return nil, err
	}
	if err := publicCompiledObjects(root, "mcp", func(server map[string]any) error {
		if auth, ok := server["auth"].(map[string]any); ok {
			return publicCompiledID(auth, "secret_id", publicid.KindSecret)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func publicCompiledID(object map[string]any, key string, kind publicid.Kind) error {
	value, exists := object[key]
	if !exists {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("compiled %s must be a UUID", key)
	}
	id, err := uuid.Parse(text)
	if err != nil {
		return err
	}
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		return err
	}
	object[key] = encoded
	return nil
}

func publicCompiledObjects(root map[string]any, key string, convert func(map[string]any) error) error {
	visit := func(value any) error {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("compiled %s entries must be objects", key)
		}
		return convert(object)
	}
	switch values := root[key].(type) {
	case nil:
		return nil
	case []any:
		for _, value := range values {
			if err := visit(value); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, value := range values {
			if err := visit(value); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid compiled %s", key)
	}
	return nil
}
