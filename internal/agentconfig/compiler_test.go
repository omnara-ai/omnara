package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestCompileYAMLCanonicalizesConfigRuntimeContract(t *testing.T) {
	sourceA := validAgentSource(`
tools:
  run_command:
    permission:
      mode: always_ask
      parameters: {}
`)
	sourceB := validAgentSource(`
tools:
  run_command:
    permission:
      mode: always_ask
      parameters: {}
`)
	first, err := Compile(SourceFormatYAML, []byte(sourceA), CompileOptions{})
	if err != nil {
		t.Fatalf("compile first: %v", err)
	}
	second, err := Compile(SourceFormatYAML, []byte(sourceB), CompileOptions{})
	if err != nil {
		t.Fatalf("compile second: %v", err)
	}
	if string(first.CanonicalJSON) != string(second.CanonicalJSON) {
		t.Fatalf("expected stable canonical JSON:\n%s\n%s", first.CanonicalJSON, second.CanonicalJSON)
	}
	if first.Hash == "" || first.Hash != second.Hash {
		t.Fatalf("expected stable hash, got %q and %q", first.Hash, second.Hash)
	}
	contract, err := RuntimeContractFromCompiled(json.RawMessage(first.CanonicalJSON), CompilerVersion, first.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if contract.Model.ConfiguredModelID != "" {
		t.Fatalf("unexpected model contract: %+v", contract.Model)
	}
	if len(contract.Tools) != 1 || contract.Tools[0].Name != "run_command" ||
		contract.Tools[0].Permission.Mode != toolpermission.ModeAlwaysAsk {
		t.Fatalf("unexpected tool contract: %+v", contract.Tools)
	}
	assertRunCommandInputSchema(t, contract.Tools[0].InputSchema)
}

func TestDefaultCatalogRunCommandSchemaMatchesModelFacingContract(t *testing.T) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("default tool catalog: %v", err)
	}
	for _, entry := range catalog.Entries() {
		if entry.DefaultPermission.Mode != toolpermission.ModeAlwaysAllow {
			t.Fatalf(
				"%s default permission = %s, want %s",
				entry.Name,
				entry.DefaultPermission.Mode,
				toolpermission.ModeAlwaysAllow,
			)
		}
	}
	entry, ok := catalog.Lookup("run_command")
	if !ok {
		t.Fatal("run_command catalog entry missing")
	}
	assertRunCommandInputSchema(t, entry.InputSchema)
	for _, name := range []string{
		"write_process", "read_process", "stop_process", "list_processes",
	} {
		entry, ok := catalog.Lookup(name)
		if !ok {
			t.Fatalf("%s catalog entry missing", name)
		}
		if name == "read_process" {
			assertReadProcessInputSchema(t, entry.InputSchema)
		}
	}
	if _, ok := catalog.Lookup("definitely_unknown_tool"); ok {
		t.Fatal("unknown tool must not be a model-facing built-in tool")
	}
}

func TestIntegrationSendPermissionIsAlwaysAllowOnly(t *testing.T) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("default tool catalog: %v", err)
	}
	entry, ok := catalog.Lookup("send_integration_message")
	if !ok {
		t.Fatal("send_integration_message catalog entry missing")
	}
	if len(entry.PermissionModes) != 1 ||
		entry.PermissionModes[0].Name != toolpermission.ModeAlwaysAllow {
		t.Fatalf("send_integration_message permission modes = %+v", entry.PermissionModes)
	}
	_, err = Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  send_integration_message:
    permission:
      mode: always_ask
`)), CompileOptions{})
	if err == nil {
		t.Fatal("expected always_ask send_integration_message permission to be rejected")
	}
}

func TestCompileYAMLToolPermissionOverrideBeatsCatalogDefault(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command:
    permission:
      mode: always_allow
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile permission override: %v", err)
	}
	tool := compiled.Compiled.Tools["run_command"]
	if tool.Permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("run_command permission = %+v, want %s", tool.Permission, toolpermission.ModeAlwaysAllow)
	}
	if string(tool.Permission.Parameters) != "{}" {
		t.Fatalf("run_command permission parameters = %s, want {}", tool.Permission.Parameters)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.Tools) != 1 || contract.Tools[0].Permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("runtime tool permission = %+v, want %s", contract.Tools, toolpermission.ModeAlwaysAllow)
	}
}

func TestCompileYAMLIncludesMCPServers(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
mcp:
  docs:
    url: https://example.com/mcp
    tools:
      search:
        permission:
          mode: always_allow
          parameters: {}
      disabled_tool:
        enabled: false
      aws___call_aws:
        permission:
          mode: always_deny
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile mcp config: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.MCPServers) != 1 {
		t.Fatalf("expected one mcp server, got %+v", contract.MCPServers)
	}
	server := contract.MCPServers[0]
	if server.ServerKey != "docs" || server.URL != "https://example.com/mcp" {
		t.Fatalf("unexpected mcp server: %+v", server)
	}
	if permission, ok := server.ResolveTool("search"); !ok ||
		permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("search resolution = permission=%+v ok=%t", permission, ok)
	}
	if permission, ok := server.ResolveTool("anything_else"); !ok ||
		permission.Mode != toolpermission.ModeAlwaysAsk {
		t.Fatalf("default resolution = permission=%+v ok=%t", permission, ok)
	}
	if _, ok := server.ResolveTool("disabled_tool"); ok {
		t.Fatalf("disabled_tool should not resolve enabled")
	}
	if permission, ok := server.ResolveTool("aws___call_aws"); !ok ||
		permission.Mode != toolpermission.ModeAlwaysDeny {
		t.Fatalf("AWS tool resolution = permission=%+v ok=%t", permission, ok)
	}
}

func TestCompileYAMLMCPDefaultEnabledFalseDisablesUnlistedTools(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
mcp:
  docs:
    url: https://example.com/mcp
    default_enabled: false
    permission:
      mode: always_allow
      parameters: {}
    tools:
      search:
        enabled: true
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile mcp config: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.MCPServers) != 1 {
		t.Fatalf("expected one mcp server, got %+v", contract.MCPServers)
	}
	server := contract.MCPServers[0]
	if permission, ok := server.ResolveTool("search"); !ok ||
		permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("search resolution = permission=%+v ok=%t", permission, ok)
	}
	if _, ok := server.ResolveTool("anything_else"); ok {
		t.Fatal("unlisted tool should be disabled when default_enabled is false")
	}
}

func TestCompileYAMLRejectsInvalidMCPConfig(t *testing.T) {
	for name, source := range map[string]string{
		"server_key_underscore": `
