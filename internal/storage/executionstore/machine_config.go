package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/envname"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxResolvedEnvironmentBytes = 1024 * 1024

type MachinePoolResources struct {
	CPU      int64
	MemoryMB int64
}

type MachineProvisioningConfig struct {
	CPU             *int
	MemoryMB        *int
	ProviderOptions map[string]json.RawMessage
}

type MachineResourceFacts struct {
	CPU      *int
	MemoryMB *int
}

type MachinePoolProviderPolicy struct {
	DefaultProvisioning MachineProvisioningConfig
	ResourceLimits      MachineResourceLimits
	ProviderConfig      json.RawMessage
}

type MachineProvisioningOverlay struct {
	CPU             *int
	MemoryMB        *int
	ProviderOptions map[string]json.RawMessage
}

type MachineEnvironment struct {
	Env       map[string]string
	SecretEnv map[string]string
}

type MachineEnvironmentOverlay struct {
	Env       map[string]*string
	SecretEnv map[string]*string
}

type MachineBindingConfig struct {
	Cwd                string
	EnvironmentOverlay MachineEnvironmentOverlay
}

type ResolvedPoolMachine struct {
	Provisioning       MachineProvisioningConfig
	MachineCwd         string
	MachineEnvironment MachineEnvironment
	BindingConfig      MachineBindingConfig
}

type machinePoolDefaults struct {
	Provisioning MachineProvisioningConfig
	Environment  MachineEnvironment
}

type MachinePoolProviders interface {
	ValidatePool(
		provider string,
		policy MachinePoolProviderPolicy,
	) error
	ResolveMachineProviderOptions(
		provider string,
		defaultOptions map[string]json.RawMessage,
		projectOptions map[string]json.RawMessage,
		agentOptions map[string]json.RawMessage,
	) (map[string]json.RawMessage, error)
	BuildMachineProvisioningIntent(
		provider string,
		policy MachinePoolProviderPolicy,
		machineProvisioning MachineProvisioningConfig,
	) (MachineProvisioningConfig, error)
}

func (s *Store) ResolveMachineProvisioning(
	provider string,
	policy MachinePoolProviderPolicy,
	projectOverlay, agentOverlay MachineProvisioningOverlay,
) (MachineProvisioningConfig, error) {
	machineProvisioning := policy.DefaultProvisioning
	if projectOverlay.CPU != nil {
		machineProvisioning.CPU = projectOverlay.CPU
	}
	if projectOverlay.MemoryMB != nil {
		machineProvisioning.MemoryMB = projectOverlay.MemoryMB
	}
	if agentOverlay.CPU != nil {
		machineProvisioning.CPU = agentOverlay.CPU
	}
	if agentOverlay.MemoryMB != nil {
		machineProvisioning.MemoryMB = agentOverlay.MemoryMB
	}
	providerOptions, err := s.machinePoolProviders.ResolveMachineProviderOptions(
		provider,
		policy.DefaultProvisioning.ProviderOptions,
		projectOverlay.ProviderOptions,
		agentOverlay.ProviderOptions,
	)
	if err != nil {
		return MachineProvisioningConfig{}, storeerr.InvalidRequest(err)
	}
	machineProvisioning.ProviderOptions = providerOptions
	if err := validateMachineProvisioning(machineProvisioning); err != nil {
		return MachineProvisioningConfig{}, storeerr.InvalidRequest(err)
	}
	resolved, err := s.machinePoolProviders.BuildMachineProvisioningIntent(
		provider,
		policy,
		machineProvisioning,
	)
	if err != nil {
		return MachineProvisioningConfig{}, storeerr.InvalidRequest(err)
	}
	return resolved, nil
}

