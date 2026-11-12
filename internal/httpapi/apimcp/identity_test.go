package apimcp

import (
	"reflect"
	"strings"
	"testing"
)

func TestInitializeAdvertisesServerIdentity(t *testing.T) {
	session := connect(t, nil, echoOptions(t))
	result := session.InitializeResult()
	if result == nil || result.ServerInfo == nil {
		t.Fatal("missing initialize result")
	}
	want := serverImplementation(specVersion(loadSpec(t)))
	if !reflect.DeepEqual(result.ServerInfo, want) {
		t.Fatalf("serverInfo = %+v, want %+v", result.ServerInfo, want)
	}
	if want.Version == "" || want.Title == "" || want.Description == "" || want.WebsiteURL == "" {
		t.Fatalf("incomplete identity %+v", want)
	}
	if icon := want.Icons[0]; icon.Source == "" || icon.MIMEType != "image/png" || icon.Sizes[0] != "180x180" {
		t.Fatalf("icon = %+v", icon)
	}
	if result.Instructions != ServerInstructions {
		t.Fatalf("instructions = %q", result.Instructions)
	}
}

// The instructions name tools by hand; fail if one is renamed or removed.
func TestServerInstructionsReferenceRealTools(t *testing.T) {
	known := make(map[string]bool, len(Tools))
	for _, tool := range Tools {
		known[tool.Name] = true
	}
	referenced := []string{
		"whoami", "orgs_list", "projects_list", "models_list", "grant_models_add",
		"grant_pools_add", "grant_machines_add", "profiles_list", "configs_create", "agents_launch",
		"agents_events_list", "agents_input", "agents_interactions_list", "agents_interactions_resolve",
	}
	for _, name := range referenced {
		if !known[name] {
			t.Errorf("instructions reference unknown tool %q", name)
		}
		if !strings.Contains(ServerInstructions, name) {
			t.Errorf("referenced list is stale: instructions no longer mention %q", name)
		}
	}
}