mcp:
  bad_key:
    url: https://example.com/mcp
`,
		"server_key_too_long": `
mcp:
  abcdefghijklmnopqrstuvwxyzabcdefg:
    url: https://example.com/mcp
`,
		"runtime_tool_name_too_long": `
mcp:
  ` + strings.Repeat("a", 32) + `:
    url: https://example.com/mcp
    tools:
      ` + strings.Repeat("b", 32) + `:
        enabled: true
        permission:
          mode: always_ask
          parameters: {}
`,
		"http_url": `
mcp:
  docs:
    url: http://example.com/mcp
`,
		"private_ip_url": `
mcp:
  docs:
    url: https://127.0.0.1/mcp
`,
		"wildcard_tool_name": `
mcp:
  docs:
    url: https://example.com/mcp
    tools:
      "*":
        enabled: true
        permission:
          mode: always_ask
          parameters: {}
`,
		"invalid_default_enabled": `
mcp:
  docs:
    url: https://example.com/mcp
    default_enabled: maybe
`,
		"invalid_server_permission": `
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: maybe
      parameters: {}
`,
		"invalid_tool_permission": `
mcp:
  docs:
    url: https://example.com/mcp
    tools:
      search:
        enabled: true
        permission:
          mode: maybe
          parameters: {}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(source)), CompileOptions{}); err == nil {
				t.Fatal("expected invalid mcp config to be rejected")
			}
		})
	}
}

func TestCompileYAMLAllowsMCPURLUserinfo(t *testing.T) {
	source := validAgentSource(`
mcp:
  docs:
    url: https://user:pass@example.com/mcp
`)
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
	if err != nil {
		t.Fatalf("compile mcp config with userinfo URL: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.MCPServers) != 1 || contract.MCPServers[0].URL != "https://user:pass@example.com/mcp" {
		t.Fatalf("unexpected mcp servers: %+v", contract.MCPServers)
	}
}

func TestCompileYAMLAllowsMCPAuthSecretReferences(t *testing.T) {
	secretID, err := publicid.Encode(publicid.KindSecret, uuid.MustParse("018f1111-1111-7111-8111-111111111111"))
	if err != nil {
		t.Fatalf("encode secret id: %v", err)
	}
	source := validAgentSource(`
mcp:
  github:
    url: https://api.githubcopilot.com/mcp
    auth:
      type: bearer
      secret_id: ` + secretID + `
`)
	var validatedSecretID string
	var validatedKind secrets.Kind
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{
		ValidateSecretID: func(secretID string, expectedKind secrets.Kind) error {
			validatedSecretID = secretID
			validatedKind = expectedKind
			return nil
		},
	})
	if err != nil {
		t.Fatalf("compile mcp auth config: %v", err)
	}
	if validatedSecretID != secretID || validatedKind != "generic" {
		t.Fatalf("secret validation = %q/%q, want %q/generic", validatedSecretID, validatedKind, secretID)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.MCPServers) != 1 || contract.MCPServers[0].Auth == nil ||
		contract.MCPServers[0].Auth.Type != MCPAuthTypeBearer ||
		contract.MCPServers[0].Auth.SecretID != secretID {
		t.Fatalf("unexpected mcp server auth: %+v", contract.MCPServers)
	}
}

func TestCompileYAMLValidatesMCPAuthSecretKind(t *testing.T) {
	secretID, err := publicid.Encode(publicid.KindSecret, uuid.MustParse("018f1111-1111-7111-8111-111111111112"))
	if err != nil {
		t.Fatalf("encode secret id: %v", err)
	}
	source := validAgentSource(`
mcp:
  github:
    url: https://api.githubcopilot.com/mcp
    auth:
      type: oauth
      secret_id: ` + secretID + `
`)
	var validatedKind secrets.Kind
	_, err = Compile(SourceFormatYAML, []byte(source), CompileOptions{
		ValidateSecretID: func(_ string, expectedKind secrets.Kind) error {
			validatedKind = expectedKind
			return nil
		},
	})
	if err != nil {
		t.Fatalf("compile mcp oauth config: %v", err)
	}
	if validatedKind != "oauth_token_set" {
		t.Fatalf("validated kind = %q, want oauth_token_set", validatedKind)
	}
}

func TestCompileYAMLAllowsSigV4MCPAuth(t *testing.T) {
	secretID, err := publicid.Encode(publicid.KindSecret, uuid.MustParse("018f1111-1111-7111-8111-111111111113"))
	if err != nil {
		t.Fatalf("encode secret id: %v", err)
	}
	source := validAgentSource(`
mcp:
  aws:
    url: https://mcp.example.com:443/aws?tenant=one
    auth:
      type: sigv4
      secret_id: ` + secretID + `
      service: execute-api
      region: us-west-2
`)
	var validatedKind secrets.Kind
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{
		ValidateSecretID: func(_ string, expectedKind secrets.Kind) error {
			validatedKind = expectedKind
			return nil
		},
	})
	if err != nil {
		t.Fatalf("compile SigV4 MCP config: %v", err)
	}
	if validatedKind != secrets.KindAWSCredentials {
		t.Fatalf("validated kind = %q, want %q", validatedKind, secrets.KindAWSCredentials)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.MCPServers) != 1 || contract.MCPServers[0].Auth == nil ||
		contract.MCPServers[0].URL != "https://mcp.example.com:443/aws?tenant=one" ||
		contract.MCPServers[0].Auth.Type != MCPAuthTypeSigV4 ||
		contract.MCPServers[0].Auth.Service != "execute-api" ||
		contract.MCPServers[0].Auth.Region != "us-west-2" {
		t.Fatalf("unexpected mcp server auth: %+v", contract.MCPServers)
	}
}

func TestCompileYAMLRequiresSigV4SigningScope(t *testing.T) {
	secretID, err := publicid.Encode(publicid.KindSecret, uuid.MustParse("018f1111-1111-7111-8111-111111111114"))
	if err != nil {
		t.Fatalf("encode secret id: %v", err)
	}
	for name, auth := range map[string]string{
		"service": "region: us-west-2",
		"region":  "service: aws-mcp",
	} {
		t.Run(name, func(t *testing.T) {
			source := validAgentSource(`
mcp:
  aws:
    url: https://aws.example.com/mcp
    auth:
      type: sigv4
      secret_id: ` + secretID + `
      ` + auth + `
`)
			if _, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{}); err == nil {
				t.Fatalf("expected missing %s to fail", name)
			}
		})
	}
}

