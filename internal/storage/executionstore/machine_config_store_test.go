package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestResolveMachineProvisioningAppliesOverlays(t *testing.T) {
	resolved, err := resolveTestMachineProvisioning(
		testMachineProvisioning(t, 1, 1024, map[string]any{
			"image": "base", "metro": "sfo", "pool_only": true,
		}),
		testMachineProvisioningOverlay(
			t,
			nil,
			ptrForMachineTest(2048),
			map[string]any{"metro": "dfw", "grant_only": true},
		),
		testMachineProvisioningOverlay(
			t,
			ptrForMachineTest(2),
			nil,
			map[string]any{"metro": "ord", "source_only": true},
		),
	)
	if err != nil {
		t.Fatalf("resolve machine provisioning: %v", err)
	}
	requireMachineProvisioningForTest(t, resolved, testMachineProvisioning(t, 2, 2048, map[string]any{
		"image": "base", "metro": "ord", "pool_only": true, "grant_only": true, "source_only": true,
	}))
}

func TestResolveMachineProvisioningDelegatesProviderOptions(t *testing.T) {
	providers := &captureMachinePoolProviders{
		options: map[string]json.RawMessage{
			"image": json.RawMessage(`"resolved"`),
			"metro": json.RawMessage(`"iad"`),
		},
	}
	store := &Store{machinePoolProviders: providers}
	poolDefault := testMachineProvisioning(t, 1, 1024, map[string]any{"image": "base", "metro": "sfo"})
	resolved, err := store.ResolveMachineProvisioning(
		"test",
		MachinePoolProviderPolicy{DefaultProvisioning: poolDefault},
		testMachineProvisioningOverlay(t, nil, nil, map[string]any{"image": "project"}),
		testMachineProvisioningOverlay(t, nil, nil, map[string]any{"image": "agent", "metro": "iad"}),
	)
	if err != nil {
		t.Fatalf("resolve machine provisioning: %v", err)
	}
	if providers.provider != "test" ||
		string(providers.defaultOptions["image"]) != `"base"` ||
		string(providers.defaultOptions["metro"]) != `"sfo"` ||
		string(providers.projectOptions["image"]) != `"project"` ||
		string(providers.agentOptions["image"]) != `"agent"` ||
		string(providers.agentOptions["metro"]) != `"iad"` {
		t.Fatalf(
			"unexpected provider input: provider=%q default=%+v project=%+v agent=%+v",
			providers.provider,
			providers.defaultOptions,
			providers.projectOptions,
			providers.agentOptions,
		)
	}
	if string(resolved.ProviderOptions["image"]) != `"resolved"` ||
		string(resolved.ProviderOptions["metro"]) != `"iad"` {
		t.Fatalf("resolved provider options = %+v", resolved.ProviderOptions)
	}
}

func TestResolveMachineProvisioningKeepsPoolDefaultSeparate(t *testing.T) {
	providers := &captureMachinePoolProviders{
		options: map[string]json.RawMessage{"image": json.RawMessage(`"resolved"`)},
	}
	store := &Store{machinePoolProviders: providers}
	poolDefault := testMachineProvisioning(t, 1, 1024, map[string]any{"image": "base"})
	if _, err := store.ResolveMachineProvisioning(
		"test",
		MachinePoolProviderPolicy{DefaultProvisioning: poolDefault},
		testMachineProvisioningOverlay(t, nil, ptrForMachineTest(2048), nil),
		testMachineProvisioningOverlay(t, ptrForMachineTest(2), nil, nil),
	); err != nil {
		t.Fatalf("resolve machine provisioning: %v", err)
	}
	requireMachineProvisioningForTest(t, providers.poolDefault, poolDefault)
	if providers.machineProvisioning.CPU == nil || *providers.machineProvisioning.CPU != 2 ||
		providers.machineProvisioning.MemoryMB == nil || *providers.machineProvisioning.MemoryMB != 2048 {
		t.Fatalf("provider machine provisioning = %+v", providers.machineProvisioning)
	}
}

