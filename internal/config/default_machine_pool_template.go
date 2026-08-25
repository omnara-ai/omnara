package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"gopkg.in/yaml.v3"

	"github.com/omnara-ai/omnara/internal/resourcemeta"
)

type defaultMachinePoolTemplateFile struct {
	Name                          string                `yaml:"name"`
	Description                   string                `yaml:"description"`
	Provider                      string                `yaml:"provider"`
	DefaultMachineCPU             *int                  `yaml:"default_machine_cpu"`
	DefaultMachineMemoryMB        *int                  `yaml:"default_machine_memory_mb"`
	DefaultMachineEnv             map[string]string     `yaml:"default_machine_env"`
	DefaultMachineSecretEnv       map[string]string     `yaml:"default_machine_secret_env"`
	DefaultMachineProviderOptions map[string]any        `yaml:"default_machine_provider_options"`
	DefaultCwd                    string                `yaml:"default_cwd"`
	ProviderConfig                map[string]any        `yaml:"provider_config"`
	ProviderAuthEnvVar            string                `yaml:"provider_auth_env_var"`
	RuntimeProtectionEnabled      bool                  `yaml:"runtime_protection_enabled"`
	MaxTotalMachines              *int32                `yaml:"max_total_machines"`
	MaxTotalCPU                   *int                  `yaml:"max_total_cpu"`
	MaxTotalMemoryMB              *int                  `yaml:"max_total_memory_mb"`
	MinMachineCPU                 *int                  `yaml:"min_machine_cpu"`
	MinMachineMemoryMB            *int                  `yaml:"min_machine_memory_mb"`
	MaxMachineCPU                 *int                  `yaml:"max_machine_cpu"`
	MaxMachineMemoryMB            *int                  `yaml:"max_machine_memory_mb"`
	DeleteAfterIdleMinutes        *int                  `yaml:"delete_after_idle_minutes"`
	Metadata                      resourcemeta.Metadata `yaml:"metadata"`
}

type defaultMachinePoolTemplatesFile struct {
	Pools []defaultMachinePoolTemplateFile `yaml:"pools"`
}

func loadDefaultMachinePoolTemplates(path string) ([]executionstore.DefaultMachinePoolTemplate, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var parsed defaultMachinePoolTemplatesFile
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES: trailing YAML document")
		}
		return nil, fmt.Errorf("parse OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES: %w", err)
	}
	if len(parsed.Pools) == 0 {
		return nil, errors.New("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES pools must contain at least one pool")
	}
	defaultPoolTemplates := make([]executionstore.DefaultMachinePoolTemplate, 0, len(parsed.Pools))
	seenNames := make(map[string]struct{}, len(parsed.Pools))
	for index, parsedPool := range parsed.Pools {
		label := fmt.Sprintf("pools[%d]", index)
		defaultPoolTemplate, err := defaultMachinePoolTemplateFromFile(parsedPool, label)
		if err != nil {
			return nil, err
		}
		if _, ok := seenNames[defaultPoolTemplate.Name]; ok {
			return nil, fmt.Errorf("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.name duplicates an earlier pool name", label)
		}
		seenNames[defaultPoolTemplate.Name] = struct{}{}
		defaultPoolTemplates = append(defaultPoolTemplates, defaultPoolTemplate)
	}
	return defaultPoolTemplates, nil
}