func (s *Store) resolvePoolMachineProvisioningConfig(
	poolGrant dbsqlc.GetActiveProjectMachinePoolGrantForLaunchRow,
	agentMachine agentconfig.RuntimeMachine,
) (MachineProvisioningConfig, error) {
	poolDefault, err := MachineProvisioningFromDefaults(
		intPtrFromSQLC(poolGrant.DefaultMachineCpu),
		intPtrFromSQLC(poolGrant.DefaultMachineMemoryMb),
		poolGrant.DefaultMachineProviderOptions,
	)
	if err != nil {
		return MachineProvisioningConfig{}, fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	projectOverlay, err := machineProvisioningOverlayFromColumns(
		intPtrFromSQLC(poolGrant.GrantDefaultMachineCpu),
		intPtrFromSQLC(poolGrant.GrantDefaultMachineMemoryMb),
		poolGrant.GrantDefaultMachineProviderOptionsOverlay,
	)
	if err != nil {
		return MachineProvisioningConfig{}, fmt.Errorf("project machine pool grant default_machine fields: %w", err)
	}
	policy := MachinePoolProviderPolicy{
		DefaultProvisioning: poolDefault,
		ResourceLimits: MachineResourceLimits{
			MaxTotalCPU:        intPtrFromSQLC(poolGrant.PoolMaxTotalCpu),
			MaxTotalMemoryMB:   intPtrFromSQLC(poolGrant.PoolMaxTotalMemoryMb),
			MaxMachineCPU:      intPtrFromSQLC(poolGrant.PoolMaxMachineCpu),
			MaxMachineMemoryMB: intPtrFromSQLC(poolGrant.PoolMaxMachineMemoryMb),
		},
		ProviderConfig: poolGrant.ProviderConfig,
	}
	return s.ResolveMachineProvisioning(
		poolGrant.Provider,
		policy,
		projectOverlay,
		runtimeMachineProvisioningOverlay(agentMachine),
	)
}

func (s *Store) ResolvePoolMachineTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	poolGrant dbsqlc.GetActiveProjectMachinePoolGrantForLaunchRow,
	agentMachine agentconfig.RuntimeMachine,
) (ResolvedPoolMachine, error) {
	provisioning, err := s.resolvePoolMachineProvisioningConfig(poolGrant, agentMachine)
	if err != nil {
		return ResolvedPoolMachine{}, err
	}
	poolDefaultEnvironment, err := MachineEnvironmentFromColumns(
		poolGrant.DefaultMachineEnv,
		poolGrant.DefaultMachineSecretEnv,
	)
	if err != nil {
		return ResolvedPoolMachine{}, fmt.Errorf("machine pool default_machine fields: %w", err)
	}
	projectEnvironmentOverlay, err := machineEnvironmentOverlayFromColumns(
		poolGrant.GrantDefaultMachineEnvOverlay,
		poolGrant.GrantDefaultMachineSecretEnvOverlay,
	)
	if err != nil {
		return ResolvedPoolMachine{}, fmt.Errorf("project machine pool grant default_machine fields: %w", err)
	}
	machineEnv, err := resolveMachineEnvironmentTx(
		ctx,
		qtx,
		poolGrant.OrgID,
		poolGrant.ProjectID,
		poolDefaultEnvironment,
		projectEnvironmentOverlay,
	)
	if err != nil {
		return ResolvedPoolMachine{}, err
	}
	bindingEnvironmentOverlay := runtimeMachineEnvironmentOverlay(agentMachine)
	if _, err := resolveMachineEnvironmentTx(
		ctx,
		qtx,
		poolGrant.OrgID,
		poolGrant.ProjectID,
		machineEnv,
		bindingEnvironmentOverlay,
	); err != nil {
		return ResolvedPoolMachine{}, err
	}
	return ResolvedPoolMachine{
		Provisioning:       provisioning,
		MachineCwd:         resolveMachineCwd(poolGrant.DefaultCwd, poolGrant.GrantDefaultCwd),
		MachineEnvironment: machineEnv,
		BindingConfig: MachineBindingConfig{
			Cwd:                agentMachine.Cwd,
			EnvironmentOverlay: bindingEnvironmentOverlay,
		},
	}, nil
}