func TestResolveMachineEnvironmentNullInheritsAndKeyNullClears(t *testing.T) {
	resolved, err := resolveMachineEnvironment(
		MachineEnvironment{Env: map[string]string{"A": "pool", "B": "pool"}},
		MachineEnvironmentOverlay{Env: map[string]*string{"A": nil, "C": ptrForMachineTest("source")}},
	)
	if err != nil {
		t.Fatalf("resolve machine environment: %v", err)
	}
	requireMachineEnvironmentForTest(
		t,
		resolved,
		MachineEnvironment{Env: map[string]string{"B": "pool", "C": "source"}},
	)
}

func TestResolveMachineEnvironmentAppliesOverlaysInOrder(t *testing.T) {
	base := MachineEnvironment{Env: map[string]string{"A": "pool", "B": "pool"}}
	resolved, err := resolveMachineEnvironment(
		base,
		MachineEnvironmentOverlay{Env: map[string]*string{
			"A": ptrForMachineTest("grant"),
			"C": ptrForMachineTest("grant"),
		}},
		MachineEnvironmentOverlay{Env: map[string]*string{
			"A": nil,
			"D": ptrForMachineTest("source"),
		}},
	)
	if err != nil {
		t.Fatalf("resolve machine environment: %v", err)
	}
	requireMachineEnvironmentForTest(
		t,
		resolved,
		MachineEnvironment{Env: map[string]string{"B": "pool", "C": "grant", "D": "source"}},
	)
	requireMachineEnvironmentForTest(
		t,
		base,
		MachineEnvironment{Env: map[string]string{"A": "pool", "B": "pool"}},
	)
}

func TestResolveMachineEnvironmentAppliesOverlaysCaseInsensitively(t *testing.T) {
	resolved, err := resolveMachineEnvironment(
		MachineEnvironment{Env: map[string]string{"App_Mode": "pool", "Delete_Me": "pool"}},
		MachineEnvironmentOverlay{Env: map[string]*string{"app_mode": ptrForMachineTest("grant")}},
		MachineEnvironmentOverlay{Env: map[string]*string{"DELETE_ME": nil}},
	)
	if err != nil {
		t.Fatalf("resolve machine environment: %v", err)
	}
	requireMachineEnvironmentForTest(
		t,
		resolved,
		MachineEnvironment{Env: map[string]string{"App_Mode": "grant"}},
	)
}

func TestResolveMachineEnvironmentRejectsNULValue(t *testing.T) {
	if _, err := resolveMachineEnvironment(
		MachineEnvironment{},
		MachineEnvironmentOverlay{Env: map[string]*string{"VALUE": ptrForMachineTest("invalid\x00value")}},
	); err == nil || err.Error() != "env.VALUE cannot contain NUL" {
		t.Fatalf("resolve machine environment error = %v", err)
	}
}

func TestResolveEnvironmentSecretsRejectsOversizedEnvironment(t *testing.T) {
	store := &Store{}
	env, err := json.Marshal(map[string]string{"VALUE": strings.Repeat("x", MaxResolvedEnvironmentBytes)})
	if err != nil {
		t.Fatalf("marshal oversized environment: %v", err)
	}
	if _, err := store.ResolveEnvironmentSecrets(
		context.Background(),
		NilID,
		NilID,
		env,
		json.RawMessage(`{}`),
	); err == nil || !errors.Is(err, storeerr.ErrPermanentEnvironment) ||
		!strings.Contains(err.Error(), "resolved environment exceeds size limit") {
		t.Fatalf("resolve oversized environment error = %v", err)
	}
}

func TestResolveMachineEnvironmentRejectsEnvSecretEnvConflict(t *testing.T) {
	secretID := secretPublicIDForUnitTest(t, "conflict")
	if _, err := resolveMachineEnvironment(
		MachineEnvironment{Env: map[string]string{"API_TOKEN": "plain"}},
		MachineEnvironmentOverlay{SecretEnv: map[string]*string{"API_TOKEN": &secretID}},
	); err == nil || err.Error() != "env and secret_env cannot both set key API_TOKEN" {
		t.Fatalf("env/secret_env conflict error = %v", err)
	}
}