func TestCompileYAMLAllowsLocalHTTPMCPOnlyWhenEnabled(t *testing.T) {
	source := validAgentSource(`
mcp:
  docs:
    url: http://localhost:8765/mcp
`)
	if _, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{}); err == nil {
		t.Fatal("expected local http mcp config to be rejected by default")
	}
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{AllowInsecureLocalMCPHTTP: true})
	if err != nil {
		t.Fatalf("compile local http mcp config with dev option: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.MCPServers) != 1 || contract.MCPServers[0].URL != "http://localhost:8765/mcp" {
		t.Fatalf("unexpected mcp servers: %+v", contract.MCPServers)
	}
	loopbackIP := strings.ReplaceAll(source, "localhost", "127.0.0.1")
	if _, err := Compile(
		SourceFormatYAML,
		[]byte(loopbackIP),
		CompileOptions{AllowInsecureLocalMCPHTTP: true},
	); err != nil {
		t.Fatalf("compile loopback http mcp config with dev option: %v", err)
	}
	publicHTTP := strings.ReplaceAll(source, "localhost:8765", "example.com")
	if _, err := Compile(
		SourceFormatYAML,
		[]byte(publicHTTP),
		CompileOptions{AllowInsecureLocalMCPHTTP: true},
	); err == nil {
		t.Fatal("expected public http mcp config to be rejected")
	}
}

func TestAskQuestionInputSchemaDoesNotExposeInteractionOwnedFields(t *testing.T) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("default tool catalog: %v", err)
	}
	entry, ok := catalog.Lookup("ask_question")
	if !ok {
		t.Fatal("ask_question missing from default tool catalog")
	}
	if strings.Contains(string(entry.InputSchema), `"allows_text"`) {
		t.Fatalf("ask_question input schema exposes allows_text: %s", entry.InputSchema)
	}
}

func assertReadProcessInputSchema(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type    string `json:"type"`
			Minimum int    `json:"minimum"`
			Maximum int    `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode read_process schema: %v", err)
	}
	maxBytes := schema.Properties["max_bytes"]
	if maxBytes.Type != "integer" || maxBytes.Minimum != 1 || maxBytes.Maximum != processaction.MaxObservationBytes {
		t.Fatalf("read_process max_bytes schema = %+v", maxBytes)
	}
	waitMS := schema.Properties["wait_ms"]
	if waitMS.Type != "integer" || waitMS.Minimum != 1 || waitMS.Maximum != processaction.MaxWaitMilliseconds {
		t.Fatalf("read_process wait_ms schema = %+v", waitMS)
	}
}

func assertRunCommandInputSchema(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var schema struct {
		Type                 string `json:"type"`
		AdditionalProperties *bool  `json:"additionalProperties"`
		Required             []string
		Properties           map[string]struct {
			Type    string   `json:"type"`
			Enum    []string `json:"enum"`
			Minimum int      `json:"minimum"`
			Maximum int      `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode run_command schema: %v", err)
	}
	if schema.Type != toolcatalog.ToolInputSchemaObject ||
		schema.AdditionalProperties == nil ||
		*schema.AdditionalProperties ||
		len(schema.Required) != 1 ||
		schema.Required[0] != "command" {
		t.Fatalf("unexpected run_command schema envelope: %s", string(raw))
	}
	if _, ok := schema.Properties["login"]; ok {
		t.Fatalf("run_command schema must not expose login: %s", string(raw))
	}
	if _, ok := schema.Properties["argv"]; ok {
		t.Fatalf("run_command schema must not expose argv: %s", string(raw))
	}
	if command := schema.Properties["command"]; command.Type != "string" {
		t.Fatalf("run_command command schema = %+v", command)
	}
	if machineRef := schema.Properties["machine_ref"]; machineRef.Type != "string" {
		t.Fatalf("run_command machine_ref schema = %+v", machineRef)
	}
	wantSelectors := []string{"default", "sh", "bash", "zsh", "pwsh", "powershell", "cmd"}
	if got := schema.Properties["shell"].Enum; !sameStringSliceForAgentConfig(got, wantSelectors) {
		t.Fatalf("shell selector enum = %v, want %v", got, wantSelectors)
	}
	waitMS := schema.Properties["wait_ms"]
	if waitMS.Type != "integer" || waitMS.Minimum != 1 || waitMS.Maximum != processaction.MaxWaitMilliseconds {
		t.Fatalf("run_command wait_ms schema = %+v", waitMS)
	}
	if ioMode := schema.Properties["io_mode"]; ioMode.Type != "string" ||
		!sameStringSliceForAgentConfig(ioMode.Enum, []string{"pipe", "pty"}) {
		t.Fatalf("run_command io_mode schema = %+v", ioMode)
	}
}

func sameStringSliceForAgentConfig(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func testMachineSourceCompileOptions(t *testing.T) CompileOptions {
	t.Helper()
	return CompileOptions{
		ResolveMachineName: func(machineName string) (string, error) {
			return testMachineSourcePublicID(t, publicid.KindMachine, machineName), nil
		},
		ResolveMachinePoolName: func(machinePoolName string) (string, error) {
			return testMachineSourcePublicID(t, publicid.KindMachinePool, machinePoolName), nil
		},
	}
}

func testMachineSourcePublicID(t *testing.T, kind publicid.Kind, name string) string {
	t.Helper()
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(string(kind)+":"+name))
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		t.Fatalf("encode %s public id: %v", kind, err)
	}
	return encoded
}

func publicidTestID(n int) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("agentconfig-test-%d", n)))
}

func TestCompileYAMLIncludesInstructionAndOptionalMachineSource(t *testing.T) {
	machineName := "Primary Machine"
	machineID := testMachineSourcePublicID(t, publicid.KindMachine, machineName)
	secretID := testMachineSourcePublicID(t, publicid.KindSecret, "Machine Secret")
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: `+machineName+`
    cwd: /workspace
    description: Primary dev machine
    env_overlay:
      APP_ENV: test
    secret_env_overlay:
      API_TOKEN: `+secretID+`
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t))
	if err != nil {
		t.Fatalf("compile with machine sources: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if contract.Instruction != "Help the user make progress." {
		t.Fatalf("unexpected instruction: %q", contract.Instruction)
	}
	if contract.MachineSources[0].MachineID != machineID || contract.MachineSources[0].Cwd != "/workspace" ||
		contract.MachineSources[0].Description != "Primary dev machine" ||
		contract.MachineSources[0].EnvOverlay["APP_ENV"] == nil ||
		*contract.MachineSources[0].EnvOverlay["APP_ENV"] != "test" ||
		contract.MachineSources[0].SecretEnvOverlay["API_TOKEN"] == nil ||
		*contract.MachineSources[0].SecretEnvOverlay["API_TOKEN"] != secretID {
		t.Fatalf("unexpected machine source contract: %+v", contract.MachineSources[0])
	}
}