func (s *Store) ResolveMachineProviderAuthToken(
	ctx context.Context,
	orgID ID,
	managementKind management.Kind,
	providerAuthSecretID ID,
	providerAuthEnvVar string,
) (string, error) {
	switch managementKind {
	case management.Tenant:
		if isNilID(providerAuthSecretID) {
			return "", errors.New("provider_auth_secret_id is required")
		}
		credential, err := s.secrets.ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
			OrgID:    orgID,
			SecretID: providerAuthSecretID,
			Kind:     secretstore.SecretKindGeneric,
		})
		if err != nil {
			if errors.Is(err, storeerr.ErrNotFound) {
				return "", errors.New("machine pool provider auth secret is unavailable")
			}
			return "", err
		}
		token := strings.TrimSpace(credential.Payload[secrets.KeyValue])
		if token == "" {
			return "", errors.New("machine pool provider auth secret value is required")
		}
		return token, nil
	case management.Cluster:
		token := strings.TrimSpace(os.Getenv(providerAuthEnvVar))
		if token == "" {
			return "", fmt.Errorf("provider auth env var %s is required", providerAuthEnvVar)
		}
		return token, nil
	default:
		return "", fmt.Errorf("machine pool management kind %q is invalid", managementKind)
	}
}

func (s *Store) validatePoolDefaultsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateMachinePoolInput,
	defaults machinePoolDefaults,
) error {
	if _, err := resolveMachineEnvironmentTx(ctx, qtx, input.OrgID, NilID, defaults.Environment); err != nil {
		return fmt.Errorf("machine pool default_machine fields %w", err)
	}
	return storeerr.InvalidRequest(s.machinePoolProviders.ValidatePool(
		input.Provider,
		MachinePoolProviderPolicy{
			DefaultProvisioning: defaults.Provisioning,
			ResourceLimits: MachineResourceLimits{
				MaxTotalCPU:        input.MaxTotalCPU,
				MaxTotalMemoryMB:   input.MaxTotalMemoryMB,
				MaxMachineCPU:      input.MaxMachineCPU,
				MaxMachineMemoryMB: input.MaxMachineMemoryMB,
			},
			ProviderConfig: input.ProviderConfig,
		},
	))

}

func resolveMachineEnvironment(
	base MachineEnvironment,
	overlays ...MachineEnvironmentOverlay,
) (MachineEnvironment, error) {
	if err := validateMachineEnvironment(base); err != nil {
		return MachineEnvironment{}, err
	}
	resolved := MachineEnvironment{Env: maps.Clone(base.Env), SecretEnv: maps.Clone(base.SecretEnv)}
	for _, overlay := range overlays {
		if err := validateMachineEnvironmentOverlay(overlay); err != nil {
			return MachineEnvironment{}, err
		}
		if overlay.Env != nil {
			resolved.Env = applyStringMapOverlay(resolved.Env, overlay.Env)
		}
		if overlay.SecretEnv != nil {
			resolved.SecretEnv = applyStringMapOverlay(resolved.SecretEnv, overlay.SecretEnv)
		}
	}
	if err := validateMachineEnvironment(resolved); err != nil {
		return MachineEnvironment{}, err
	}
	return resolved, nil
}

func resolveMachineEnvironmentTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
	base MachineEnvironment,
	overlays ...MachineEnvironmentOverlay,
) (MachineEnvironment, error) {
	environment, err := resolveMachineEnvironment(base, overlays...)
	if err != nil {
		return MachineEnvironment{}, err
	}
	if err := validateMachineEnvironmentSecretsTx(ctx, qtx, orgID, projectID, environment); err != nil {
		return MachineEnvironment{}, err
	}
	return environment, nil
}

func applyStringMapOverlay(base map[string]string, overlay map[string]*string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}
	for key, value := range overlay {
		name := strings.ToUpper(key)
		resolvedKey := key
		for baseKey := range base {
			if strings.ToUpper(baseKey) == name {
				resolvedKey = baseKey
				break
			}
		}
		if value == nil {
			delete(base, resolvedKey)
			continue
		}
		base[resolvedKey] = *value
	}
	return base
}

func validateMachineProvisioning(machineProvisioning MachineProvisioningConfig) error {
	if _, err := resourcesFromMachineProvisioning(machineProvisioning); err != nil {
		return err
	}
	if machineProvisioning.ProviderOptions == nil {
		return errors.New("requires provider_options")
	}
	return nil
}

