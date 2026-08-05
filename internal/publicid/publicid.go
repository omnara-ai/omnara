package publicid

import (
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type Kind string

const (
	KindOrganization            Kind = "organization"
	KindInstallation            Kind = "installation"
	KindUser                    Kind = "user"
	KindActor                   Kind = "actor"
	KindProject                 Kind = "project"
	KindAgent                   Kind = "agent"
	KindAgentConfig             Kind = "agent_config"
	KindAgentProfile            Kind = "agent_profile"
	KindIntegrationInstall      Kind = "integration_install"
	KindIntegrationTarget       Kind = "integration_target"
	KindAgentEvent              Kind = "agent_event"
	KindAgentInput              Kind = "agent_input"
	KindAgentMachineBinding     Kind = "agent_machine_binding"
	KindAgentTurn               Kind = "agent_turn"
	KindAgentInteraction        Kind = "agent_interaction"
	KindModelCallContext        Kind = "model_call_context"
	KindContextCheckpoint       Kind = "context_checkpoint"
	KindToolCall                Kind = "tool_call"
	KindMachinePool             Kind = "machine_pool"
	KindMachine                 Kind = "machine"
	KindMachineDaemonToken      Kind = "machine_daemon_token"
	KindDaemonRuntime           Kind = "daemon_runtime"
	KindProjectMachineGrant     Kind = "project_machine_grant"
	KindProjectMachinePoolGrant Kind = "project_machine_pool_grant"
	KindProcess                 Kind = "process"
	KindProcessAction           Kind = "process_action"
	KindArtifact                Kind = "artifact"
	KindPersonalAccessToken     Kind = "personal_access_token"
	KindOrgInvitation           Kind = "org_invitation"
	KindSecret                  Kind = "secret"
	KindSecretGrant             Kind = "secret_grant"
	KindModelProviderConfig     Kind = "model_provider_config"
	KindConfiguredModel         Kind = "configured_model"
	KindConfiguredModelRevision Kind = "configured_model_revision"
	KindProjectModelGrant       Kind = "project_model_grant"
	KindSkill                   Kind = "skill"
	KindSkillRevision           Kind = "skill_revision"
	KindSkillGrant              Kind = "skill_grant"
	KindOrgAPIKey               Kind = "org_api_key"
)

var kindPrefixes = map[Kind]string{
	KindOrganization:            "org",
	KindInstallation:            "inst",
	KindUser:                    "usr",
	KindActor:                   "actr",
	KindProject:                 "proj",
	KindAgent:                   "agt",
	KindAgentConfig:             "acfg",
	KindAgentProfile:            "aprf",
	KindIntegrationInstall:      "iin",
	KindIntegrationTarget:       "itgt",
	KindAgentEvent:              "evt",
	KindAgentInput:              "ain",
	KindAgentMachineBinding:     "amb",
	KindAgentTurn:               "trn",
	KindAgentInteraction:        "int",
	KindModelCallContext:        "mcc",
	KindContextCheckpoint:       "ccp",
	KindToolCall:                "tcl",
	KindMachinePool:             "mpo",
	KindMachine:                 "mch",
	KindMachineDaemonToken:      "mdt",
	KindDaemonRuntime:           "drt",
	KindProjectMachineGrant:     "pmg",
	KindProjectMachinePoolGrant: "pmpg",
	KindProcess:                 "prc",
	KindProcessAction:           "pac",
	KindArtifact:                "art",
	KindPersonalAccessToken:     "pat",
	KindOrgInvitation:           "oinv",
	KindSecret:                  "sec",
	KindSecretGrant:             "sgr",
	KindModelProviderConfig:     "mpc",
	KindConfiguredModel:         "mdl",
	KindConfiguredModelRevision: "mrev",
	KindProjectModelGrant:       "pmog",
	KindSkill:                   "skl",
	KindSkillRevision:           "skr",
	KindSkillGrant:              "skg",
	KindOrgAPIKey:               "oak",
}

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

var (
	ErrMalformed = errors.New("malformed public id")
	ErrNilUUID   = errors.New("public id cannot encode nil uuid")
)

type WrongPrefixError struct {
	Want string
	Got  string
}

func (e WrongPrefixError) Error() string {
	if e.Got == "" {
		return fmt.Sprintf("wrong public id prefix: want %q", e.Want)
	}
	return fmt.Sprintf("wrong public id prefix: want %q, got %q", e.Want, e.Got)
}

func Prefix(kind Kind) (string, bool) {
	prefix, ok := kindPrefixes[kind]
	return prefix, ok
}

func Encode(kind Kind, id uuid.UUID) (string, error) {
	if id == uuid.Nil {
		return "", ErrNilUUID
	}
	prefix, ok := Prefix(kind)
	if !ok {
		return "", fmt.Errorf("unknown public id kind: %s", kind)
	}
	return prefix + "_" + strings.ToLower(encoding.EncodeToString(id[:])), nil
}

func Decode(kind Kind, value string) (uuid.UUID, error) {
	prefix, ok := Prefix(kind)
	if !ok {
		return uuid.Nil, fmt.Errorf("unknown public id kind: %s", kind)
	}
	got, token, ok := strings.Cut(value, "_")
	if !ok || got == "" || token == "" {
		return uuid.Nil, ErrMalformed
	}
	if got != prefix {
		return uuid.Nil, WrongPrefixError{Want: prefix, Got: got}
	}
	if len(token) != 26 {
		return uuid.Nil, ErrMalformed
	}
	raw, err := encoding.DecodeString(strings.ToUpper(token))
	if err != nil || len(raw) != 16 {
		return uuid.Nil, ErrMalformed
	}
	id, err := uuid.FromBytes(raw)
	if err != nil {
		return uuid.Nil, ErrMalformed
	}
	if id == uuid.Nil {
		return uuid.Nil, ErrNilUUID
	}
	return id, nil
}