func TestCompileYAMLIncludesMachinePoolSource(t *testing.T) {
	poolName := "Build Pool"
	poolID := testMachineSourcePublicID(t, publicid.KindMachinePool, poolName)
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: `+poolName+`
    max_machines: 5
    initial_num_machines: 3
    cwd: /workspace
    description: Agent pool machine
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t))
	if err != nil {
		t.Fatalf("compile with pool machine source: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if contract.MachineSources[0].MachinePoolID != poolID || contract.MachineSources[0].MachineID != "" ||
		contract.MachineSources[0].MaxMachines != 5 ||
		contract.MachineSources[0].InitialNumMachines != 3 ||
		contract.MachineSources[0].Cwd != "/workspace" ||
		contract.MachineSources[0].Description != "Agent pool machine" {
		t.Fatalf("unexpected machine source contract: %+v", contract.MachineSources[0])
	}
	if compiled.Compiled.MachineSources[0].MaxMachines != 5 ||
		compiled.Compiled.MachineSources[0].InitialNumMachines != 3 {
		t.Fatalf("compiled pool machine counts = %+v, want max=5 initial=3", compiled.Compiled.MachineSources[0])
	}
}

func TestCompileYAMLIncludesMachinePoolSourceConfig(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Build Pool
    machine_cpu: 2
    machine_memory_mb: 4096
    env_overlay:
      APP_ENV: test
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t))
	if err != nil {
		t.Fatalf("compile with pool machine source config: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	source := contract.MachineSources[0]
	if source.MachineCPU == nil || *source.MachineCPU != 2 ||
		source.MachineMemoryMB == nil || *source.MachineMemoryMB != 4096 ||
		source.EnvOverlay["APP_ENV"] == nil || *source.EnvOverlay["APP_ENV"] != "test" {
		t.Fatalf("machine source config = %+v", source)
	}
}

func TestCompileYAMLIncludesMachinePoolSourceSecretEnv(t *testing.T) {
	secretID := testMachineSourcePublicID(t, publicid.KindSecret, "Pool Secret")
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Build Pool
    secret_env_overlay:
      API_TOKEN: `+secretID+`
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t))
	if err != nil {
		t.Fatalf("compile with pool machine source secret_env: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	source := contract.MachineSources[0]
	if source.SecretEnvOverlay["API_TOKEN"] == nil ||
		*source.SecretEnvOverlay["API_TOKEN"] != secretID {
		t.Fatalf("machine source secret_env_overlay = %+v", source.SecretEnvOverlay)
	}
}

func TestCompileYAMLValidatesMachinePoolSourceSecretEnv(t *testing.T) {
	secretID := testMachineSourcePublicID(t, publicid.KindSecret, "Pool Secret")
	opts := testMachineSourceCompileOptions(t)
	var validatedSecretID string
	var validatedKind secrets.Kind
	opts.ValidateSecretID = func(secretID string, expectedKind secrets.Kind) error {
		validatedSecretID = secretID
		validatedKind = expectedKind
		return nil
	}
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Build Pool
    secret_env_overlay:
      API_TOKEN: `+secretID+`
      REMOVED: null
tools:
  run_command: {}
`)), opts); err != nil {
		t.Fatalf("compile with pool machine source secret_env: %v", err)
	}
	if validatedSecretID != secretID || validatedKind != "generic" {
		t.Fatalf("secret validation = %q/%q, want %q/generic", validatedSecretID, validatedKind, secretID)
	}
}

func TestCompileYAMLRejectsInvalidMachinePoolSourceSecretEnv(t *testing.T) {
	secretID := testMachineSourcePublicID(t, publicid.KindSecret, "Pool Secret")
	opts := testMachineSourceCompileOptions(t)
	validationErr := errors.New("secret is not available")
	opts.ValidateSecretID = func(string, secrets.Kind) error {
		return validationErr
	}
	_, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Build Pool
    secret_env_overlay:
      API_TOKEN: `+secretID+`
tools:
  run_command: {}
`)), opts)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"machine_sources[0].secret_env_overlay.API_TOKEN: secret is not available",
		) {
		t.Fatalf("compile error = %v", err)
	}
}

func TestCompileYAMLIncludesNamedMachinePoolSource(t *testing.T) {
	poolID := testMachineSourcePublicID(t, publicid.KindMachinePool, "Hosted Pool")
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Hosted Pool
    max_machines: 3
    initial_num_machines: 2
    cwd: /workspace
    description: Hosted machine
    machine_cpu: 2
    machine_memory_mb: 4096
    env_overlay:
      APP_ENV: test
    machine_provider_options_overlay:
      startup_script: echo ready
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t))
	if err != nil {
		t.Fatalf("compile with named machine pool source: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	source := contract.MachineSources[0]
	if source.MachinePoolID != poolID || source.MachineID != "" || source.MaxMachines != 3 ||
		source.InitialNumMachines != 2 ||
		source.Cwd != "/workspace" ||
		source.Description != "Hosted machine" {
		t.Fatalf("unexpected named machine pool source: %+v", source)
	}
	if source.MachineCPU == nil || *source.MachineCPU != 2 ||
		source.MachineMemoryMB == nil || *source.MachineMemoryMB != 4096 ||
		source.EnvOverlay["APP_ENV"] == nil || *source.EnvOverlay["APP_ENV"] != "test" ||
		string(source.MachineProviderOptionsOverlay["startup_script"]) != `"echo ready"` {
		t.Fatalf("named machine pool config = %+v", source)
	}
}

func TestCompileYAMLDefersMachinePoolProviderOptionValueValidation(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Hosted Pool
    machine_provider_options_overlay:
      startup_script:
        - echo bad
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t)); err != nil {
		t.Fatalf("compile should defer machine pool provider option value validation: %v", err)
	}
}