func TestResolveMachineEnvironmentRejectsSecretEnvEnvConflict(t *testing.T) {
	secretID := secretPublicIDForUnitTest(t, "reverse-conflict")
	if _, err := resolveMachineEnvironment(
		MachineEnvironment{SecretEnv: map[string]string{"API_TOKEN": secretID}},
		MachineEnvironmentOverlay{Env: map[string]*string{"API_TOKEN": ptrForMachineTest("plain")}},
	); err == nil || err.Error() != "env and secret_env cannot both set key API_TOKEN" {
		t.Fatalf("secret_env/env conflict error = %v", err)
	}
}

func TestResolveMachineEnvironmentRejectsCaseInsensitiveConflicts(t *testing.T) {
	secretID := secretPublicIDForUnitTest(t, "case-conflict")
	if _, err := resolveMachineEnvironment(MachineEnvironment{
		Env:       map[string]string{"Api_Token": "plain"},
		SecretEnv: map[string]string{"API_TOKEN": secretID},
	}); err == nil || err.Error() != "env and secret_env cannot both set key API_TOKEN" {
		t.Fatalf("case-insensitive env/secret_env conflict error = %v", err)
	}
}

func TestResolveMachineEnvironmentRejectsCaseInsensitiveDuplicates(t *testing.T) {
	if _, err := resolveMachineEnvironment(MachineEnvironment{
		Env: map[string]string{"App_Mode": "one", "APP_MODE": "two"},
	}); err == nil || err.Error() != "env cannot set key APP_MODE more than once with different casing" {
		t.Fatalf("case-insensitive duplicate error = %v", err)
	}
	if _, err := resolveMachineEnvironment(
		MachineEnvironment{},
		MachineEnvironmentOverlay{Env: map[string]*string{
			"App_Mode": ptrForMachineTest("one"),
			"APP_MODE": ptrForMachineTest("two"),
		}},
	); err == nil || err.Error() != "env cannot set key APP_MODE more than once with different casing" {
		t.Fatalf("case-insensitive overlay duplicate error = %v", err)
	}
}

func TestResolveMachineEnvironmentValidatesEnvNames(t *testing.T) {
	secretID := secretPublicIDForUnitTest(t, "env-name")
	tests := map[string]struct {
		base     MachineEnvironment
		overlays []MachineEnvironmentOverlay
	}{
		"env_empty":  {base: MachineEnvironment{Env: map[string]string{"": "value"}}},
		"env_equals": {base: MachineEnvironment{Env: map[string]string{"BAD=KEY": "value"}}},
		"env_nul":    {base: MachineEnvironment{Env: map[string]string{"BAD\x00KEY": "value"}}},
		"secret_equals": {
			base: MachineEnvironment{SecretEnv: map[string]string{"BAD=KEY": secretID}},
		},
		"overlay_equals": {
			overlays: []MachineEnvironmentOverlay{{
				Env: map[string]*string{"BAD=KEY": ptrForMachineTest("value")},
			}},
		},
		"overlay_secret": {
			overlays: []MachineEnvironmentOverlay{{SecretEnv: map[string]*string{"BAD=KEY": &secretID}}},
		},
		"reserved_case": {
			base: MachineEnvironment{Env: map[string]string{"omnara_machine_token": "value"}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveMachineEnvironment(tc.base, tc.overlays...); err == nil {
				t.Fatal("expected invalid env name to fail")
			}
		})
	}

	if _, err := resolveMachineEnvironment(
		MachineEnvironment{
			Env:       map[string]string{"1.lower-name has space": "value"},
			SecretEnv: map[string]string{"lower.name": secretID},
		},
	); err != nil {
		t.Fatalf("resolve machine environment with raw process env names: %v", err)
	}
}

