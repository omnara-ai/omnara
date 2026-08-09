package providercontract

import "testing"

func TestFirstEnvUsesFirstNonEmptyAlias(t *testing.T) {
	t.Setenv("PROVIDER_CONTRACT_FIRST", "  ")
	t.Setenv("PROVIDER_CONTRACT_SECOND", " second ")

	if got := FirstEnv(
		"PROVIDER_CONTRACT_FIRST",
		"PROVIDER_CONTRACT_SECOND",
		"PROVIDER_CONTRACT_THIRD",
	); got != "second" {
		t.Fatalf("FirstEnv() = %q, want second", got)
	}
}