func validateMachineEnvironment(environment MachineEnvironment) error {
	envNames, err := validateEnvNames("env", environment.Env)
	if err != nil {
		return err
	}
	for key, value := range environment.Env {
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("env.%s cannot contain NUL", key)
		}
	}
	if _, err := validateEnvNames("secret_env", environment.SecretEnv); err != nil {
		return err
	}
	for key, secretID := range environment.SecretEnv {
		if _, ok := envNames[strings.ToUpper(key)]; ok {
			return fmt.Errorf("env and secret_env cannot both set key %s", key)
		}
		if secretID == "" {
			return fmt.Errorf("secret_env.%s is required", key)
		}
		if _, err := publicid.Decode(publicid.KindSecret, secretID); err != nil {
			return fmt.Errorf("secret_env.%s: %w", key, err)
		}
	}
	return nil
}

func validateMachineEnvironmentOverlay(overlay MachineEnvironmentOverlay) error {
	if _, err := validateEnvNames("env", overlay.Env); err != nil {
		return err
	}
	if _, err := validateEnvNames("secret_env", overlay.SecretEnv); err != nil {
		return err
	}
	return nil
}

func validateEnvNames[T any](field string, values map[string]T) (map[string]struct{}, error) {
	names := make(map[string]struct{}, len(values))
	for key := range values {
		if err := validateEnvName(field, key); err != nil {
			return nil, err
		}
		name := strings.ToUpper(key)
		if _, ok := names[name]; ok {
			return nil, fmt.Errorf("%s cannot set key %s more than once with different casing", field, name)
		}
		names[name] = struct{}{}
	}
	return names, nil
}

func validateEnvName(field, key string) error {
	if key == "" {
		return fmt.Errorf("%s key is required", field)
	}
	if !envname.Valid(key) {
		return fmt.Errorf("%s key %q must match %s", field, key, envname.Pattern)
	}
	if strings.HasPrefix(strings.ToUpper(key), "OMNARA_") {
		return fmt.Errorf("%s cannot set reserved OMNARA_ key %s", field, key)
	}
	return nil
}

type MachineProvisioningColumns struct {
	CPU             *int32
	MemoryMB        *int32
	ProviderOptions json.RawMessage
}

func machineProvisioningToColumns(
	machineProvisioning MachineProvisioningConfig,
) (MachineProvisioningColumns, error) {
	if err := validateMachineProvisioning(machineProvisioning); err != nil {
		return MachineProvisioningColumns{}, err
	}
	if machineProvisioning.CPU != nil && *machineProvisioning.CPU > math.MaxInt32 {
		return MachineProvisioningColumns{}, fmt.Errorf("cpu must be between 1 and %d", math.MaxInt32)
	}
	if machineProvisioning.MemoryMB != nil && *machineProvisioning.MemoryMB > math.MaxInt32 {
		return MachineProvisioningColumns{}, fmt.Errorf("memory_mb must be between 1 and %d", math.MaxInt32)
	}
	providerOptions, err := marshalJSON(machineProvisioning.ProviderOptions)
	if err != nil {
		return MachineProvisioningColumns{}, err
	}
	var cpu *int32
	if machineProvisioning.CPU != nil {
		value := int32(*machineProvisioning.CPU)
		cpu = &value
	}
	var memoryMB *int32
	if machineProvisioning.MemoryMB != nil {
		value := int32(*machineProvisioning.MemoryMB)
		memoryMB = &value
	}
	return MachineProvisioningColumns{
		CPU:             cpu,
		MemoryMB:        memoryMB,
		ProviderOptions: providerOptions,
	}, nil
}

func machineEnvironmentToColumns(environment MachineEnvironment) (json.RawMessage, json.RawMessage, error) {
	if err := validateMachineEnvironment(environment); err != nil {
		return nil, nil, err
	}
	if environment.Env == nil {
		environment.Env = map[string]string{}
	}
	env, err := marshalJSON(environment.Env)
	if err != nil {
		return nil, nil, err
	}
	if environment.SecretEnv == nil {
		environment.SecretEnv = map[string]string{}
	}
	secretEnv, err := marshalJSON(environment.SecretEnv)
	if err != nil {
		return nil, nil, err
	}
	return env, secretEnv, nil
}

