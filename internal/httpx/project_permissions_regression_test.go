package httpx

import (
	"encoding/json"
	protocol "github.com/Serialeo/agentdock-protocol"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/agentdock"
	projectstore "github.com/uvwt/nexusdock/internal/project"
	"testing"
	"time"
)

func TestBrowserCloseIsHistoricalCleanup(t *testing.T) {
	if !allowsHistoricalTargetControl("browser_session", map[string]any{"action": "close"}) {
		t.Fatal("browser close is not admitted as historical cleanup, unlike command/ACP close")
	}
}

func TestAppliedFullAccessMatchesTargetPermissions(t *testing.T) {
	server, _, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_permission_review_12345678", "Review")
	fullAccess := true
	var err error
	node, err = nodes.Update(t.Context(), node.ID, agentdock.UpdateInput{FullAccess: &fullAccess})
	if err != nil {
		t.Fatal(err)
	}
	project, err := projects.CreateProject(t.Context(), projectstore.CreateProjectInput{Name: "Review", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	configured := protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly}
	_, err = projects.CreateDeployment(t.Context(), projectstore.CreateDeploymentInput{ProjectID: project.ID, NodeID: node.ID, Permissions: configured, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("operation=%s", invoke.Operation)
	}
	var applied protocol.Deployment
	if err := json.Unmarshal(invoke.Arguments, &applied); err != nil {
		t.Fatal(err)
	}
	writeProjectResult(t, socket, invoke.RequestID, map[string]any{"deployment": applied})
	want := effectiveProjectPermissions(configured, true)
	if applied.Permissions != want {
		t.Fatalf("node receives raw fallback flags although target advertises effective flags: applied=%#v target=%#v", applied.Permissions, want)
	}
}

func TestRevokedBrowserTargetCloseReachesNodeButStartDoesNot(t *testing.T) {
	tool := protocol.ToolDescriptor{Name: "browser_session", Title: "Browser sessions", Description: "test browser session control",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"action":     map[string]any{"type": "string", "enum": []any{"close", "start"}},
			"session_id": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}}, "required": []any{"action"}},
		OutputSchema: map[string]any{"type": "object", "additionalProperties": true},
	}
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTestWithTools(t, []protocol.ToolDescriptor{tool})
	defer closeNode()
	ctx, bind := openReadyProjectTargetForMCPTest(t, server, project, deployment, socket, "mcp:browser-cleanup-owner", "browser-cleanup-open")
	if _, err := projects.RevokeTargetsForDeployment(t.Context(), deployment.ID, "permission removed"); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "action": "close", "session_id": "owned-browser"}
	other := withMCPClientBinding(t.Context(), "mcp:other-browser-owner")
	denied, err := server.callNodeTool(other, "browser_session", args)
	if err != nil || !denied.IsError || denied.StructuredContent.(map[string]any)["code"] != protocol.ErrorSessionTargetDenied {
		t.Fatalf("different owner reached browser close: %#v %v", denied, err)
	}
	type outcome struct {
		result *mcpsdk.CallToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { r, e := server.callNodeTool(ctx, "browser_session", args); done <- outcome{r, e} }()
	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationToolCall || invoke.ExecutionContext == nil || invoke.ExecutionContext.TargetID != bind.TargetID {
		t.Fatalf("close lost bound ownership: %#v", invoke)
	}
	var call protocol.ToolCallRequest
	if err := json.Unmarshal(invoke.Arguments, &call); err != nil {
		t.Fatal(err)
	}
	if call.Tool != "browser_session" || call.Arguments["action"] != "close" || call.Arguments["session_id"] != "owned-browser" {
		t.Fatalf("wrong close forwarded: %#v", call)
	}
	writeProjectResult(t, socket, invoke.RequestID, map[string]any{"browser_ok": true, "closed": true})
	result := <-done
	if result.err != nil || result.result == nil || result.result.IsError {
		t.Fatalf("owner close denied: %#v %v", result.result, result.err)
	}
	denied, err = server.callNodeTool(ctx, "browser_session", map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "action": "start", "url": "about:blank"})
	if err != nil || !denied.IsError || denied.StructuredContent.(map[string]any)["code"] != protocol.ErrorSessionTargetDenied {
		t.Fatalf("revoked target started a browser: %#v %v", denied, err)
	}
	_ = socket.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	var unexpected protocol.Message
	if err := socket.ReadJSON(&unexpected); err == nil {
		t.Fatalf("denied start reached node: %#v", unexpected)
	}
}