func TestResolveMachineEnvironmentNullDeleteCanMoveKeyBetweenMaps(t *testing.T) {
	secretID := secretPublicIDForUnitTest(t, "null-delete")
	resolved, err := resolveMachineEnvironment(
		MachineEnvironment{
			Env:       map[string]string{"API_TOKEN": "plain"},
			SecretEnv: map[string]string{"PASSWORD": secretID},
		},
		MachineEnvironmentOverlay{
			Env:       map[string]*string{"API_TOKEN": nil},
			SecretEnv: map[string]*string{"PASSWORD": nil},
		},
		MachineEnvironmentOverlay{
			Env:       map[string]*string{"PASSWORD": ptrForMachineTest("plain-password")},
			SecretEnv: map[string]*string{"API_TOKEN": &secretID},
		},
	)
	if err != nil {
		t.Fatalf("resolve machine environment: %v", err)
	}
	if resolved.Env["PASSWORD"] != "plain-password" {
		t.Fatalf("resolved env = %+v, want PASSWORD plain-password", resolved.Env)
	}
	if _, ok := resolved.Env["API_TOKEN"]; ok {
		t.Fatalf("resolved env should not include API_TOKEN: %+v", resolved.Env)
	}
	if resolved.SecretEnv["API_TOKEN"] != secretID {
		t.Fatalf("resolved secret_env = %+v, want API_TOKEN secret", resolved.SecretEnv)
	}
	if _, ok := resolved.SecretEnv["PASSWORD"]; ok {
		t.Fatalf("resolved secret_env should not include PASSWORD: %+v", resolved.SecretEnv)
	}
}

func TestResolveMachineEnvironmentAllowsEmptyOverlays(t *testing.T) {
	base := MachineEnvironment{}
	resolved, err := resolveMachineEnvironment(
		base,
		MachineEnvironmentOverlay{},
		MachineEnvironmentOverlay{},
	)
	if err != nil {
		t.Fatalf("resolve machine environment: %v", err)
	}
	requireMachineEnvironmentForTest(t, resolved, base)
}

func TestMachineEnvironmentOverlayFromColumnsRejectsMalformedEnv(t *testing.T) {
	if _, err := machineEnvironmentOverlayFromColumns(json.RawMessage(`["bad"]`), nil); err == nil {
		t.Fatal("expected malformed env to fail")
	}
}

func TestMachineProvisioningOverlayFromColumnsRejectsMalformedProviderOptions(t *testing.T) {
	if _, err := machineProvisioningOverlayFromColumns(nil, nil, json.RawMessage(`["bad"]`)); err == nil {
		t.Fatal("expected malformed provider_options to fail")
	}
}

func TestMachineProvisioningToColumnsRejectsInt32Overflow(t *testing.T) {
	tooLarge := math.MaxInt32 + 1
	for _, machineProvisioning := range []MachineProvisioningConfig{
		testMachineProvisioning(t, tooLarge, 1024, map[string]any{"image": "test"}),
		testMachineProvisioning(t, 1, tooLarge, map[string]any{"image": "test"}),
	} {
		if _, err := machineProvisioningToColumns(machineProvisioning); err == nil {
			t.Fatal("expected int32 overflow to fail")
		}
	}
}

func resolveTestMachineProvisioning(
	poolDefault MachineProvisioningConfig,
	projectOverlay, agentOverlay MachineProvisioningOverlay,
) (MachineProvisioningConfig, error) {
	store := &Store{machinePoolProviders: mergingMachinePoolProviders{}}
	return store.ResolveMachineProvisioning(
		"",
		MachinePoolProviderPolicy{DefaultProvisioning: poolDefault},
		projectOverlay,
		agentOverlay,
	)
}

type mergingMachinePoolProviders struct{}

func (mergingMachinePoolProviders) ResolveMachineProviderOptions(
	_ string,
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	var merged map[string]json.RawMessage
	for _, overlay := range []map[string]json.RawMessage{
		defaultOptions,
		projectOptions,
		agentOptions,
	} {
		if overlay != nil && merged == nil {
			merged = map[string]json.RawMessage{}
		}
		for key, value := range overlay {
			merged[key] = append(json.RawMessage(nil), value...)
		}
	}
	return merged, nil
}

func (mergingMachinePoolProviders) ValidatePool(
	_ string,
	_ MachinePoolProviderPolicy,
) error {
	return nil
}

