package agentconfig

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestSourceFromCompiledRoundTrips(t *testing.T) {
	names := map[uuid.UUID]string{}
	resolve := func(kind, name string) uuid.UUID {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(kind+":"+name))
		names[id] = name
		return id
	}
	integrations := map[string]IntegrationResolution{
		"engineering-team": {IntegrationID: publicidTestID(120), IntegrationKind: integrationdefinition.SlackThread},
		"reviews":          {IntegrationID: publicidTestID(160), IntegrationKind: integrationdefinition.GitHubPR},
	}
	lookup := func(id uuid.UUID) (string, error) {
		name, ok := names[id]
		if !ok {
			return "", fmt.Errorf("unknown id %s", id)
		}
		return name, nil
	}
	opts := CompileOptions{
		ResolveModelSelection: func(providerConfig, name string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{ConfiguredModelID: resolve("model", providerConfig+"/"+name)}, nil
		},
		ResolveMachineName:      func(name string) (uuid.UUID, error) { return resolve("machine", name), nil },
		ResolveMachinePoolName:  func(name string) (uuid.UUID, error) { return resolve("pool", name), nil },
		ResolveAgentProfileName: func(name string) (uuid.UUID, error) { return resolve("profile", name), nil },
		ResolveMemoryStoreName:  func(name string) (uuid.UUID, error) { return resolve("memory", name), nil },
		ResolveSkillID: func(id string) (SkillResolution, error) {
			return SkillResolution{ID: uuid.Must(publicid.Decode(publicid.KindSkill, id)), Name: "test-skill"}, nil
		},
		ResolveIntegrationName: func(name string) (IntegrationResolution, error) {
			integration, ok := integrations[name]
			if !ok {
				return IntegrationResolution{}, fmt.Errorf("integration %s is unavailable", name)
			}
			return integration, nil
		},
	}
	sourceNames := SourceNames{
		Model: func(id uuid.UUID) (string, string, error) {
			name, err := lookup(id)
			providerConfig, modelName, _ := strings.Cut(name, "/")
			return providerConfig, modelName, err
		},
		Machine: lookup, MachinePool: lookup, Profile: lookup, MemoryStore: lookup,
	}
	secretID := testMachineSourcePublicID(t, publicid.KindSecret, "token")
	authored, err := Compile(SourceFormatYAML, []byte(`version: v1
instruction: |-
  Help the user.
  Be concise.
model:
  provider_config: openai-prod
  name: gpt-test
  context_window_tokens: 64000
  default_max_output_tokens: 256
  cache_retention: long
  reasoning: {effort: high}
machine_sources:
  - machine_name: build-box
    cwd: /workspace
    description: Build box
    env_overlay: {APP_ENV: test, API_VERSION: "2023-06-01", FLAG: "on", MODE: "0o17"}
    secret_env_overlay: {TOKEN: `+secretID+`, UNSET: null}
  - machine_pool_name: build-pool
    max_machines: 2
    initial_num_machines: 1
    delete_after_idle_minutes: 30
    machine_cpu: 4
    machine_memory_mb: 2048
    machine_provider_options_overlay: {image: custom}
tools:
  run_command: {permission: {mode: always_ask}}
  web_fetch: {enabled: false}
  lookup_order:
    type: custom
    description: Look up an order.
    input_schema: {type: object, properties: {id: {type: string}}}
  int__engineering-team__post_message: {deferred: true}
mcp:
  docs:
    url: https://example.com/mcp
    default_enabled: false
    permission: {mode: always_allow}
    deferred: true
    auth: {type: sigv4, secret_id: `+secretID+`, service: execute-api, region: us-east-1}
    tools: {search: {enabled: true, permission: {mode: always_allow}, deferred: false}}
  search:
    url: https://example.com/search
interaction_handlers:
  engineering-team: {}
git_credentials: {integration: reviews}
skills: [`+testMachineSourcePublicID(t, publicid.KindSkill, "test-skill")+`]
subagents:
  helper:
    type: profile
    profile: researcher
    description: Researches questions.
    model:
      provider_config: openai-prod
      name: gpt-mini
      context_window_tokens: 32000
      default_max_output_tokens: 128
      cache_retention: short
      reasoning: {effort: low}
    instruction: {append: Stay focused.}
    archive_after_idle_minutes: 15
  copy: {type: self, max_instances: 2}
max_subagents: 4
max_depth: 2
event_webhook:
  url: https://example.com/events
  events: [tool_call_update]
  signing_secret_id: `+secretID+`
memory_stores:
  - {name: notes, access: read_write}
`), opts)
	require.NoError(t, err)
	require.Empty(t, unsetCompiledFields(authored.Compiled))
	base, err := Compile(SourceFormatYAML, []byte(validAgentSource("")), opts)
	require.NoError(t, err)
	additions := IntegrationCapabilitiesSource{
		Tools:               map[string]AgentConfigToolSource{"int__engineering-team__post_message": {}},
		InteractionHandlers: map[string]AgentConfigIntegrationCapabilitySource{"engineering-team": {}},
	}
	for _, name := range toolcatalog.InteractionHandlerToolNames() {
		additions.Tools[name] = AgentConfigToolSource{}
	}
	launched, err := DeriveWithIntegrationCapabilities(base.Compiled, additions, opts)
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		compiled  Compiled
		wantTools []string
	}{
		{
			name:      "authored",
			compiled:  authored.Compiled,
			wantTools: []string{"int__engineering-team__post_message", "lookup_order", "run_command", "web_fetch"},
		},
		{
			name:      "integration launch",
			compiled:  launched,
			wantTools: append([]string{"int__engineering-team__post_message"}, toolcatalog.InteractionHandlerToolNames()...),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := EncodeCompiled(test.compiled)
			require.NoError(t, err)
			source, err := SourceFromCompiled(test.compiled, sourceNames)
			require.NoError(t, err)
			require.ElementsMatch(t, test.wantTools, slices.Collect(maps.Keys(source.Tools)))
			raw, err := EncodeSourceYAML(source)
			require.NoError(t, err)
			result, err := Compile(SourceFormatYAML, []byte(raw), opts)
			require.NoError(t, err)
			require.Equal(t, encoded.Hash, result.Hash)
		})
	}
}

func unsetCompiledFields(compiled Compiled) []string {
	set := map[string]bool{}
	var walk func(reflect.Value)
	walk = func(value reflect.Value) {
		switch value.Kind() {
		case reflect.Pointer:
			if !value.IsNil() {
				walk(value.Elem())
			}
		case reflect.Slice:
			for i := range value.Len() {
				walk(value.Index(i))
			}
		case reflect.Map:
			for entries := value.MapRange(); entries.Next(); {
				walk(entries.Value())
			}
		default:
			if value.Kind() != reflect.Struct || value.Type().PkgPath() != reflect.TypeFor[Compiled]().PkgPath() {
				return
			}
			for i := range value.NumField() {
				name := value.Type().Name() + "." + value.Type().Field(i).Name
				set[name] = set[name] || !value.Field(i).IsZero()
				walk(value.Field(i))
			}
		}
	}
	walk(reflect.ValueOf(compiled))
	var unset []string
	for name, isSet := range set {
		if !isSet {
			unset = append(unset, name)
		}
	}
	return unset
}
