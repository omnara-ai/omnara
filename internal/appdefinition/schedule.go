package appdefinition

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/jsonschema"
)

const MaxScheduleSettingsBytes = 128 * 1024

// ScheduleDefinition describes an app-owned scheduled action. Presentation hints
// in InputSchema do not grant authority or change server-side resource handling.
// ValidatePlan is pure: storage invokes it while enforcing its transaction gates.
type ScheduleDefinition struct {
	InputSchema      json.RawMessage
	Description      string
	ValidateSettings func(json.RawMessage) error
	ValidatePlan     func(SchedulePlan) error
}

// ScheduleSlot exposes only the facts needed to authorize planned app work.
// Project, actor, lease, selection identity and config membership are verified
// separately by storage; app definitions never receive a database transaction.
type ScheduleSlot struct {
	Key       string
	Scope     Scope
	ProfileID uuid.UUID
	Content   json.RawMessage
}

type SchedulePlan struct {
	AppName    string
	Occurrence cronschedule.Occurrence
	Settings   json.RawMessage
	Slots      []ScheduleSlot
}

func ValidateScheduleSettings(appType Type, settings json.RawMessage) (json.RawMessage, error) {
	definition, ok := Lookup(appType)
	if !ok || definition.Schedule == nil {
		return nil, fmt.Errorf("app does not support schedules")
	}
	if len(settings) == 0 || len(settings) > MaxScheduleSettingsBytes {
		return nil, fmt.Errorf("app schedule settings must contain at most %d bytes", MaxScheduleSettingsBytes)
	}
	schedule := definition.Schedule
	if err := jsonschema.Validate(schedule.InputSchema, settings); err != nil {
		return nil, err
	}
	if schedule.ValidateSettings != nil {
		if err := schedule.ValidateSettings(settings); err != nil {
			return nil, err
		}
	}
	return jsoncanonical.Normalize(settings)
}

func ValidateSchedulePlan(appType Type, plan SchedulePlan) error {
	definition, ok := Lookup(appType)
	if !ok || definition.Schedule == nil || definition.Schedule.ValidatePlan == nil {
		return fmt.Errorf("app does not authorize scheduled work")
	}
	return definition.Schedule.ValidatePlan(plan)
}