func TestCompileYAMLIncludesMultipleBYOMachinesAndMachinePools(t *testing.T) {
	firstMachineName := "Primary Machine"
	secondMachineName := "Secondary Machine"
	firstPoolName := "Build Pool"
	secondPoolName := "Test Pool"
	firstMachineID := testMachineSourcePublicID(t, publicid.KindMachine, firstMachineName)
	secondMachineID := testMachineSourcePublicID(t, publicid.KindMachine, secondMachineName)
	firstPoolID := testMachineSourcePublicID(t, publicid.KindMachinePool, firstPoolName)
	secondPoolID := testMachineSourcePublicID(t, publicid.KindMachinePool, secondPoolName)
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: `+firstMachineName+`
    cwd: /workspace/primary
    description: Primary dev machine
  - machine_pool_name: `+firstPoolName+`
    cwd: /workspace/build
    description: Build pool machine
  - machine_name: `+secondMachineName+`
    cwd: /workspace/secondary
    description: Secondary dev machine
  - machine_pool_name: `+secondPoolName+`
    cwd: /workspace/test
    description: Test pool machine
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t))
	if err != nil {
		t.Fatalf("compile with multiple byo machines and pools: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.MachineSources) != 4 {
		t.Fatalf("machine source count = %d, want 4: %+v", len(contract.MachineSources), contract.MachineSources)
	}
	want := []RuntimeMachine{
		{MachineID: firstMachineID, Cwd: "/workspace/primary", Description: "Primary dev machine"},
		{
			MachinePoolID:      firstPoolID,
			MaxMachines:        1,
			InitialNumMachines: 1,
			Cwd:                "/workspace/build",
			Description:        "Build pool machine",
		},
		{MachineID: secondMachineID, Cwd: "/workspace/secondary", Description: "Secondary dev machine"},
		{
			MachinePoolID:      secondPoolID,
			MaxMachines:        1,
			InitialNumMachines: 1,
			Cwd:                "/workspace/test",
			Description:        "Test pool machine",
		},
	}
	for index, machine := range contract.MachineSources {
		if !reflect.DeepEqual(machine, want[index]) {
			t.Fatalf("machine %d = %+v, want %+v", index, machine, want[index])
		}
	}
}

func TestCompileYAMLRejectsDuplicateMachinePoolSources(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Build Pool
  - machine_pool_name: Build Pool
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t)); err == nil || !strings.Contains(err.Error(), "duplicates a machine pool") {
		t.Fatalf("duplicate pool error = %v", err)
	}
}

func TestCompileYAMLRejectsDuplicateMachineSources(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: Primary Machine
  - machine_name: Primary Machine
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t)); err == nil || !strings.Contains(err.Error(), "duplicates a machine id") {
		t.Fatalf("duplicate machine error = %v", err)
	}
}

func TestCompileYAMLRejectsMissingInstruction(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(`
model:
  provider_config: openai-prod
  name: gpt-test
`), CompileOptions{}); err == nil {
		t.Fatal("expected missing instruction to be rejected")
	}
}

func TestCompileYAMLRejectsWhitespaceInstruction(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(`
instruction: "   "
model:
  provider_config: openai-prod
  name: gpt-test
`), CompileOptions{}); err == nil {
		t.Fatal("expected whitespace-only instruction to be rejected")
	}
}

func TestCompileYAMLRejectsInvalidToolName(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  "1bad_name": {}
`)), CompileOptions{}); err == nil {
		t.Fatal("expected tool name starting with a digit to be rejected")
	}
}

func TestCompileYAMLRejectsMachineSettingsWithoutMachineSource(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - cwd: /workspace
    description: Missing source
tools:
  run_command: {}
`)), CompileOptions{}); err == nil {
		t.Fatal("expected machine settings without machine_name or machine_pool_name to be rejected")
	}
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: "   "
tools:
  run_command: {}
`)), CompileOptions{}); err == nil {
		t.Fatal("expected whitespace-only machine_name to be rejected")
	}
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: Primary Machine
    machine_pool_name: Build Pool
tools:
  run_command: {}
`)), CompileOptions{}); err == nil {
		t.Fatal("expected machine config with both machine_name and machine_pool_name to be rejected")
	}
}

func TestCompileYAMLRejectsInvalidMachinePoolCounts(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "max on machine source",
			source: `
machine_sources:
  - machine_name: Primary Machine
    max_machines: 2
tools:
  run_command: {}
`,
		},
		{
			name: "initial on machine source",
			source: `
machine_sources:
  - machine_name: Primary Machine
    initial_num_machines: 1
tools:
  run_command: {}
`,
		},
		{
			name: "default machine fields on machine source",
			source: `
machine_sources:
  - machine_name: Primary Machine
    machine_cpu: 2
tools:
  run_command: {}
`,
		},
		{
			name: "empty default machine overlay on machine source",
			source: `
machine_sources:
  - machine_name: Primary Machine
    env_overlay: {}
tools:
  run_command: {}
`,
		},
		{
			name: "negative max",
			source: `
machine_sources:
  - machine_pool_name: Build Pool
    max_machines: -1
tools:
  run_command: {}
`,
		},
		{
			name: "negative initial",
			source: `
machine_sources:
  - machine_pool_name: Build Pool
    initial_num_machines: -1
tools:
  run_command: {}
`,
		},
		{
			name: "initial exceeds max",
			source: `
machine_sources:
  - machine_pool_name: Build Pool
    max_machines: 2
    initial_num_machines: 3
tools:
  run_command: {}
`,
		},
		{
			name: "initial exceeds zero max",
			source: `
machine_sources:
  - machine_pool_name: Build Pool
    max_machines: 0
    initial_num_machines: 1
tools:
  run_command: {}
`,
		},
		{
			name: "max int32 overflow",
			source: fmt.Sprintf(`
machine_sources:
  - machine_pool_name: Build Pool
    max_machines: %d
tools:
  run_command: {}
`, math.MaxInt32+1),
		},
		{
			name: "initial int32 overflow",
			source: fmt.Sprintf(`
machine_sources:
  - machine_pool_name: Build Pool
    max_machines: %d
    initial_num_machines: %d
tools:
  run_command: {}
`, math.MaxInt32, math.MaxInt32+1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(tt.source)), CompileOptions{}); err == nil {
				t.Fatal("expected invalid machine pool count to be rejected")
			}
		})
	}
}

