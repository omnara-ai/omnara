package agentconfig

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/stretchr/testify/require"
)

func gitCredentialsTestOptions(t *testing.T) CompileOptions {
	t.Helper()
	return CompileOptions{ResolveIntegrationName: func(name string) (IntegrationResolution, error) {
		require.Equal(t, "reviews", name)
		return IntegrationResolution{IntegrationID: publicidTestID(160), IntegrationKind: integrationdefinition.GitHubPR}, nil
	}}
}

func TestGitCredentialsCompileAndReferenceIndependently(t *testing.T) {
	for _, tools := range []string{"", "\ntools: {int__reviews__read: {}}"} {
		t.Run(tools, func(t *testing.T) {
			opts := gitCredentialsTestOptions(t)
			resolve := opts.ResolveIntegrationName
			calls := 0
			opts.ResolveIntegrationName = func(name string) (IntegrationResolution, error) {
				calls++
				return resolve(name)
			}
			result := compileIntegrationTest(t, "git_credentials: {integration: reviews}"+tools, opts)
			require.Equal(t, 1, calls, "tools and git credentials share one name resolution")
			want := &GitCredentialsCompiled{Integration: "reviews", IntegrationID: publicidTestID(160)}
			require.Equal(t, want, result.Compiled.GitCredentials)
			require.Equal(t, []uuid.UUID{want.IntegrationID}, ReferencedIntegrationIDs(result.Compiled))
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
			require.NoError(t, err)
			require.Equal(t, want, contract.GitCredentials)
			require.Equal(t, []uuid.UUID{want.IntegrationID}, contract.ReferencedIntegrationIDs())
			require.Equal(t, tools != "", contract.RequiresModelToolSupport())
			parsed, err := ParseSource(SourceFormatYAML, []byte(result.Source))
			require.NoError(t, err)
			require.Equal(t, &GitCredentialsSource{Integration: "reviews"}, parsed.GitCredentials)
			jsonSource, err := json.Marshal(parsed)
			require.NoError(t, err)
			fromJSON, err := Compile(SourceFormatJSON, jsonSource, opts)
			require.NoError(t, err)
			require.Equal(t, result.Hash, fromJSON.Hash)
		})
	}
}

func TestGitCredentialsSourceRequiresValidGitHubReference(t *testing.T) {
	for _, source := range []string{
		"null",
		"{}",
		"{integration: ''}",
		"{integration: bad__name}",
		"{integration: reviews, unexpected_field: true}",
		"{integration: reviews, integration_id: 00000000-0000-0000-0000-000000000001}",
	} {
		t.Run(source, func(t *testing.T) {
			_, err := Compile(
				SourceFormatYAML, []byte(validAgentSource("git_credentials: "+source)), gitCredentialsTestOptions(t),
			)
			require.Error(t, err)
		})
	}
	for _, kind := range []integrationdefinition.Kind{
		integrationdefinition.SlackThread, integrationdefinition.DiscordThread,
	} {
		t.Run(string(kind), func(t *testing.T) {
			opts := CompileOptions{ResolveIntegrationName: func(string) (IntegrationResolution, error) {
				return IntegrationResolution{IntegrationID: publicidTestID(160), IntegrationKind: kind}, nil
			}}
			_, err := Compile(SourceFormatYAML, []byte(validAgentSource("git_credentials: {integration: reviews}")), opts)
			require.ErrorContains(t, err, "GitHub integration")
		})
	}
}

func TestGitCredentialsRequireResolvedIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		opts CompileOptions
		want string
	}{
		{name: "no resolver", want: "ResolveIntegrationName"},
		{name: "unavailable", opts: CompileOptions{ResolveIntegrationName: func(string) (IntegrationResolution, error) {
			return IntegrationResolution{}, errors.New("integration unavailable")
		}}, want: "integration unavailable"},
		{name: "empty identity", opts: CompileOptions{ResolveIntegrationName: func(string) (IntegrationResolution, error) {
			return IntegrationResolution{IntegrationKind: integrationdefinition.GitHubPR}, nil
		}}, want: "empty integration ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(SourceFormatYAML, []byte(validAgentSource("git_credentials: {integration: reviews}")), test.opts)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestGitCredentialsCompiledRejectsInvalidAndConflictingPins(t *testing.T) {
	for _, test := range []struct {
		name        string
		credentials GitCredentialsCompiled
		want        string
	}{
		{"missing name", GitCredentialsCompiled{IntegrationID: publicidTestID(160)}, "integration name"},
		{
			"bad name", GitCredentialsCompiled{Integration: "bad__name", IntegrationID: publicidTestID(160)},
			"integration name",
		},
		{"missing ID", GitCredentialsCompiled{Integration: "reviews"}, "empty integration ID"},
		{
			"conflicting ID", GitCredentialsCompiled{Integration: "reviews", IntegrationID: publicidTestID(161)},
			"inconsistent pinned IDs",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := compileIntegrationTest(t,
				"tools: {int__reviews__read: {enabled: false}}", gitCredentialsTestOptions(t),
			).Compiled
			base.GitCredentials = &test.credentials
			encoded, err := EncodeCompiled(base)
			require.NoError(t, err)
			_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, encoded.Hash)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestGitCredentialsDerivationPreservesExplicitChoice(t *testing.T) {
	opts := gitCredentialsTestOptions(t)
	base := compileIntegrationTest(t, "", opts).Compiled
	addition := IntegrationCapabilitiesSource{GitCredentials: &GitCredentialsSource{Integration: "reviews"}}
	derived, err := DeriveWithIntegrationCapabilities(base, addition, opts)
	baseline := &GitCredentialsCompiled{Integration: "reviews", IntegrationID: publicidTestID(160)}
	require.NoError(t, err)
	require.Equal(t, baseline, derived.GitCredentials)
	require.Nil(t, base.GitCredentials, "derivation must not mutate the source profile")

	base.GitCredentials = baseline
	derived, err = DeriveWithIntegrationCapabilities(base, IntegrationCapabilitiesSource{
		GitCredentials: &GitCredentialsSource{Integration: "another-app"},
	}, CompileOptions{ResolveIntegrationName: func(string) (IntegrationResolution, error) {
		t.Fatal("an explicit choice must not be resolved again")
		return IntegrationResolution{}, nil
	}})
	require.NoError(t, err)
	require.Equal(t, base.GitCredentials, derived.GitCredentials)
	derived.GitCredentials.Integration = "changed"
	require.Equal(t, "reviews", base.GitCredentials.Integration)
}

func TestGitCredentialsDerivationRejectsReusedIntegrationName(t *testing.T) {
	for _, credentialsInBase := range []bool{false, true} {
		source := "tools: {int__reviews__read: {enabled: false}}"
		addition := IntegrationCapabilitiesSource{GitCredentials: &GitCredentialsSource{Integration: "reviews"}}
		if credentialsInBase {
			source = "git_credentials: {integration: reviews}"
			addition = IntegrationCapabilitiesSource{Tools: map[string]AgentConfigToolSource{"int__reviews__read": {}}}
		}
		base := compileIntegrationTest(t, source, gitCredentialsTestOptions(t)).Compiled
		_, err := DeriveWithIntegrationCapabilities(base, addition, CompileOptions{
			ResolveIntegrationName: func(string) (IntegrationResolution, error) {
				return IntegrationResolution{
					IntegrationID: publicidTestID(161), IntegrationKind: integrationdefinition.GitHubPR,
				}, nil
			},
		})
		require.ErrorIs(t, err, ErrIntegrationCapabilityUnavailable)
	}
}

func TestGitCredentialsAreStrippedFromSubagents(t *testing.T) {
	base := compileIntegrationTest(t, "git_credentials: {integration: reviews}", gitCredentialsTestOptions(t)).Compiled
	for _, kind := range []string{SubagentTypeSelf, SubagentTypeProfile} {
		for _, model := range []*ModelCompiled{nil, {ConfiguredModelID: publicidTestID(162)}} {
			child := SubagentCompiledFrom(base, SubagentCompiled{Type: kind, Model: model}, SubagentDepth{})
			require.Nil(t, child.GitCredentials)
			require.Empty(t, ReferencedIntegrationIDs(child))
			require.NotNil(t, base.GitCredentials)
		}
	}
}
