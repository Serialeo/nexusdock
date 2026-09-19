package httpx

import (
	"encoding/json"
	protocol "github.com/Serialeo/agentdock-protocol"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

func TestNodeToolBridgeEnvelopeCompactionPreservesPayloadAndError(t *testing.T) {
	server, _, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	ctx, bind := openReadyProjectTargetForMCPTest(t, server, project, deployment, socket, "mcp:compact-bridge", "compact-bridge")
	for _, failed := range []bool{false, true} {
		type callResult struct {
			result *mcpsdk.CallToolResult
			err    error
		}
		called := make(chan callResult, 1)
		go func() {
			response, err := server.callNodeTool(ctx, "read_file", map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "path": "README.md"})
			called <- callResult{response, err}
		}()
		invoke := readProjectInvoke(t, socket)
		if invoke.Operation != protocol.OperationToolCall {
			t.Fatalf("unexpected operation: %s", invoke.Operation)
		}
		text := strings.Repeat("bridge-body-marker\n", 1024)
		payload := map[string]any{"path": "README.md", "content": text, "encoding": "utf-8", "size_bytes": len(text), "truncated": false}
		if failed {
			payload = map[string]any{"code": "FILE_NOT_FOUND", "error": "missing README.md", "details": map[string]any{"path": "README.md"}}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		envelope := map[string]any{"isError": failed, "structuredContent": payload, "content": []map[string]any{{"type": "text", "text": string(encoded)}}}
		writeProjectResult(t, socket, invoke.RequestID, envelope)
		got := <-called
		if got.err != nil || got.result == nil {
			t.Fatalf("gateway call failed: %v", got.err)
		}
		if got.result.IsError != failed {
			t.Fatal("Bridge changed isError")
		}
		body := got.result.StructuredContent.(map[string]any)
		if failed {
			if body["code"] != "FILE_NOT_FOUND" {
				t.Fatalf("Bridge swallowed error: %#v", body)
			}
		} else {
			if body["content"] != text {
				t.Fatal("Bridge swallowed file content")
			}
			if body["encoding"] != nil || body["size_bytes"] != nil || body["truncated"] != nil {
				t.Fatal("Bridge bypassed compaction")
			}
			encoded, err := json.Marshal(got.result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(encoded), "bridge-body-marker") != 1024 {
				t.Fatal("Bridge duplicated file body")
			}
		}
	}
}