func TestCompileYAMLAllowsZeroMaxMachines(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Build Pool
    max_machines: 0
tools:
  run_command: {}
`)), testMachineSourceCompileOptions(t))
	if err != nil {
		t.Fatalf("compile zero max machines: %v", err)
	}
	source := compiled.Compiled.MachineSources[0]
	if source.MaxMachines != 0 || source.InitialNumMachines != 0 {
		t.Fatalf("zero max machine source = %+v, want max=0 initial=0", source)
	}
}

func TestCompiledToolContractStoresOnlyUserOwnedToolSettings(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command: {}
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile run_command: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(compiled.CanonicalJSON, &body); err != nil {
		t.Fatalf("unmarshal compiled: %v", err)
	}
	tools, ok := body["tools"].(map[string]any)
	if !ok {
		t.Fatalf("compiled tools missing: %s", string(compiled.CanonicalJSON))
	}
	runCommand, ok := tools["run_command"].(map[string]any)
	if !ok {
		t.Fatalf("compiled run_command missing: %s", string(compiled.CanonicalJSON))
	}
	wantKeys := map[string]struct{}{
		"permission": {},
		"enabled":    {},
	}
	for key := range runCommand {
		if _, ok := wantKeys[key]; !ok {
			t.Fatalf("compiled run_command stores non-user-owned %q: %s", key, string(compiled.CanonicalJSON))
		}
	}
}

func TestCompileYAMLCustomTool(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  lookup_customer:
    type: custom
    description: Look up a customer by email.
    input_schema:
      type: object
      properties:
        email:
          type: string
          description: Customer email address.
      required:
        - email
      additionalProperties: false
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile custom tool: %v", err)
	}
	tool := compiled.Compiled.Tools["lookup_customer"]
	if !tool.Enabled || tool.Type != toolcatalog.ToolTypeCustom ||
		tool.Permission.Mode != toolpermission.ModeAlwaysAllow ||
		tool.Description != "Look up a customer by email." {
		t.Fatalf("unexpected compiled custom tool: %+v", tool)
	}
	var schema struct {
		Type       string `json:"type"`
		Required   []string
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("decode custom schema: %v", err)
	}
	if schema.Type != toolcatalog.ToolInputSchemaObject || len(schema.Required) != 1 || schema.Required[0] != "email" ||
		schema.Properties["email"].Type != "string" ||
		schema.Properties["email"].Description == "" {
		t.Fatalf("unexpected custom schema: %s", string(tool.InputSchema))
	}
	contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.Tools) != 1 {
		t.Fatalf("runtime tools = %+v, want one custom tool", contract.Tools)
	}
	runtimeTool := contract.Tools[0]
	if runtimeTool.Name != "lookup_customer" || runtimeTool.Type != toolcatalog.ToolTypeCustom ||
		runtimeTool.Description != "Look up a customer by email." {
		t.Fatalf("unexpected runtime custom tool: %+v", runtimeTool)
	}
}

func TestCompileYAMLCustomToolMayUseDoubleUnderscore(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  docs__search:
    type: custom
    description: Search project documentation.
    input_schema:
      type: object
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile custom tool outside reserved MCP namespace: %v", err)
	}
	if _, ok := compiled.Compiled.Tools["docs__search"]; !ok {
		t.Fatal("compiled custom tool is missing")
	}
}

func TestCompileYAMLRejectsInvalidCustomTool(t *testing.T) {
	for name, source := range map[string]string{
		"built_in_collision": `
tools:
  run_command:
    type: custom
    description: Run process override.
    input_schema:
      type: object
`,
		"reserved_mcp_namespace": `
tools:
  mcp__docs__search:
    type: custom
    description: Impersonate a discovered MCP tool.
    input_schema:
      type: object
`,
		"missing_description": `
tools:
  lookup_customer:
    type: custom
    input_schema:
      type: object
`,
		"bad_name": `
tools:
  lookup.customer:
    type: custom
    description: Lookup.
    input_schema:
      type: object
`,
		"non_object_schema": `
tools:
  lookup_customer:
    type: custom
    description: Lookup.
    input_schema:
      type: array
`,
		"required_without_properties": `
tools:
  lookup_customer:
    type: custom
    description: Lookup.
    input_schema:
      type: object
      required: [email]
`,
		"required_missing_property": `
tools:
  lookup_customer:
    type: custom
    description: Lookup.
    input_schema:
      type: object
      required: [email]
      properties:
        id:
          type: string
`,
		"non_object_property_schema": `
tools:
  lookup_customer:
    type: custom
    description: Lookup.
    input_schema:
      type: object
      properties:
        email: string
`,
		"duplicate_schema_key": `
tools:
  lookup_customer:
    type: custom
    description: Lookup.
    input_schema:
      type: object
      type: string
`,
		"missing_input_schema": `
tools:
  lookup_customer:
    type: custom
    description: Lookup.
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(source)), CompileOptions{}); err == nil {
				t.Fatal("expected invalid custom tool to be rejected")
			}
		})
	}
}

func TestRuntimeContractRejectsUnknownCompiledToolFields(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command: {}
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile run_command: %v", err)
	}

	assertRejectsMutation := func(name string, mutate func(map[string]any)) {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(compiled.CanonicalJSON, &body); err != nil {
			t.Fatalf("unmarshal compiled: %v", err)
		}
		mutate(body)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal mutated %s: %v", name, err)
		}
		if _, err := RuntimeContractFromCompiled(raw, CompilerVersion, hashJSON(raw)); err == nil {
			t.Fatalf("expected mutated %s contract to be rejected", name)
		}
	}

	assertRejectsMutation("top-level unknown field", func(body map[string]any) {
		body["unexpected"] = true
	})
	assertRejectsMutation("tool unknown field", func(body map[string]any) {
		tools, ok := body["tools"].(map[string]any)
		if !ok {
			t.Fatalf("compiled tools shape = %#v", body["tools"])
		}
		runCommand, ok := tools["run_command"].(map[string]any)
		if !ok {
			t.Fatalf("compiled run_command shape = %#v", tools["run_command"])
		}
		runCommand["unexpected"] = true
	})
}

