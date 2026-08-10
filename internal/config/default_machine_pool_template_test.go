package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultMachinePoolTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: hosted-pool
    description: Cluster pool
    provider: unikraft
    provider_auth_env_var: HOSTED_POOL_TOKEN
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: sfo
    default_cwd: /workspace
    max_total_machines: 5
    max_total_cpu: 5
    max_total_memory_mb: 5120
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
    metadata: {}
  - name: hosted-pool-large
    provider: unikraft
    provider_auth_env_var: HOSTED_POOL_LARGE_TOKEN
    default_machine_cpu: 2
    default_machine_memory_mb: 2048
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: iad
    max_total_machines: 2
    max_total_cpu: 4
    max_total_memory_mb: 4096
    max_machine_cpu: 2
    max_machine_memory_mb: 2048
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("HOSTED_POOL_TOKEN", "kraft-token")
	t.Setenv("HOSTED_POOL_LARGE_TOKEN", "kraft-large-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.DefaultMachinePools) != 2 || cfg.DefaultMachinePools[0].Name != "hosted-pool" ||
		cfg.DefaultMachinePools[0].Provider != "unikraft" ||
		cfg.DefaultMachinePools[0].ProviderAuthEnvVar != "HOSTED_POOL_TOKEN" ||
		cfg.DefaultMachinePools[1].Name != "hosted-pool-large" ||
		cfg.DefaultMachinePools[1].ProviderAuthEnvVar != "HOSTED_POOL_LARGE_TOKEN" {
		t.Fatalf("unexpected default pool templates: %+v", cfg.DefaultMachinePools)
	}
	if cfg.DefaultMachinePools[0].DefaultMachineCPU == nil ||
		*cfg.DefaultMachinePools[0].DefaultMachineCPU != 1 ||
		cfg.DefaultMachinePools[0].DefaultMachineMemoryMB == nil ||
		*cfg.DefaultMachinePools[0].DefaultMachineMemoryMB != 1024 {
		t.Fatalf("default machine resources = %+v", cfg.DefaultMachinePools[0])
	}
	wantProviderOptions := `{"image":"registry.example.com/agent:latest","metro":"sfo"}`
	if got := string(cfg.DefaultMachinePools[0].DefaultMachineProviderOptions); got != wantProviderOptions {
		t.Fatalf("default machine provider options = %s", got)
	}
	if string(cfg.DefaultMachinePools[0].ProviderConfig) != `{}` {
		t.Fatalf("provider config = %s", cfg.DefaultMachinePools[0].ProviderConfig)
	}
}

func TestLoadDefaultMachinePoolExample(t *testing.T) {
	path := filepath.Join("..", "..", "default-machine-pools-example.yaml")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("OMNARA_UNIKRAFT_DEFAULT_POOL_TOKEN", "kraft-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load example default pool template: %v", err)
	}
	if len(cfg.DefaultMachinePools) != 1 {
		t.Fatalf("default pool template count = %d, want 1", len(cfg.DefaultMachinePools))
	}
	if cfg.DefaultMachinePools[0].Name != "default-pool" {
		t.Fatalf("example default pool name = %q, want default-pool", cfg.DefaultMachinePools[0].Name)
	}
	if cfg.DefaultMachinePools[0].ProviderAuthEnvVar != "OMNARA_UNIKRAFT_DEFAULT_POOL_TOKEN" {
		t.Fatalf("example default pool provider_auth_env_var = %q", cfg.DefaultMachinePools[0].ProviderAuthEnvVar)
	}
	if string(cfg.DefaultMachinePools[0].ProviderConfig) != `{}` {
		t.Fatalf("provider config = %s", cfg.DefaultMachinePools[0].ProviderConfig)
	}
}

func TestLoadDefaultBlaxelMachinePoolTemplateWithoutCPU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: blaxel-pool
    provider: blaxel
    provider_auth_env_var: BLAXEL_POOL_TOKEN
    default_machine_memory_mb: 1024
    default_machine_provider_options:
      image: blaxel/base-image:latest
      region: us-pdx-1
    provider_config:
      workspace: omnara
    max_total_machines: 2
    max_total_memory_mb: 2048
    max_machine_memory_mb: 1024
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("BLAXEL_POOL_TOKEN", "blaxel-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load blaxel default pool template: %v", err)
	}
	loaded := cfg.DefaultMachinePools[0]
	if loaded.DefaultMachineCPU != nil || loaded.MaxTotalCPU != nil || loaded.MaxMachineCPU != nil {
		t.Fatalf(
			"blaxel default pool cpu fields = default %v total %v machine %v",
			loaded.DefaultMachineCPU,
			loaded.MaxTotalCPU,
			loaded.MaxMachineCPU,
		)
	}
}