func (providers mergingMachinePoolProviders) BuildMachineProvisioningIntent(
	provider string,
	policy MachinePoolProviderPolicy,
	machineProvisioning MachineProvisioningConfig,
) (MachineProvisioningConfig, error) {
	if err := providers.ValidatePool(provider, policy); err != nil {
		return MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

type captureMachinePoolProviders struct {
	provider            string
	defaultOptions      map[string]json.RawMessage
	projectOptions      map[string]json.RawMessage
	agentOptions        map[string]json.RawMessage
	options             map[string]json.RawMessage
	poolDefault         MachineProvisioningConfig
	machineProvisioning MachineProvisioningConfig
}

func (c *captureMachinePoolProviders) ResolveMachineProviderOptions(
	provider string,
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	c.provider = provider
	c.defaultOptions = defaultOptions
	c.projectOptions = projectOptions
	c.agentOptions = agentOptions
	return c.options, nil
}

func (c *captureMachinePoolProviders) ValidatePool(
	_ string,
	policy MachinePoolProviderPolicy,
) error {
	c.poolDefault = policy.DefaultProvisioning
	return nil
}

func (c *captureMachinePoolProviders) BuildMachineProvisioningIntent(
	provider string,
	policy MachinePoolProviderPolicy,
	machineProvisioning MachineProvisioningConfig,
) (MachineProvisioningConfig, error) {
	if err := c.ValidatePool(provider, policy); err != nil {
		return MachineProvisioningConfig{}, err
	}
	c.machineProvisioning = machineProvisioning
	return machineProvisioning, nil
}

func TestResolveMachineCwd(t *testing.T) {
	if got := resolveMachineCwd("/pool", "/project"); got != "/project" {
		t.Fatalf("resolved machine cwd = %q, want /project", got)
	}
	if got := resolveMachineCwd("/pool", ""); got != "/pool" {
		t.Fatalf("resolved machine cwd = %q, want /pool", got)
	}
}

func TestResolveProcessCwd(t *testing.T) {
	if got := resolveProcessCwd("/machine", "", ""); got != "/machine" {
		t.Fatalf("resolved machine cwd = %q, want /machine", got)
	}
	if got := resolveProcessCwd("/machine", "", "src"); got != "/machine/src" {
		t.Fatalf("resolved relative machine cwd = %q, want /machine/src", got)
	}
	if got := resolveProcessCwd("/machine", "/binding", ""); got != "/binding" {
		t.Fatalf("resolved binding cwd = %q, want /binding", got)
	}
	if got := resolveProcessCwd("/machine", "/binding", "src"); got != "/binding/src" {
		t.Fatalf("resolved relative process cwd = %q, want /binding/src", got)
	}
	if got := resolveProcessCwd("/machine", "/binding", "/requested"); got != "/requested" {
		t.Fatalf("resolved absolute process cwd = %q, want /requested", got)
	}
}

func TestCheckLaunchAggregateCap(t *testing.T) {
	if err := checkLaunchAggregateCap(2, 1, 3, nil); err != nil {
		t.Fatalf("uncapped resource failed: %v", err)
	}
	limit := 5
	if err := checkLaunchAggregateCap(2, 1, 3, &limit); err != nil {
		t.Fatalf("within cap failed: %v", err)
	}
	if err := checkLaunchAggregateCap(2, 0, 1, &limit); err == nil {
		t.Fatal("expected missing capped resource to fail")
	}
	if err := checkLaunchAggregateCap(2, 2, 3, &limit); err == nil {
		t.Fatal("expected over-cap resource to fail")
	}
	zero := 0
	if err := checkLaunchAggregateCap(0, 128, 1, &zero); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("zero total cap admission error = %v, want state transition conflict", err)
	}
}

func secretPublicIDForUnitTest(t *testing.T, seed string) string {
	t.Helper()
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("machine-config-secret:"+seed))
	value, err := publicid.Encode(publicid.KindSecret, id)
	if err != nil {
		t.Fatalf("encode secret public id: %v", err)
	}
	return value
}