func TestRuntimeContractRejectsBuiltInNameMarkedCustom(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command: {}
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile built-in tool: %v", err)
	}

	toolBody := func(t *testing.T, body map[string]any, name string) map[string]any {
		t.Helper()
		tools, ok := body["tools"].(map[string]any)
		if !ok {
			t.Fatalf("compiled tools shape = %#v", body["tools"])
		}
		tool, ok := tools[name].(map[string]any)
		if !ok {
			t.Fatalf("compiled tool %q shape = %#v", name, tools[name])
		}
		return tool
	}

	var body map[string]any
	if err := json.Unmarshal(compiled.CanonicalJSON, &body); err != nil {
		t.Fatalf("unmarshal compiled: %v", err)
	}
	toolBody(t, body, "run_command")["type"] = toolcatalog.ToolTypeCustom
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal mutated compiled: %v", err)
	}
	if _, err := RuntimeContractFromCompiled(raw, CompilerVersion, hashJSON(raw)); err == nil {
		t.Fatal("expected built-in name marked custom to be rejected")
	}
}

func TestRuntimeContractRejectsDefinitionHashMismatch(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource("")), CompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := RuntimeContractFromCompiled(
		json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion,
		"not-the-real-hash",
	); err == nil {
		t.Fatal("expected definition hash mismatch")
	}
}

func TestCompileYAMLRejectsUnknownToolName(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  definitely_unknown_tool: {}
`)), CompileOptions{}); err == nil {
		t.Fatal("expected unknown tool to be rejected")
	}
}

func TestCompileYAMLRejectsBuiltInToolWithCustomFields(t *testing.T) {
	for name, source := range map[string]string{
		"description_on_builtin": `
tools:
  run_command:
    description: Should not be allowed.
`,
		"description_on_explicit_builtin": `
tools:
  run_command:
    type: built_in
    description: Should not be allowed.
`,
		"input_schema_on_builtin": `
tools:
  run_command:
    input_schema:
      type: object
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(source)), CompileOptions{}); err == nil {
				t.Fatal("expected built-in tool with custom-only fields to be rejected")
			}
		})
	}
}

func TestCompileYAMLRejectsInvalidModel(t *testing.T) {
	for name, source := range map[string]string{
		"missing_model": "instruction: Help the user make progress.\n",
		"missing_model_name": `
model:
  provider_config: openai-prod
`,
		"removed_model_options": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
  model_options:
    temperature: 0.2
`,
		"removed_compat_options": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
  compat_options:
    provider: openrouter
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{}); err == nil {
				t.Fatal("expected invalid config to be rejected")
			}
		})
	}
}

func TestCompileYAMLPersistsOnlyResolvedConfiguredModel(t *testing.T) {
	supportsTools := true
	result, err := Compile(SourceFormatYAML, []byte(`
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
  context_window_tokens: 100000
  default_max_output_tokens: 16000
  cache_retention: short
  reasoning:
    effort: high
`), CompileOptions{
		ResolveModelSelection: func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{
				ConfiguredModelID: "configured_model_test",
				SupportsTools:     &supportsTools,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("compile config: %v", err)
	}
	model := result.Compiled.Model
	if model.ConfiguredModelID != "configured_model_test" {
		t.Fatalf("compiled model did not persist resolved configured model: %+v", model)
	}
	if model.ContextWindowTokens == nil || *model.ContextWindowTokens != 100000 || model.DefaultMaxOutputTokens == nil ||
		*model.DefaultMaxOutputTokens != 16000 ||
		model.CacheRetention != "short" ||
		model.Reasoning == nil ||
		model.Reasoning.Effort != "high" {
		t.Fatalf("compiled model did not persist agent model defaults: %+v", model)
	}
	canonical := string(result.CanonicalJSON)
	for _, legacyField := range []string{
		"provider_config",
		"provider_model_slug",
		"configured_model_revision_id",
		"api_format",
		"api_variant",
	} {
		if strings.Contains(canonical, legacyField) {
			t.Fatalf("compiled model duplicated %q in canonical JSON: %s", legacyField, canonical)
		}
	}
}

func TestCompileYAMLRejectsToolsWhenResolvedModelDoesNotSupportTools(t *testing.T) {
	supportsTools := false
	source := validAgentSource(`
tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
`)
	_, err := Compile(
		SourceFormatYAML,
		[]byte(source),
		CompileOptions{
			ResolveModelSelection: func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error) {
				return ResolvedModelSelection{
					ConfiguredModelID: "configured_model_test",
					SupportsTools:     &supportsTools,
				}, nil
			},
		},
	)
	if err == nil {
		t.Fatal("expected tool-using config to be rejected")
	}
	if !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileYAMLRejectsSkillsWhenResolvedModelDoesNotSupportTools(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, publicidTestID(81))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	supportsTools := false
	_, err = Compile(SourceFormatYAML, []byte(validAgentSource(`
skills:
  - `+skillID+`
`)), CompileOptions{
		ResolveModelSelection: func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{
				ConfiguredModelID: "configured_model_test",
				SupportsTools:     &supportsTools,
			}, nil
		},
		ResolveSkillID: func(id string) (SkillResolution, error) {
			return SkillResolution{PublicID: id, Name: "pdf-tools"}, nil
		},
	})
	if err == nil {
		t.Fatal("expected skills-attached config to be rejected")
	}
	if !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileExplicitSkillToolOverridesImplicitAttachment(t *testing.T) {
	explicit, err := Compile(
		SourceFormatYAML,
		[]byte(validAgentSource(`
tools:
  skill:
    permission:
      mode: always_ask
`)),
		CompileOptions{},
	)
	if err != nil {
		t.Fatalf("compile explicit skill tool: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(
		explicit.CanonicalJSON,
		CompilerVersion,
		explicit.Hash,
	)
	if err != nil {
		t.Fatalf("load explicit skill runtime contract: %v", err)
	}
	if len(contract.Tools) != 1 ||
		contract.Tools[0].Name != "skill" ||
		contract.Tools[0].Permission.Mode != toolpermission.ModeAlwaysAsk {
		t.Fatalf("unexpected explicit skill runtime contract: %+v", contract)
	}

	skillID, err := publicid.Encode(publicid.KindSkill, publicidTestID(86))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	supportsTools := false
	disabled, err := Compile(
		SourceFormatYAML,
		[]byte(validAgentSource(`
tools:
  skill:
    enabled: false
skills:
  - `+skillID+`
`)),
		CompileOptions{
			ResolveModelSelection: func(
				providerConfig string,
				configuredModelName string,
			) (ResolvedModelSelection, error) {
				return ResolvedModelSelection{
					ConfiguredModelID: "configured_model_without_tools",
					SupportsTools:     &supportsTools,
				}, nil
			},
			ResolveSkillID: func(id string) (SkillResolution, error) {
				return SkillResolution{PublicID: id, Name: "pdf-tools"}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("compile explicitly disabled skill tool: %v", err)
	}
	contract, err = RuntimeContractFromCompiled(
		disabled.CanonicalJSON,
		CompilerVersion,
		disabled.Hash,
	)
	if err != nil {
		t.Fatalf("load disabled skill runtime contract: %v", err)
	}
	if contract.RequiresModelToolSupport() || len(contract.Tools) != 0 || len(contract.Skills) != 1 {
		t.Fatalf("disabled skill tool did not override attachment: %+v", contract)
	}
}

func TestCompileSkillsAllowedWithoutMachineSources(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, publicidTestID(82))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	opts := CompileOptions{
		ResolveSkillID: func(id string) (SkillResolution, error) {
			return SkillResolution{PublicID: id, Name: "pdf-tools"}, nil
		},
	}
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
skills:
  - `+skillID+`
`)), opts)
	if err != nil {
		t.Fatalf("compile without machine_sources: %v", err)
	}
	if len(result.Compiled.Skills) != 1 || result.Compiled.Skills[0].PublicID != skillID {
		t.Fatalf("unexpected compiled skills: %+v", result.Compiled.Skills)
	}
}

