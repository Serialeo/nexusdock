package httpx

import (
	"errors"
	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/Serialeo/agentdock-protocol/mcpapps"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/mcpresult"
	"strings"
	"testing"
)

func TestProjectedListOutputAllowsRollingProvidersWithoutRelaxingInput(t *testing.T) {
	old := platformContractDescriptor("list_dir", map[string]any{"path": map[string]any{"type": "string"}}, []any{"path"})
	old.OutputSchema = map[string]any{"type": "object", "additionalProperties": true, "required": []any{"path", "entries", "partial", "truncated", "skipped_paths"}, "properties": map[string]any{
		"path": map[string]any{"type": "string"}, "partial": map[string]any{"type": "boolean"}, "truncated": map[string]any{"type": "boolean"}, "skipped_paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"entries": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path", "type", "name", "size_bytes", "modified", "is_hidden"}, "properties": map[string]any{
			"path": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "size_bytes": map[string]any{"type": "integer"}, "modified": map[string]any{"type": "string"}, "is_hidden": map[string]any{"type": "boolean"},
		}}},
	}}
	newer, err := cloneToolDescriptor(old)
	if err != nil {
		t.Fatal(err)
	}
	newer.OutputSchema = mcpresult.Schema("list_dir", newer.OutputSchema)
	merged, accepted, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{old, newer})
	if err != nil {
		t.Fatal(err)
	}
	oldHash, _ := toolContractHash(old)
	newHash, _ := toolContractHash(newer)
	if oldHash == newHash || !containsToolContractHash(accepted, oldHash) || !containsToolContractHash(accepted, newHash) {
		t.Fatal("lost raw provider hash identity")
	}
	props := merged.OutputSchema["properties"].(map[string]any)
	entry := props["entries"].(map[string]any)["items"].(map[string]any)
	if entry["properties"].(map[string]any)["modified"] != nil {
		t.Fatal("Fleet republished retired telemetry")
	}
	bad, err := cloneToolDescriptor(newer)
	if err != nil {
		t.Fatal(err)
	}
	bad.InputSchema["properties"].(map[string]any)["path"] = map[string]any{"type": "integer"}
	if _, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{old, bad}); !errors.Is(err, errIncompatibleToolContract) {
		t.Fatalf("unsafe input drift accepted: %v", err)
	}
}

func TestOldNodeWidgetIsAdaptedOnlyAtRelayPresentationBoundary(t *testing.T) {
	html := mcpapps.HTML("task_progress", "Task")
	response, err := decodeNodeMCPAppResource(protocol.TaskProgressUIResourceURI, map[string]any{"contents": []map[string]any{{"uri": protocol.TaskProgressUIResourceURI, "mimeType": protocol.MCPAppMIMEType, "text": html}}}, "https://nexus.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Contents[0].Text, "render(compactViewData(data,message.params))") {
		t.Fatal("old node widget cannot render compact model result")
	}
	if strings.Count(response.Contents[0].Text, "function compactViewData(") != 1 {
		t.Fatal("widget was adapted more than once")
	}
}
