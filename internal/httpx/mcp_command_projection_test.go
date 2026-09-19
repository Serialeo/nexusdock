package httpx

import (
	"encoding/json"
	"errors"
	"github.com/uvwt/nexusdock/internal/mcpresult"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCompactNodeExecCommandResultDropsRoutingTelemetryAndZeroValues(t *testing.T) {
	raw := map[string]any{
		"command_ok":           true,
		"deployment_id":        "deployment-a",
		"elapsed_ms":           55,
		"event_id":             "event-a",
		"exit_code":            0,
		"node_id":              "node-a",
		"outcome_state":        "completed",
		"project_id":           "project-a",
		"sandbox":              map[string]any{"enabled": false},
		"session_id":           "session-a",
		"status":               "exited",
		"stderr":               "",
		"stderr_dropped_bytes": 0,
		"stderr_omitted_bytes": 0,
		"stderr_output_bytes":  0,
		"stderr_total_bytes":   0,
		"stderr_truncated":     false,
		"stdout":               "result",
		"stdout_dropped_bytes": 0,
		"stdout_omitted_bytes": 0,
		"stdout_output_bytes":  6,
		"stdout_total_bytes":   6,
		"stdout_truncated":     false,
		"target_id":            "target-a",
		"timed_out":            false,
		"work_session_id":      "ws-a",
		"workdir":              "/tmp/project",
	}
	want := map[string]any{"exit_code": 0, "stdout": "result"}
	if got := mcpresult.Project("exec_command", raw); !projectionJSONEqual(got, want) {
		t.Fatalf("compactNodeExecCommandResult() = %#v, want %#v", got, want)
	}
}

func TestCompactNodeExecCommandResultKeepsAsyncContinuation(t *testing.T) {
	raw := map[string]any{
		"session_id":       "session-a",
		"status":           "running",
		"session_reason":   "foreground_threshold_exceeded",
		"observe_after_ms": 1000,
		"elapsed_ms":       5001,
		"event_id":         "event-a",
	}
	want := map[string]any{
		"session_id": "session-a",
		"status":     "running",
	}
	if got := mcpresult.Project("exec_command", raw); !projectionJSONEqual(got, want) {
		t.Fatalf("compactNodeExecCommandResult() = %#v, want %#v", got, want)
	}
}

func TestCompactNodeCommandOutputUsesConditionalFailureFields(t *testing.T) {
	timeout := map[string]any{
		"timed_out":     true,
		"exit_code":     -1,
		"command_error": "signal: killed",
	}
	if got := mcpresult.Project("session_observe", timeout); !reflect.DeepEqual(got, map[string]any{"timed_out": true}) {
		t.Fatalf("timeout projection = %#v", got)
	}

	truncated := map[string]any{
		"stdout":               "tail",
		"stdout_truncated":     true,
		"stdout_total_bytes":   120000,
		"stdout_output_bytes":  4,
		"stdout_omitted_bytes": 119996,
	}
	want := map[string]any{"stdout": "tail", "stdout_truncated": true, "stdout_total_bytes": 120000}
	if got := mcpresult.Project("session_observe", truncated); !projectionJSONEqual(got, want) {
		t.Fatalf("truncated projection = %#v, want %#v", got, want)
	}
}

func TestCompactNodeSessionCollectionDropsCountAndIdentity(t *testing.T) {
	raw := map[string]any{
		"count": 1,
		"sessions": []any{map[string]any{
			"session_id":      "session-a",
			"status":          "running",
			"elapsed_ms":      123,
			"target_id":       "target-a",
			"deployment_id":   "deployment-a",
			"work_session_id": "ws-a",
			"workdir":         "/tmp/project",
		}},
	}
	want := map[string]any{"sessions": []map[string]any{{"session_id": "session-a", "status": "running"}}}
	if got := mcpresult.Project("session_observe", raw); !projectionJSONEqual(got, want) {
		t.Fatalf("compactNodeSessionCollection() = %#v, want %#v", got, want)
	}
}

func TestNodeOutputSchemaHidesCommandInternals(t *testing.T) {
	schema := nodeOutputSchema("exec_command", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command_ok":    map[string]any{"type": "boolean"},
			"deployment_id": map[string]any{"type": "string"},
			"sandbox":       map[string]any{"type": "object"},
			"stdout":        map[string]any{"type": "string"},
		},
		"additionalProperties": true,
	})
	if schema["additionalProperties"] != true {
		t.Fatalf("gateway command schema must preserve provider extensibility: %#v", schema)
	}
	props := schema["properties"].(map[string]any)
	for _, key := range []string{"command_ok", "deployment_id", "sandbox", "elapsed_ms", "workdir"} {
		if _, ok := props[key]; ok {
			t.Fatalf("gateway command schema still exposes %s: %#v", key, props)
		}
	}
	for _, key := range []string{"stdout", "stdout_truncated", "stderr_truncated"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("gateway command schema missing %s: %#v", key, props)
		}
	}
}

func TestGatewayToolResultDoesNotDuplicateCommandStdoutInTextContent(t *testing.T) {
	stdout := strings.Repeat("large-output-line\n", 4096)
	response, err := gatewayToolResult("exec_command", map[string]any{
		"exit_code": 0,
		"stdout":    stdout,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, ok := response.Content[0].(*mcpsdk.TextContent)
	if !ok || content.Text != "command exited: 0" {
		t.Fatalf("gateway text content = %#v, want compact command status", response.Content)
	}
	structured, ok := response.StructuredContent.(map[string]any)
	if !ok || structured["stdout"] != stdout {
		t.Fatalf("gateway structured content = %#v", response.StructuredContent)
	}
}

func TestGatewayToolResultKeepsCommandInfrastructureErrorInTextContent(t *testing.T) {
	response, err := gatewayToolResult("exec_command", map[string]any{"code": "NODE_OFFLINE"}, errors.New("node is offline"))
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsError {
		t.Fatal("gateway infrastructure error was not marked as an error")
	}
	content, ok := response.Content[0].(*mcpsdk.TextContent)
	if !ok || content.Text != "node is offline" {
		t.Fatalf("gateway error text content = %#v", response.Content)
	}
}

func projectionJSONEqual(a, b any) bool {
	x, ex := json.Marshal(a)
	y, ey := json.Marshal(b)
	return ex == nil && ey == nil && string(x) == string(y)
}
