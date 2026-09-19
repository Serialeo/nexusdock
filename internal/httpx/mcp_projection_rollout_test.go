package httpx

import (
	"errors"
	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/mcpresult"
	"testing"
)

func TestCurrentOutputProjectionPreservesInputContract(t *testing.T) {
	descriptor := platformContractDescriptor("list_dir", map[string]any{"path": map[string]any{"type": "string"}}, []any{"path"})
	descriptor.OutputSchema = map[string]any{"type": "object", "properties": map[string]any{"entries": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}}}}}}
	merged, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := cloneToolDescriptor(merged)
	if err != nil {
		t.Fatal(err)
	}
	bad.InputSchema["properties"].(map[string]any)["path"] = map[string]any{"type": "integer"}
	if _, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{descriptor, bad}); !errors.Is(err, errIncompatibleToolContract) {
		t.Fatalf("unsafe input drift accepted: %v", err)
	}
}

func TestCurrentNodeWidgetIsRelayedWithoutCompatibilityRewriting(t *testing.T) {
	html := mcpresult.WidgetHTML("task_progress", "Task")
	response, err := decodeNodeMCPAppResource(protocol.TaskProgressUIResourceURI, map[string]any{"contents": []map[string]any{{"uri": protocol.TaskProgressUIResourceURI, "mimeType": protocol.MCPAppMIMEType, "text": html}}}, "https://nexus.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if response.Contents[0].Text != html {
		t.Fatal("relay rewrote current node widget")
	}
}
