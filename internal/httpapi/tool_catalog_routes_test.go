package httpapi

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestToolCatalogMarksBindingManagedToolsNonConfigurable(t *testing.T) {
	response, err := (strictOpenAPIServer{}).GetToolCatalog(
		context.Background(),
		openapi.GetToolCatalogRequestObject{},
	)
	if err != nil {
		t.Fatalf("get tool catalog: %v", err)
	}
	body, ok := response.(openapi.GetToolCatalog200JSONResponse)
	if !ok {
		t.Fatalf("tool catalog response = %T", response)
	}
	configurable := make(map[string]bool, len(body.BuiltInTools))
	for _, entry := range body.BuiltInTools {
		if entry.Configurable == nil {
			t.Fatalf("tool %q omitted configurable", entry.Name)
		}
		configurable[entry.Name] = *entry.Configurable
	}
	for _, name := range []string{
		toolcatalog.ToolNameListChannels,
		toolcatalog.ToolNameSendChannelMessage,
	} {
		value, exists := configurable[name]
		if !exists || value {
			t.Fatalf("binding-managed tool %q present=%t configurable=%t", name, exists, value)
		}
	}
	if !configurable[toolcatalog.ToolNameRunCommand] {
		t.Fatal("ordinary built-in tool is not configurable")
	}
}