func MachineEnvironmentOverlayToColumns(
	overlay MachineEnvironmentOverlay,
) (json.RawMessage, json.RawMessage, error) {
	if err := validateMachineEnvironmentOverlay(overlay); err != nil {
		return nil, nil, err
	}
	if overlay.Env == nil {
		overlay.Env = map[string]*string{}
	}
	envOverlay, err := marshalJSON(overlay.Env)
	if err != nil {
		return nil, nil, err
	}
	if overlay.SecretEnv == nil {
		overlay.SecretEnv = map[string]*string{}
	}
	secretEnvOverlay, err := marshalJSON(overlay.SecretEnv)
	if err != nil {
		return nil, nil, err
	}
	return envOverlay, secretEnvOverlay, nil
}

func MachineProvisioningFromDefaults(
	cpu, memoryMB *int,
	providerOptions json.RawMessage,
) (MachineProvisioningConfig, error) {
	return machineProvisioningFromColumns(cpu, memoryMB, providerOptions)
}

func MachineProvisioningFromRecord(machine MachineRecord) (MachineProvisioningConfig, error) {
	return machineProvisioningFromColumns(
		machine.CPU,
		machine.MemoryMB,
		machine.ProviderOptions,
	)
}

func machineProvisioningFromColumns(
	cpu, memoryMB *int,
	providerOptions json.RawMessage,
) (MachineProvisioningConfig, error) {
	machineProvisioning := MachineProvisioningConfig{CPU: cpu, MemoryMB: memoryMB}
	if err := decodeMachineJSONField(
		providerOptions,
		"provider_options",
		&machineProvisioning.ProviderOptions,
	); err != nil {
		return MachineProvisioningConfig{}, err
	}
	if err := validateMachineProvisioning(machineProvisioning); err != nil {
		return MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

func effectiveMachineEnvironment(
	machineEnv, machineSecretEnv json.RawMessage,
	bindingOverlay MachineEnvironmentOverlay,
) (MachineEnvironment, error) {
	environment, err := MachineEnvironmentFromColumns(machineEnv, machineSecretEnv)
	if err != nil {
		return MachineEnvironment{}, err
	}
	return resolveMachineEnvironment(environment, bindingOverlay)
}

func MachineEnvironmentFromColumns(env, secretEnv json.RawMessage) (MachineEnvironment, error) {
	var environment MachineEnvironment
	if err := decodeMachineJSONField(env, "env", &environment.Env); err != nil {
		return MachineEnvironment{}, err
	}
	if err := decodeMachineJSONField(secretEnv, "secret_env", &environment.SecretEnv); err != nil {
		return MachineEnvironment{}, err
	}
	if err := validateMachineEnvironment(environment); err != nil {
		return MachineEnvironment{}, err
	}
	return environment, nil
}

func decodeMachineJSONField(raw json.RawMessage, fieldName string, dest any) error {
	raw = normalizedJSON(raw)
	if err := decodeStrictObject(raw, dest); err != nil {
		return fmt.Errorf("%s: %w", fieldName, err)
	}
	return nil
}

func machineProvisioningOverlayFromColumns(
	cpu, memoryMB *int,
	providerOptionsOverlay json.RawMessage,
) (MachineProvisioningOverlay, error) {
	overlay := MachineProvisioningOverlay{CPU: cpu, MemoryMB: memoryMB}
	providerOptionsOverlay = normalizedJSON(providerOptionsOverlay)
	if err := json.Unmarshal(providerOptionsOverlay, &overlay.ProviderOptions); err != nil {
		return MachineProvisioningOverlay{}, err
	}
	if len(overlay.ProviderOptions) == 0 {
		overlay.ProviderOptions = nil
	}
	return overlay, nil
}

func machineEnvironmentOverlayFromColumns(
	envOverlay, secretEnvOverlay json.RawMessage,
) (MachineEnvironmentOverlay, error) {
	var overlay MachineEnvironmentOverlay
	envOverlay = normalizedJSON(envOverlay)
	if err := json.Unmarshal(envOverlay, &overlay.Env); err != nil {
		return MachineEnvironmentOverlay{}, err
	}
	if len(overlay.Env) == 0 {
		overlay.Env = nil
	}
	secretEnvOverlay = normalizedJSON(secretEnvOverlay)
	if err := json.Unmarshal(secretEnvOverlay, &overlay.SecretEnv); err != nil {
		return MachineEnvironmentOverlay{}, err
	}
	if len(overlay.SecretEnv) == 0 {
		overlay.SecretEnv = nil
	}
	return overlay, nil
}

func runtimeMachineProvisioningOverlay(machine agentconfig.RuntimeMachine) MachineProvisioningOverlay {
	return MachineProvisioningOverlay{
		CPU:             machine.MachineCPU,
		MemoryMB:        machine.MachineMemoryMB,
		ProviderOptions: machine.MachineProviderOptionsOverlay,
	}
}

func runtimeMachineEnvironmentOverlay(machine agentconfig.RuntimeMachine) MachineEnvironmentOverlay {
	return MachineEnvironmentOverlay{Env: machine.EnvOverlay, SecretEnv: machine.SecretEnvOverlay}
}

func hasMachineProvisioningOverlay(machine agentconfig.RuntimeMachine) bool {
	return machine.MachineCPU != nil ||
		machine.MachineMemoryMB != nil ||
		machine.MachineProviderOptionsOverlay != nil
}

func validateMachineEnvironmentSecretsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
	environment MachineEnvironment,
) error {
	validatedSecretIDs := make(map[ID]struct{}, len(environment.SecretEnv))
	for envName, secretRef := range environment.SecretEnv {
		secretID, err := publicid.Decode(publicid.KindSecret, secretRef)
		if err != nil {
			return fmt.Errorf("secret_env.%s: %w", envName, err)
		}
		if _, ok := validatedSecretIDs[secretID]; ok {
			continue
		}
		var secret secretops.Facts
		if isNilID(projectID) {
			record, err := qtx.GetSecret(ctx, dbsqlc.GetSecretParams{OrgID: orgID, ID: secretID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("secret_env.%s secret is not found or not org-owned: %w", envName, storeerr.ErrNotFound)
				}
				return fmt.Errorf("secret_env.%s: %w", envName, err)
			}
			secret = secretops.FactsFromGet(record)
			if secret.OwnerKind != secretstore.SecretOwnerOrg {
				return fmt.Errorf("secret_env.%s secret is not found or not org-owned: %w", envName, storeerr.ErrNotFound)
			}
		} else {
			record, err := qtx.GetProjectAvailableSecret(
				ctx,
				dbsqlc.GetProjectAvailableSecretParams{
					OrgID:     orgID,
					ProjectID: projectID,
					SecretID:  secretID,
				},
			)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("secret_env.%s secret is not available to the project: %w", envName, storeerr.ErrNotFound)
				}
				return fmt.Errorf("secret_env.%s: %w", envName, err)
			}
			secret = secretops.FactsFromProjectAvailable(record)
		}
		if secret.ManagementKind != management.Tenant {
			return fmt.Errorf(
				"secret_env.%s secret is not available for tenant-managed resources: %w",
				envName,
				storeerr.ErrNotFound,
			)
		}
		if secret.Kind != secretstore.SecretKindGeneric {
			return fmt.Errorf(
				"secret_env.%s secret kind %q does not match expected kind %q: %w",
				envName,
				secret.Kind,
				secretstore.SecretKindGeneric,
				storeerr.ErrInvalidSecretRequest,
			)
		}
		validatedSecretIDs[secretID] = struct{}{}
	}
	return nil
}

