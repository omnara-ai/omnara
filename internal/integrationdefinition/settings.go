package integrationdefinition

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/jsonschema"
)

const MaxSettingsBytes = 128 * 1024

// SettingsDefinition owns the complete settings document, including any launcher.
// The kernel stores JSON without interpreting an integration's fields.
type SettingsDefinition struct {
	InputSchema      json.RawMessage
	Description      string
	ValidateSettings func(json.RawMessage) error
}

type LauncherDefinition struct {
	Matches         func(json.RawMessage, Event) bool
	AuthorizeIntent func(json.RawMessage, Event, LaunchIntent) error
	// MayLaunchWithoutSelection protects early replies while a matching event is
	// enriched. It is a pure candidate check, not authorization or a saved choice.
	MayLaunchWithoutSelection func(json.RawMessage, Event) bool
}

// LaunchIntent contains only admission facts, not a public settings contract.
type LaunchIntent struct {
	LaunchKey string
	ProfileID uuid.UUID
}

func ValidateSettings(kind Kind, raw json.RawMessage) (json.RawMessage, error) {
	definition, ok := Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("unknown integration kind %q", kind)
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > MaxSettingsBytes {
		return nil, fmt.Errorf("integration settings exceed %d bytes", MaxSettingsBytes)
	}
	if err := dbsafe.JSONStrings(raw); err != nil {
		return nil, err
	}
	if definition.Settings == nil {
		if err := jsonschema.Validate(json.RawMessage(`{"type":"object","additionalProperties":false}`), raw); err != nil {
			return nil, err
		}
	} else if err := definition.Settings.Validate(raw); err != nil {
		return nil, err
	}
	return jsoncanonical.Normalize(raw)
}

func (s SettingsDefinition) Validate(raw json.RawMessage) error {
	if err := jsonschema.Validate(s.InputSchema, raw); err != nil {
		return err
	}
	if s.ValidateSettings != nil {
		return s.ValidateSettings(raw)
	}
	return nil
}

func (d Definition) MatchesLaunch(settings json.RawMessage, event Event) bool {
	return d.Launcher != nil && d.Launcher.Matches != nil && d.Launcher.Matches(settings, event)
}

func (d Definition) MayLaunchWithoutSelection(settings json.RawMessage, event Event) bool {
	return d.MatchesLaunch(settings, event) && d.Launcher.MayLaunchWithoutSelection != nil &&
		d.Launcher.MayLaunchWithoutSelection(settings, event)
}

func (d Definition) AuthorizeLaunch(settings json.RawMessage, event Event, intent LaunchIntent) error {
	if !d.MatchesLaunch(settings, event) || d.Launcher.AuthorizeIntent == nil {
		return fmt.Errorf("integration does not authorize this launch")
	}
	return d.Launcher.AuthorizeIntent(settings, event, intent)
}
