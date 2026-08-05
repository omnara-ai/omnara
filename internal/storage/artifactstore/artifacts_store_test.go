package artifactstore

import "testing"

func TestArtifactObjectKeyUsesAgentAndArtifactID(t *testing.T) {
	agentID := parseUUIDText("018ffc6b-7f1a-7828-8687-93aa210f5f4a")
	artifactID := parseUUIDText("018ffc6b-7f1a-7c16-8a7a-973be2463b7d")

	if got, want := artifactObjectKey(
		agentID,
		artifactID,
	), "artifacts/018ffc6b-7f1a-7828-8687-93aa210f5f4a/018ffc6b-7f1a-7c16-8a7a-973be2463b7d"; got != want {
		t.Fatalf("artifact object key = %q, want %q", got, want)
	}
}