func environmentByteSize(env map[string]string) int {
	size := 0
	for name, value := range env {
		size += len(name) + len(value)
	}
	return size
}

func (s *Store) ResolveEnvironmentSecrets(
	ctx context.Context,
	orgID, projectID ID,
	envJSON, secretEnvJSON json.RawMessage,
) (map[string]string, error) {
	environment, err := MachineEnvironmentFromColumns(envJSON, secretEnvJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", storeerr.ErrPermanentEnvironment, err)
	}
	return s.resolveEnvironmentSecrets(ctx, orgID, projectID, environment)
}

func (s *Store) resolveEnvironmentSecrets(
	ctx context.Context,
	orgID, projectID ID,
	environment MachineEnvironment,
) (map[string]string, error) {
	env := maps.Clone(environment.Env)
	secretEnv := environment.SecretEnv
	if env == nil {
		env = map[string]string{}
	}
	resolvedBytes := environmentByteSize(env)
	if resolvedBytes > MaxResolvedEnvironmentBytes {
		return nil, fmt.Errorf("%w: resolved environment exceeds size limit", storeerr.ErrPermanentEnvironment)
	}
	resolvedSecrets := make(map[ID]string, len(secretEnv))
	for envName, secretRef := range secretEnv {
		secretID, err := publicid.Decode(publicid.KindSecret, secretRef)
		if err != nil {
			return nil, fmt.Errorf("%w: secret_env.%s: %w", storeerr.ErrPermanentEnvironment, envName, err)
		}
		value, ok := resolvedSecrets[secretID]
		if !ok {
			payload, err := s.readEnvironmentSecretPayload(ctx, orgID, projectID, secretID)
			if err != nil {
				if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrInvalidSecretRequest) {
					return nil, fmt.Errorf("%w: secret_env.%s: %w", storeerr.ErrPermanentEnvironment, envName, err)
				}
				return nil, fmt.Errorf("secret_env.%s: %w", envName, err)
			}
			value = payload.Payload[secrets.KeyValue]
			if strings.ContainsRune(value, 0) {
				return nil, fmt.Errorf(
					"%w: secret_env.%s value cannot contain NUL",
					storeerr.ErrPermanentEnvironment,
					envName,
				)
			}
			resolvedSecrets[secretID] = value
		}
		resolvedBytes += len(envName) + len(value)
		if resolvedBytes > MaxResolvedEnvironmentBytes {
			return nil, fmt.Errorf("%w: resolved environment exceeds size limit", storeerr.ErrPermanentEnvironment)
		}
		env[envName] = value
	}
	return env, nil
}