func TestLoadDefaultMachinePoolTemplateAllowsZeroTotalCaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: zero-cap-pool
    provider: blaxel
    provider_auth_env_var: BLAXEL_POOL_TOKEN
    default_machine_memory_mb: 128
    default_machine_provider_options:
      image: blaxel/base-image:latest
      region: us-pdx-1
    provider_config:
      workspace: omnara
    max_total_machines: 0
    max_total_memory_mb: 0
    max_machine_memory_mb: 128
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("BLAXEL_POOL_TOKEN", "blaxel-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load zero-cap default pool template: %v", err)
	}
	if len(cfg.DefaultMachinePools) != 1 ||
		cfg.DefaultMachinePools[0].MaxTotalMachines != 0 ||
		cfg.DefaultMachinePools[0].MaxTotalMemoryMB == nil ||
		*cfg.DefaultMachinePools[0].MaxTotalMemoryMB != 0 {
		t.Fatalf("unexpected zero-cap default pool templates: %+v", cfg.DefaultMachinePools)
	}
}

func TestLoadDefaultMachinePoolTemplateRequiresMachineCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: missing-cap-pool
    provider: blaxel
    provider_auth_env_var: BLAXEL_POOL_TOKEN
    default_machine_memory_mb: 128
    default_machine_provider_options:
      image: blaxel/base-image:latest
      region: us-pdx-1
    provider_config:
      workspace: omnara
    max_total_memory_mb: 128
    max_machine_memory_mb: 128
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("BLAXEL_POOL_TOKEN", "blaxel-token")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "max_total_machines must be set") {
		t.Fatalf("error = %v, want omitted max_total_machines rejection", err)
	}
}

func TestLoadDefaultMachinePoolTemplateRequiresAuthEnvVar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: token-pool
    provider: unikraft
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: sfo
    max_total_machines: 1
    max_total_cpu: 1
    max_total_memory_mb: 1024
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("TEST_DEFAULT_POOL_TOKEN", "kraft-token")

	if _, err := Load(); err == nil {
		t.Fatal("expected missing default pool provider auth env var error")
	}
}

func TestLoadDefaultMachinePoolTemplateRequiresAuthEnvValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: token-pool
    provider: unikraft
    provider_auth_env_var: TEST_DEFAULT_POOL_TOKEN
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: sfo
    max_total_machines: 1
    max_total_cpu: 1
    max_total_memory_mb: 1024
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("TEST_DEFAULT_POOL_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected missing default pool provider auth env value error")
	}
}

func TestLoadDefaultMachinePoolTemplateRejectsNULDefaultCwd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: nul-cwd-pool
    provider: unikraft
    provider_auth_env_var: TEST_DEFAULT_POOL_TOKEN
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: sfo
    default_cwd: "/workspace\u0000bad"
    max_total_machines: 1
    max_total_cpu: 1
    max_total_memory_mb: 1024
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("TEST_DEFAULT_POOL_TOKEN", "kraft-token")

	if _, err := Load(); err == nil {
		t.Fatal("expected default_cwd NUL rejection")
	}
}

func TestLoadDefaultMachinePoolTemplateRequiresName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - provider: unikraft
    provider_auth_env_var: TEST_DEFAULT_POOL_TOKEN
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: sfo
    max_total_machines: 1
    max_total_cpu: 1
    max_total_memory_mb: 1024
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("TEST_DEFAULT_POOL_TOKEN", "kraft-token")

	if _, err := Load(); err == nil {
		t.Fatal("expected missing default pool name error")
	}
}

func TestLoadDefaultMachinePoolTemplateRejectsDuplicateNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: duplicate-pool
    provider: unikraft
    provider_auth_env_var: TEST_DEFAULT_POOL_TOKEN
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: sfo
    max_total_machines: 1
    max_total_cpu: 1
    max_total_memory_mb: 1024
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
  - name: duplicate-pool
    provider: unikraft
    provider_auth_env_var: TEST_DEFAULT_POOL_TOKEN
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_provider_options:
        image: registry.example.com/agent:latest
        metro: iad
    max_total_machines: 1
    max_total_cpu: 1
    max_total_memory_mb: 1024
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("TEST_DEFAULT_POOL_TOKEN", "kraft-token")

	if _, err := Load(); err == nil {
		t.Fatal("expected duplicate default pool name error")
	}
}

func TestLoadDefaultMachinePoolTemplateRejectsUnknownDefaultMachineField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default-pool.yaml")
	if err := os.WriteFile(path, []byte(`
pools:
  - name: invalid-machine-config-pool
    provider: unikraft
    provider_auth_env_var: TEST_DEFAULT_POOL_TOKEN
    default_machine_cpu: 1
    default_machine_memory_mb: 1024
    default_machine_image: registry.example.com/agent:latest
    default_machine_provider_options:
      metro: sfo
    max_total_machines: 1
    max_total_cpu: 1
    max_total_memory_mb: 1024
    max_machine_cpu: 1
    max_machine_memory_mb: 1024
`), 0o644); err != nil {
		t.Fatalf("write default pool template: %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES", path)
	t.Setenv("TEST_DEFAULT_POOL_TOKEN", "kraft-token")

	if _, err := Load(); err == nil {
		t.Fatal("expected unknown default_machine field to be rejected")
	}
}