func defaultMachinePoolTemplateFromFile(
	parsed defaultMachinePoolTemplateFile,
	label string,
) (executionstore.DefaultMachinePoolTemplate, error) {
	if parsed.Name == "" {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.name is required",
			label,
		)
	}
	normalizedName, err := resourcename.CanonicalizeRequired("machine pool name", parsed.Name)
	if err != nil {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.name: %w",
			label,
			err,
		)
	}
	parsed.Name = normalizedName
	if parsed.Provider == "" {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.provider is required",
			label,
		)
	}
	parsed.ProviderAuthEnvVar = strings.TrimSpace(parsed.ProviderAuthEnvVar)
	if parsed.ProviderAuthEnvVar == "" {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.provider_auth_env_var is required",
			label,
		)
	}
	if strings.TrimSpace(os.Getenv(parsed.ProviderAuthEnvVar)) == "" {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.provider_auth_env_var %s is not set",
			label,
			parsed.ProviderAuthEnvVar,
		)
	}
	if parsed.MaxTotalMachines == nil {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.max_total_machines must be set (0 blocks machine provisioning until the cap is raised)",
			label,
		)
	}
	if *parsed.MaxTotalMachines < 0 {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.max_total_machines cannot be negative",
			label,
		)
	}
	if parsed.MaxTotalCPU != nil && *parsed.MaxTotalCPU < 0 {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.max_total_cpu cannot be negative",
			label,
		)
	}
	if parsed.MaxTotalMemoryMB != nil && *parsed.MaxTotalMemoryMB < 0 {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.max_total_memory_mb cannot be negative",
			label,
		)
	}
	if parsed.MinMachineCPU != nil && *parsed.MinMachineCPU < 0 {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.min_machine_cpu cannot be negative",
			label,
		)
	}
	if parsed.MinMachineMemoryMB != nil && *parsed.MinMachineMemoryMB < 0 {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.min_machine_memory_mb cannot be negative",
			label,
		)
	}
	if parsed.MaxMachineCPU != nil && *parsed.MaxMachineCPU <= 0 {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.max_machine_cpu must be positive when set",
			label,
		)
	}
	if parsed.MaxMachineMemoryMB != nil && *parsed.MaxMachineMemoryMB <= 0 {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.max_machine_memory_mb must be positive when set",
			label,
		)
	}
	if strings.ContainsRune(parsed.DefaultCwd, 0) {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.default_cwd cannot contain NUL",
			label,
		)
	}
	if parsed.DefaultMachineEnv == nil {
		parsed.DefaultMachineEnv = map[string]string{}
	}
	if parsed.DefaultMachineSecretEnv == nil {
		parsed.DefaultMachineSecretEnv = map[string]string{}
	}
	if parsed.DefaultMachineProviderOptions == nil {
		parsed.DefaultMachineProviderOptions = map[string]any{}
	}
	if parsed.ProviderConfig == nil {
		parsed.ProviderConfig = map[string]any{}
	}
	if parsed.Metadata == nil {
		parsed.Metadata = resourcemeta.Metadata{}
	}
	if err := parsed.Metadata.Validate(); err != nil {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.metadata: %w",
			label,
			err,
		)
	}
	defaultMachineEnv, err := json.Marshal(parsed.DefaultMachineEnv)
	if err != nil {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"marshal OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.default_machine_env: %w",
			label,
			err,
		)
	}
	defaultMachineSecretEnv, err := json.Marshal(parsed.DefaultMachineSecretEnv)
	if err != nil {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"marshal OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.default_machine_secret_env: %w",
			label,
			err,
		)
	}
	defaultMachineProviderOptions, err := json.Marshal(parsed.DefaultMachineProviderOptions)
	if err != nil {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"marshal OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.default_machine_provider_options: %w",
			label,
			err,
		)
	}
	providerConfig, err := json.Marshal(parsed.ProviderConfig)
	if err != nil {
		return executionstore.DefaultMachinePoolTemplate{}, fmt.Errorf(
			"marshal OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES %s.provider_config: %w",
			label,
			err,
		)
	}
	return executionstore.DefaultMachinePoolTemplate{
		Name:                          parsed.Name,
		Description:                   parsed.Description,
		Provider:                      parsed.Provider,
		DefaultMachineCPU:             parsed.DefaultMachineCPU,
		DefaultMachineMemoryMB:        parsed.DefaultMachineMemoryMB,
		DefaultMachineEnv:             json.RawMessage(defaultMachineEnv),
		DefaultMachineSecretEnv:       json.RawMessage(defaultMachineSecretEnv),
		DefaultMachineProviderOptions: json.RawMessage(defaultMachineProviderOptions),
		DefaultCwd:                    parsed.DefaultCwd,
		ProviderConfig:                json.RawMessage(providerConfig),
		ProviderAuthEnvVar:            parsed.ProviderAuthEnvVar,
		RuntimeProtectionEnabled:      parsed.RuntimeProtectionEnabled,
		MaxTotalMachines:              *parsed.MaxTotalMachines,
		MaxTotalCPU:                   parsed.MaxTotalCPU,
		MaxTotalMemoryMB:              parsed.MaxTotalMemoryMB,
		MinMachineCPU:                 parsed.MinMachineCPU,
		MinMachineMemoryMB:            parsed.MinMachineMemoryMB,
		MaxMachineCPU:                 parsed.MaxMachineCPU,
		MaxMachineMemoryMB:            parsed.MaxMachineMemoryMB,
		DeleteAfterIdleMinutes:        parsed.DeleteAfterIdleMinutes,
		Metadata:                      parsed.Metadata,
	}, nil
}