func (s *Store) readEnvironmentSecretPayload(
	ctx context.Context,
	orgID, projectID, secretID ID,
) (secretstore.SecretPayloadRecord, error) {
	if isNilID(projectID) {
		return s.secrets.ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
			OrgID:    orgID,
			SecretID: secretID,
			Kind:     secretstore.SecretKindGeneric,
		})
	}
	return s.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID:     orgID,
		ProjectID: projectID,
		SecretID:  secretID,
		Kind:      secretstore.SecretKindGeneric,
	})
}

func (s *Store) ResolvePoolMachineProvisioningEnv(
	ctx context.Context,
	claim PoolMachineProvisioningClaim,
) (map[string]string, error) {
	environment, err := effectiveMachineEnvironment(
		claim.Machine.Env,
		claim.Machine.SecretEnv,
		claim.BindingEnvironmentOverlay,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", storeerr.ErrPermanentEnvironment, err)
	}
	return s.resolveEnvironmentSecrets(ctx, claim.Machine.OrgID, claim.GrantProjectID, environment)
}

func resourcesFromMachineProvisioning(
	machineProvisioning MachineProvisioningConfig,
) (MachinePoolResources, error) {
	if machineProvisioning.CPU != nil && *machineProvisioning.CPU <= 0 {
		return MachinePoolResources{}, errors.New("cpu must be a positive integer")
	}
	if machineProvisioning.MemoryMB != nil && *machineProvisioning.MemoryMB <= 0 {
		return MachinePoolResources{}, errors.New("memory_mb must be a positive integer")
	}
	resources := MachinePoolResources{}
	if machineProvisioning.CPU != nil {
		resources.CPU = int64(*machineProvisioning.CPU)
	}
	if machineProvisioning.MemoryMB != nil {
		resources.MemoryMB = int64(*machineProvisioning.MemoryMB)
	}
	return resources, nil
}

func decodeStrictObject(raw json.RawMessage, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func resolveMachineCwd(poolCwd, grantCwd string) string {
	if grantCwd != "" {
		return grantCwd
	}
	return poolCwd
}

func resolveProcessCwd(machineCwd, bindingCwd, requestedCwd string) string {
	base := machineCwd
	if bindingCwd != "" {
		base = bindingCwd
	}
	if requestedCwd == "" {
		return base
	}
	if path.IsAbs(requestedCwd) || base == "" {
		return path.Clean(requestedCwd)
	}
	return path.Join(base, requestedCwd)
}