func TestRuntimeContractSkillsRequireModelToolSupport(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, publicidTestID(87))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	compiled, err := Compile(
		SourceFormatYAML,
		[]byte(validAgentSource(`
skills:
  - `+skillID+`
`)),
		CompileOptions{
			ResolveSkillID: func(id string) (SkillResolution, error) {
				return SkillResolution{PublicID: id, Name: "pdf-tools"}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("compile skill config: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(
		compiled.CanonicalJSON,
		CompilerVersion,
		compiled.Hash,
	)
	if err != nil {
		t.Fatalf("load skill runtime contract: %v", err)
	}
	if !contract.RequiresModelToolSupport() {
		t.Fatal("skill-only runtime contract must require model tool support")
	}
	if len(contract.Tools) != 1 ||
		contract.Tools[0].Name != "skill" ||
		contract.Tools[0].Permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("implicit skill tool was not materialized: %+v", contract.Tools)
	}
}

func TestCompileYAMLRejectsMCPDeclarationWhenResolvedModelDoesNotSupportTools(t *testing.T) {
	supportsTools := false
	_, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
mcp:
  docs:
    url: https://example.com/mcp
    default_enabled: false
`)), CompileOptions{
		ResolveModelSelection: func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{
				ConfiguredModelID: "configured_model_test",
				SupportsTools:     &supportsTools,
			}, nil
		},
	})
	if err == nil {
		t.Fatal("expected mcp declaration to be rejected")
	}
	if !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileYAMLAllowsDisabledToolsWhenResolvedModelDoesNotSupportTools(t *testing.T) {
	supportsTools := false
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command:
    enabled: false
`)), CompileOptions{
		ResolveModelSelection: func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{
				ConfiguredModelID: "configured_model_test",
				SupportsTools:     &supportsTools,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("compile config: %v", err)
	}
	if result.Compiled.Model.ConfiguredModelID != "configured_model_test" {
		t.Fatalf("compiled configured model = %q", result.Compiled.Model.ConfiguredModelID)
	}
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.Tools) != 0 {
		t.Fatalf("disabled tool should not appear in runtime contract: %+v", contract.Tools)
	}
	if len(contract.MCPServers) != 0 {
		t.Fatalf("disabled local tool config should not create mcp servers: %+v", contract.MCPServers)
	}
}

func TestCompileYAMLRejectsUnsupportedPermissionAndUnknownFields(t *testing.T) {
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command:
    permission:
      mode: maybe
      parameters: {}
`)), CompileOptions{}); err == nil {
		t.Fatal("expected unsupported permission error")
	}
	if _, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  definitely_unknown_tool: {}
`)), CompileOptions{}); err == nil {
		t.Fatal("expected unknown tool to be rejected")
	}
	if _, err := Compile(SourceFormatYAML, []byte(`
model:
  provider_config: openai-prod
  name: gpt-test
unknown: true
`), CompileOptions{}); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestCompileSkillsResolverInvokedAndPopulatesContract(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, publicidTestID(72))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	var resolved []string
	opts := testMachineSourceCompileOptions(t)
	opts.ResolveSkillID = func(id string) (SkillResolution, error) {
		resolved = append(resolved, id)
		if id != skillID {
			t.Fatalf("resolver got unexpected id: %s", id)
		}
		return SkillResolution{
			PublicID: skillID,
			Name:     "pdf-tools",
		}, nil
	}
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: Primary Machine
skills:
  - `+skillID+`
`)), opts)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !reflect.DeepEqual(resolved, []string{skillID}) {
		t.Fatalf("resolver calls = %v, want [%s]", resolved, skillID)
	}
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if len(contract.Skills) != 1 || contract.Skills[0].PublicID != skillID {
		t.Fatalf("unexpected skills contract: %+v", contract.Skills)
	}
}

func TestCompileSkillsRejectsDuplicateIDs(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, publicidTestID(74))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	_, err = Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: Primary Machine
skills:
  - `+skillID+`
  - `+skillID+`
`)), testMachineSourceCompileOptions(t))
	if err == nil {
		t.Fatalf("expected JSON-schema uniqueItems error")
	}
}

func TestCompileSkillsRejectsDuplicateNames(t *testing.T) {
	skillOne, err := publicid.Encode(publicid.KindSkill, publicidTestID(76))
	if err != nil {
		t.Fatalf("encode skill one: %v", err)
	}
	skillTwo, err := publicid.Encode(publicid.KindSkill, publicidTestID(77))
	if err != nil {
		t.Fatalf("encode skill two: %v", err)
	}
	opts := testMachineSourceCompileOptions(t)
	opts.ResolveSkillID = func(id string) (SkillResolution, error) {
		return SkillResolution{
			PublicID: id,
			Name:     "deploy",
		}, nil
	}
	_, err = Compile(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_name: Primary Machine
skills:
  - `+skillOne+`
  - `+skillTwo+`
`)), opts)
	if err == nil {
		t.Fatalf("expected duplicate skill name to be rejected")
	}
	if !strings.Contains(err.Error(), "attached more than once") {
		t.Fatalf("error did not mention duplicate-name rule: %v", err)
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("error did not include the colliding name: %v", err)
	}
}

func validAgentSource(extra string) string {
	base := `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
`
	return base + strings.TrimPrefix(extra, "\n")
}

func hashJSON(raw json.RawMessage) string {
	canonical := canonicalizeJSON(raw)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
