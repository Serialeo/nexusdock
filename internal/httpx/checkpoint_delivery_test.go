package httpx

import (
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/mcpresult"
)

func TestCheckpointPolicySurvivesTaskBridgeDelivery(t *testing.T) {
	descriptor := protocol.ToolDescriptor{
		Name: "task_manage", Title: "Tasks", Description: "Manage tasks",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}, "task_id": map[string]any{"type": "string"}}, "required": []string{"action"}, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"task_id": map[string]any{"type": "string"}, "checkpoint_policy": map[string]any{"type": "object"}}, "additionalProperties": false},
	}
	server, _, project, deployment, socket, closeNode := prepareOnlineProjectMCPTestWithTools(t, []protocol.ToolDescriptor{descriptor})
	defer closeNode()
	ctx, bind := openReadyProjectTargetForMCPTest(t, server, project, deployment, socket, "mcp:checkpoint-policy", "checkpoint-policy")
	policy := map[string]any{"source": "nexusdock/checkpoint", "version": "rev-7", "enforcement": "caller_driven", "prompt": "checkpoint-prompt-marker: record evidence and the next action", "rules": []string{"task_id and summary are required"}}
	for _, action := range []string{"create", "get", "resume"} {
		called := make(chan projectMCPCall, 1)
		go func() {
			result, err := server.callNodeTool(ctx, "task_manage", map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "action": action, "task_id": "tsk_checkpoint"})
			called <- projectMCPCall{result, err}
		}()
		invoke := readProjectInvoke(t, socket)
		if invoke.Operation != protocol.OperationToolCall {
			t.Fatalf("operation=%s", invoke.Operation)
		}
		// 模拟 AgentDock 已经做过一轮 MCP 投影的返回，再经过 Nexus Bridge。
		upstream, err := mcpresult.Build("task_manage", map[string]any{"task_id": "tsk_checkpoint", "checkpoint_policy": policy}, false)
		if err != nil {
			t.Fatal(err)
		}
		writeProjectResult(t, socket, invoke.RequestID, upstream)
		got := <-called
		if got.err != nil || got.result == nil || got.result.IsError {
			t.Fatalf("%s: result=%#v err=%v", action, got.result, got.err)
		}
		body := got.result.StructuredContent.(map[string]any)
		delivered, ok := body["checkpoint_policy"].(map[string]any)
		if !ok || delivered["prompt"] != policy["prompt"] || delivered["version"] != "rev-7" {
			t.Fatalf("%s lost checkpoint prompt: %#v", action, body)
		}
	}
	tool := nodeMCPToolWithApps(descriptor, false)
	if tool == nil {
		t.Fatal("task tool is unavailable")
	}
	if _, ok := tool.OutputSchema.(map[string]any)["properties"].(map[string]any)["checkpoint_policy"]; !ok {
		t.Fatal("published output schema drops checkpoint policy")
	}
}
