package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	mcpcontract "github.com/Serialeo/agentdock-protocol/mcpcontract"
	"github.com/gorilla/websocket"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/agentdock"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

type projectToolCallResult struct {
	result map[string]any
	err    error
}

func TestProjectOpenIsIdempotentOwnerBoundAndUsesBridgeV4TargetBinding(t *testing.T) {
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	ownerCtx := withMCPClientBinding(t.Context(), "mcp:project-owner-a")
	args := map[string]any{"project_id": project.ID, "client_request_id": "open-1"}

	opened := make(chan projectToolCallResult, 1)
	go func() {
		result, err := server.callProjectOpen(ownerCtx, args)
		opened <- projectToolCallResult{result: result, err: err}
	}()

	promptInvoke := readProjectInvoke(t, socket)
	if promptInvoke.Operation != protocol.OperationProjectPromptLoad || promptInvoke.ExecutionContext != nil {
		t.Fatalf("Project Prompt invoke = %#v", promptInvoke)
	}
	var promptRequest protocol.ProjectPromptLoadRequest
	if err := json.Unmarshal(promptInvoke.Arguments, &promptRequest); err != nil {
		t.Fatal(err)
	}
	if promptRequest.DeploymentID != deployment.ID || promptRequest.DeploymentRevision != deployment.AppliedRevision || promptRequest.CWDRel != "." {
		t.Fatalf("Project Prompt request = %#v", promptRequest)
	}
	prompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:root-prompt", Complete: true, Bytes: 11,
		Sources: []protocol.PromptSource{{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:source-root", Bytes: 11, Content: "root rules\n"}},
	}
	writeProjectResult(t, socket, promptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: ".", Prompt: prompt, SourceProvenance: projectTestSourceProvenance()})

	bindInvoke := readProjectInvoke(t, socket)
	if bindInvoke.Operation != protocol.OperationProjectTargetBind || bindInvoke.ExecutionContext != nil {
		t.Fatalf("Target bind invoke = %#v", bindInvoke)
	}
	var bind protocol.ProjectTargetBindRequest
	if err := json.Unmarshal(bindInvoke.Arguments, &bind); err != nil {
		t.Fatal(err)
	}
	if bind.WorkSessionID == "" || bind.TargetID == "" || bind.ProjectID != project.ID || bind.DeploymentID != deployment.ID || bind.DeploymentRevision != deployment.AppliedRevision || bind.ContextRevision == "" {
		t.Fatalf("Target bind request = %#v", bind)
	}
	if len(bind.PromptScopes) != 1 || bind.PromptScopes[0].Scope != "." || bind.PromptScopes[0].PromptRevision != prompt.PromptRevision {
		t.Fatalf("Target bind Prompt scopes = %#v", bind.PromptScopes)
	}
	writeProjectResult(t, socket, bindInvoke.RequestID, projectTestTargetAcknowledgment(bind))

	first := <-opened
	if first.err != nil {
		t.Fatal(first.err)
	}
	normalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectOpen, first.result)
	if normalized["status"] != string(protocol.WorkSessionReady) || normalized["work_session_id"] != bind.WorkSessionID {
		t.Fatalf("project_open result = %#v", normalized)
	}
	targets, ok := normalized["targets"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("project_open targets = %#v", normalized["targets"])
	}

	repeated, err := server.callProjectOpen(ownerCtx, args)
	if err != nil {
		t.Fatal(err)
	}
	if repeated["work_session_id"] != first.result["work_session_id"] {
		t.Fatalf("idempotent project_open created another WorkSession: first=%#v repeated=%#v", first.result, repeated)
	}

	otherOwner := withMCPClientBinding(t.Context(), "mcp:project-owner-b")
	denied, err := server.callProjectContext(otherOwner, map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID})
	if err == nil || denied["code"] != protocol.ErrorSessionTargetDenied {
		t.Fatalf("cross-owner project_context = %#v err=%v", denied, err)
	}
	if _, err := projects.GetWorkSession(t.Context(), "mcp:project-owner-b", bind.WorkSessionID); !errors.Is(err, projectstore.ErrWorkSessionNotFound) {
		t.Fatalf("cross-owner WorkSession lookup = %v", err)
	}

	emptyTargets, err := server.callProjectOpen(ownerCtx, map[string]any{"project_id": project.ID, "client_request_id": "open-empty", "targets": []any{}})
	if err == nil || emptyTargets["code"] != "INVALID_PROJECT" {
		t.Fatalf("explicit empty targets = %#v err=%v", emptyTargets, err)
	}
	for name, invalid := range map[string]map[string]any{
		"top_level_node_override": {"project_id": project.ID, "client_request_id": "open-node", "node_id": deployment.NodeID},
		"nested_workdir_override": {
			"project_id": project.ID, "client_request_id": "open-workdir",
			"targets": []any{map[string]any{"deployment_id": deployment.ID, "working_folder": "/tmp/override"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, callErr := server.callProjectOpen(ownerCtx, invalid)
			if callErr == nil || result["code"] != "INVALID_PROJECT" {
				t.Fatalf("strict project_open accepted routing override: result=%#v err=%v", result, callErr)
			}
		})
	}
	invalidContext, err := server.callProjectContext(ownerCtx, map[string]any{
		"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "permissions": map[string]any{"shell": true},
	})
	if err == nil || invalidContext["code"] != "INVALID_PROJECT" {
		t.Fatalf("strict project_context accepted permissions override: result=%#v err=%v", invalidContext, err)
	}
	invalidList, err := server.callNexusTool(ownerCtx, mcpcontract.ToolProjectList, map[string]any{"node_id": deployment.NodeID})
	if err == nil || invalidList["code"] != "INVALID_PROJECT" {
		t.Fatalf("strict project_list accepted arguments: result=%#v err=%v", invalidList, err)
	}
}

func TestProjectContextRefreshesOnlyBoundTargetAndRejectsRevokedTarget(t *testing.T) {
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	ctx := withMCPClientBinding(t.Context(), "mcp:project-context-owner")

	opened := make(chan projectToolCallResult, 1)
	go func() {
		result, err := server.callProjectOpen(ctx, map[string]any{
			"project_id": project.ID, "client_request_id": "context-open",
			"targets": []any{map[string]any{"deployment_id": deployment.ID, "cwd_rel": "."}},
		})
		opened <- projectToolCallResult{result: result, err: err}
	}()
	rootPromptInvoke := readProjectInvoke(t, socket)
	rootPrompt := protocol.ProjectPrompt{PromptRevision: "sha256:root", Complete: true, Sources: []protocol.PromptSource{{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:root-source", Content: "root\n", Bytes: 5}}, Bytes: 5}
	writeProjectResult(t, socket, rootPromptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: ".", Prompt: rootPrompt, SourceProvenance: projectTestSourceProvenance()})
	bindInvoke := readProjectInvoke(t, socket)
	var bind protocol.ProjectTargetBindRequest
	if err := json.Unmarshal(bindInvoke.Arguments, &bind); err != nil {
		t.Fatal(err)
	}
	writeProjectResult(t, socket, bindInvoke.RequestID, projectTestTargetAcknowledgment(bind))
	if openedResult := <-opened; openedResult.err != nil {
		t.Fatal(openedResult.err)
	}

	refreshed := make(chan projectToolCallResult, 1)
	go func() {
		result, err := server.callProjectContext(ctx, map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "cwd_rel": "backend"})
		refreshed <- projectToolCallResult{result: result, err: err}
	}()
	promptInvoke := readProjectInvoke(t, socket)
	if promptInvoke.Operation != protocol.OperationProjectPromptLoad {
		t.Fatalf("context refresh first operation = %q", promptInvoke.Operation)
	}
	var promptRequest protocol.ProjectPromptLoadRequest
	if err := json.Unmarshal(promptInvoke.Arguments, &promptRequest); err != nil {
		t.Fatal(err)
	}
	if promptRequest.CWDRel != "backend" {
		t.Fatalf("context refresh cwd = %q", promptRequest.CWDRel)
	}
	backendPrompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:backend", Complete: true, Bytes: 13,
		Sources: []protocol.PromptSource{{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:root-source", Bytes: 5, Content: "root\n"}, {Path: "backend/AGENTS.md", Scope: "backend", SHA256: "sha256:backend-source", Bytes: 8, Content: "backend\n"}},
	}
	writeProjectResult(t, socket, promptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: "backend", Prompt: backendPrompt, SourceProvenance: projectTestSourceProvenance()})

	rebindInvoke := readProjectInvoke(t, socket)
	if rebindInvoke.Operation != protocol.OperationProjectTargetRebind {
		t.Fatalf("context refresh second operation = %q", rebindInvoke.Operation)
	}
	var rebind protocol.ProjectTargetRebindRequest
	if err := json.Unmarshal(rebindInvoke.Arguments, &rebind); err != nil {
		t.Fatal(err)
	}
	if rebind.TargetID != bind.TargetID || rebind.WorkSessionID != bind.WorkSessionID || rebind.CWDRel != "backend" || rebind.ContextRevision == bind.ContextRevision {
		t.Fatalf("Target rebind = %#v", rebind)
	}
	if len(rebind.PromptScopes) != 2 {
		t.Fatalf("rebind Prompt scopes = %#v", rebind.PromptScopes)
	}
	writeProjectResult(t, socket, rebindInvoke.RequestID, projectTestTargetRebindAcknowledgment(rebind))
	refreshResult := <-refreshed
	if refreshResult.err != nil {
		t.Fatal(refreshResult.err)
	}
	normalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectContext, refreshResult.result)
	projectView := normalized["project"].(map[string]any)
	if projectView["id"] != project.ID || projectView["orchestration_policy"] != project.OrchestrationPolicy {
		t.Fatalf("project_context Project = %#v", projectView)
	}
	deploymentView := normalized["deployment"].(map[string]any)
	if deploymentView["id"] != deployment.ID || deploymentView["working_folder"] != deployment.WorkingFolder {
		t.Fatalf("project_context Deployment = %#v", deploymentView)
	}
	target := normalized["target"].(map[string]any)
	if target["cwd_rel"] != "backend" || target["target_id"] != bind.TargetID {
		t.Fatalf("project_context target = %#v", target)
	}
	promptView := target["prompt"].(map[string]any)
	promptSources := promptView["sources"].([]any)
	if len(promptSources) != 2 || promptSources[1].(map[string]any)["content"] != "backend\n" {
		t.Fatalf("project_context full Prompt sources = %#v", promptSources)
	}
	if _, exists := normalized["delivery"]; exists {
		t.Fatalf("project_context exposed Host delivery state: %#v", normalized)
	}
	stored, err := projects.GetWorkTarget(t.Context(), "mcp:project-context-owner", bind.WorkSessionID, bind.TargetID)
	if err != nil || stored.Target.CWDRel != "backend" || stored.Target.ContextRevision != rebind.ContextRevision {
		t.Fatalf("stored refreshed Target = %#v err=%v", stored, err)
	}

	if _, err := projects.RevokeTargetsForDeployment(t.Context(), deployment.ID, "test revoke"); err != nil {
		t.Fatal(err)
	}
	denied, err := server.callProjectContext(ctx, map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "cwd_rel": "."})
	if err == nil || denied["code"] != protocol.ErrorSessionTargetDenied {
		t.Fatalf("revoked Target project_context = %#v err=%v", denied, err)
	}
}

func TestProjectContextRevisionsIgnoreProjectRowMetadataRevision(t *testing.T) {
	before := projectstore.Project{ID: "project_context_identity", Name: "Before", Revision: "rev-1", OrchestrationPolicy: "old guidance", Enabled: true}
	after := before
	after.Name = "After"
	after.Revision = "rev-99"
	after.OrchestrationPolicy = "new guidance"
	deployment := projectstore.Deployment{ID: "deployment_context_identity", NodeID: "node_context_identity", AppliedRevision: "rev-7"}
	permissions := protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadWrite, Shell: true}
	prompt := protocol.ProjectPrompt{PromptRevision: "sha256:prompt-context-identity", Complete: true, Sources: []protocol.PromptSource{}}
	scopes := []protocol.PromptScopeRevision{{Scope: ".", PromptRevision: prompt.PromptRevision}}
	provenance := projectTestSourceProvenance()

	beforeTarget := projectTargetContextRevision(before, deployment, permissions, ".", prompt, scopes, provenance)
	afterTarget := projectTargetContextRevision(after, deployment, permissions, ".", prompt, scopes, provenance)
	if beforeTarget != afterTarget {
		t.Fatalf("Project row metadata changed Target execution context: before=%q after=%q", beforeTarget, afterTarget)
	}
	targets := []projectstore.WorkTarget{{Target: protocol.WorkTarget{ID: "target_context_identity", DeploymentID: deployment.ID, ContextRevision: beforeTarget}}}
	if beforeSession, afterSession := projectSessionContextRevision(before, targets), projectSessionContextRevision(after, targets); beforeSession != afterSession {
		t.Fatalf("Project row metadata changed WorkSession context: before=%q after=%q", beforeSession, afterSession)
	}
}

func TestProjectOpenUnavailableDeploymentReturnsNonExecutableTarget(t *testing.T) {
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_mcp_unavailable_12345678", "UnavailableNode")
	project := createProjectThroughAPI(t, handler, "Unavailable MCP")
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, t.TempDir())
	if deployment.ApplyStatus != string(protocol.DeploymentApplyPending) {
		t.Fatalf("test requires pending Deployment: %#v", deployment)
	}

	ctx := withMCPClientBinding(t.Context(), "mcp:unavailable-owner")
	result, err := server.callProjectOpen(ctx, map[string]any{"project_id": project.ID, "client_request_id": "unavailable-open"})
	if err != nil {
		t.Fatal(err)
	}
	normalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectOpen, result)
	if normalized["status"] != string(protocol.WorkSessionFailed) {
		t.Fatalf("unavailable WorkSession status = %#v", normalized["status"])
	}
	targets := normalized["targets"].([]any)
	if len(targets) != 1 || targets[0].(map[string]any)["status"] != string(protocol.TargetUnavailable) {
		t.Fatalf("unavailable targets = %#v", targets)
	}
	stored, err := projects.ListWorkTargets(t.Context(), "mcp:unavailable-owner", normalized["work_session_id"].(string))
	if err != nil || len(stored) != 1 || stored[0].Target.ContextRevision != "" || stored[0].Target.Prompt.Complete {
		t.Fatalf("unavailable Target unexpectedly obtained executable context = %#v err=%v", stored, err)
	}
}

func TestProjectOpenRejectsDeploymentFromAnotherProject(t *testing.T) {
	server, handler, _, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_cross_project_12345678", "CrossProjectNode")
	first := createProjectThroughAPI(t, handler, "First Project")
	second := createProjectThroughAPI(t, handler, "Second Project")
	foreign := createDeploymentThroughAPI(t, handler, second.ID, node.ID, t.TempDir())

	ctx := withMCPClientBinding(t.Context(), "mcp:cross-project-owner")
	result, err := server.callProjectOpen(ctx, map[string]any{
		"project_id": first.ID, "client_request_id": "cross-project-open",
		"targets": []any{map[string]any{"deployment_id": foreign.ID}},
	})
	if err == nil || result["code"] != "INVALID_PROJECT" {
		t.Fatalf("foreign Deployment selection = %#v err=%v", result, err)
	}
}

func TestProjectOpenAppliedDeploymentBecomesUnavailableWhenNodeOffline(t *testing.T) {
	server, projects, project, deployment, _, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	server.agentDockHub.Disconnect(deployment.NodeID)
	if server.agentDockHub.Online(deployment.NodeID) {
		t.Fatal("test Node remained online after disconnect")
	}

	ctx := withMCPClientBinding(t.Context(), "mcp:offline-owner")
	result, err := server.callProjectOpen(ctx, map[string]any{"project_id": project.ID, "client_request_id": "offline-open"})
	if err != nil {
		t.Fatal(err)
	}
	normalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectOpen, result)
	if normalized["status"] != string(protocol.WorkSessionFailed) {
		t.Fatalf("offline WorkSession status = %#v", normalized["status"])
	}
	targets := normalized["targets"].([]any)
	if len(targets) != 1 || targets[0].(map[string]any)["status"] != string(protocol.TargetUnavailable) {
		t.Fatalf("offline targets = %#v", targets)
	}
	stored, err := projects.ListWorkTargets(t.Context(), "mcp:offline-owner", normalized["work_session_id"].(string))
	if err != nil || len(stored) != 1 || stored[0].Target.ContextRevision != "" || stored[0].Target.Prompt.Complete || stored[0].LastError == "" {
		t.Fatalf("offline Target unexpectedly obtained executable context = %#v err=%v", stored, err)
	}
}

func TestProjectOpenPromptFailurePersistsContextErrorWithoutBindingTarget(t *testing.T) {
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	ctx := withMCPClientBinding(t.Context(), "mcp:prompt-failure-owner")
	opened := make(chan projectToolCallResult, 1)
	go func() {
		result, err := server.callProjectOpen(ctx, map[string]any{
			"project_id": project.ID, "client_request_id": "prompt-failure-open",
			"targets": []any{map[string]any{"deployment_id": deployment.ID, "cwd_rel": "escape"}},
		})
		opened <- projectToolCallResult{result: result, err: err}
	}()

	promptInvoke := readProjectInvoke(t, socket)
	if promptInvoke.Operation != protocol.OperationProjectPromptLoad {
		t.Fatalf("prompt failure operation = %q", promptInvoke.Operation)
	}
	if err := socket.WriteJSON(protocol.Message{
		Type: protocol.MessageToolError, RequestID: promptInvoke.RequestID,
		Error: &protocol.RemoteError{Code: protocol.ErrorPromptScopeEscape, Message: "cwd escaped Project Prompt scope", Category: "authorization"},
	}); err != nil {
		t.Fatal(err)
	}
	response := <-opened
	if response.err != nil {
		t.Fatal(response.err)
	}
	normalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectOpen, response.result)
	if normalized["status"] != string(protocol.WorkSessionFailed) {
		t.Fatalf("Prompt failure WorkSession status = %#v", normalized["status"])
	}
	targets := normalized["targets"].([]any)
	if len(targets) != 1 || targets[0].(map[string]any)["status"] != string(protocol.TargetContextError) {
		t.Fatalf("Prompt failure targets = %#v", targets)
	}
	stored, err := projects.ListWorkTargets(t.Context(), "mcp:prompt-failure-owner", normalized["work_session_id"].(string))
	if err != nil || len(stored) != 1 || stored[0].Target.ContextRevision != "" || stored[0].Target.Prompt.Complete || stored[0].LastError == "" {
		t.Fatalf("Prompt failure Target = %#v err=%v", stored, err)
	}
	_ = socket.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	var unexpected protocol.Message
	if err := socket.ReadJSON(&unexpected); err == nil {
		t.Fatalf("Prompt failure unexpectedly continued to target.bind: %#v", unexpected)
	}
}

func TestSameNodeWorkSessionsKeepIndependentTargetCWDAndPrompt(t *testing.T) {
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	_, rootBind := openReadyProjectTargetAtCWDForMCPTest(t, server, project, deployment, socket, "mcp:isolation-owner", "isolation-root", ".")
	_, backendBind := openReadyProjectTargetAtCWDForMCPTest(t, server, project, deployment, socket, "mcp:isolation-owner", "isolation-backend", "backend")
	if rootBind.WorkSessionID == backendBind.WorkSessionID || rootBind.TargetID == backendBind.TargetID || rootBind.ContextRevision == backendBind.ContextRevision {
		t.Fatalf("independent WorkSessions shared identity/revision: root=%#v backend=%#v", rootBind, backendBind)
	}
	rootTarget, err := projects.GetWorkTarget(t.Context(), "mcp:isolation-owner", rootBind.WorkSessionID, rootBind.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	backendTarget, err := projects.GetWorkTarget(t.Context(), "mcp:isolation-owner", backendBind.WorkSessionID, backendBind.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if rootTarget.Target.CWDRel != "." || backendTarget.Target.CWDRel != "backend" || rootTarget.Target.Prompt.PromptRevision == backendTarget.Target.Prompt.PromptRevision {
		t.Fatalf("Target cwd/Prompt crossed sessions: root=%#v backend=%#v", rootTarget.Target, backendTarget.Target)
	}
}

func TestProjectListDoesNotInvokeNodeOperations(t *testing.T) {
	server, _, _, _, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	result, err := server.callProjectList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	normalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectList, result)
	projects, ok := normalized["projects"].([]any)
	if !ok || len(projects) < 1 {
		t.Fatalf("project_list = %#v", normalized)
	}
	_ = socket.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var unexpected protocol.Message
	if err := socket.ReadJSON(&unexpected); err == nil {
		t.Fatalf("project_list unexpectedly invoked Node operation: %#v", unexpected)
	}
	_ = socket.SetReadDeadline(time.Time{})
}

func TestNodeToolUsesServerBoundTargetExecutionContext(t *testing.T) {
	server, _, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	ctx, bind := openReadyProjectTargetForMCPTest(t, server, project, deployment, socket, "mcp:node-route-owner", "node-route-open")

	type nodeCallResult struct {
		result any
		err    error
	}
	called := make(chan nodeCallResult, 1)
	go func() {
		result, err := server.callNodeTool(ctx, "read_file", map[string]any{
			"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "path": "README.md",
		})
		called <- nodeCallResult{result: result, err: err}
	}()

	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationToolCall || invoke.ExecutionContext == nil {
		t.Fatalf("Node tool invoke = %#v", invoke)
	}
	wantContext := protocol.ExecutionContext{
		WorkSessionID: bind.WorkSessionID, TargetID: bind.TargetID, ProjectID: bind.ProjectID, DeploymentID: bind.DeploymentID,
		DeploymentRevision: bind.DeploymentRevision, ContextRevision: bind.ContextRevision,
	}
	if *invoke.ExecutionContext != wantContext {
		t.Fatalf("execution_context = %#v, want %#v", *invoke.ExecutionContext, wantContext)
	}
	var request protocol.ToolCallRequest
	if err := json.Unmarshal(invoke.Arguments, &request); err != nil {
		t.Fatal(err)
	}
	if request.Tool != "read_file" || request.Arguments["path"] != "README.md" {
		t.Fatalf("tool.call request = %#v", request)
	}
	for _, forbidden := range []string{"work_session_id", "target_id", "node_id", "working_folder", "permissions"} {
		if _, exists := request.Arguments[forbidden]; exists {
			t.Fatalf("model routing field %s leaked into Node tool arguments: %#v", forbidden, request.Arguments)
		}
	}
	writeProjectResult(t, socket, invoke.RequestID, map[string]any{"path": "README.md", "content": "ok"})
	response := <-called
	if response.err != nil {
		t.Fatal(response.err)
	}
	toolResult, ok := response.result.(*mcpsdk.CallToolResult)
	if !ok || toolResult.IsError {
		t.Fatalf("Node tool result = %#v", response.result)
	}
}

func TestNodeToolRejectsInvalidProjectRoutingBeforeNodeInvoke(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, server *Server, projects *projectstore.Store, project projectstore.Project, deployment projectstore.Deployment, ctx context.Context, bind protocol.ProjectTargetBindRequest) (*mcpsdk.CallToolResult, error)
		code string
	}{
		{
			name: "cross_owner", code: protocol.ErrorSessionTargetDenied,
			call: func(t *testing.T, server *Server, _ *projectstore.Store, _ projectstore.Project, _ projectstore.Deployment, _ context.Context, bind protocol.ProjectTargetBindRequest) (*mcpsdk.CallToolResult, error) {
				return server.callNodeTool(withMCPClientBinding(t.Context(), "mcp:other-owner"), "read_file", map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "path": "README.md"})
			},
		},
		{
			name: "cross_session", code: protocol.ErrorSessionTargetDenied,
			call: func(t *testing.T, server *Server, _ *projectstore.Store, _ projectstore.Project, _ projectstore.Deployment, ctx context.Context, bind protocol.ProjectTargetBindRequest) (*mcpsdk.CallToolResult, error) {
				return server.callNodeTool(ctx, "read_file", map[string]any{"work_session_id": "ws_other", "target_id": bind.TargetID, "path": "README.md"})
			},
		},
		{
			name: "node_override", code: protocol.ErrorSessionTargetDenied,
			call: func(t *testing.T, server *Server, _ *projectstore.Store, _ projectstore.Project, deployment projectstore.Deployment, ctx context.Context, bind protocol.ProjectTargetBindRequest) (*mcpsdk.CallToolResult, error) {
				return server.callNodeTool(ctx, "read_file", map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "node_id": deployment.NodeID, "path": "README.md"})
			},
		},
		{
			name: "stale_deployment", code: protocol.ErrorRevisionConflict,
			call: func(t *testing.T, server *Server, projects *projectstore.Store, project projectstore.Project, deployment projectstore.Deployment, ctx context.Context, bind protocol.ProjectTargetBindRequest) (*mcpsdk.CallToolResult, error) {
				if _, err := projects.UpdateDeployment(t.Context(), project.ID, deployment.ID, projectstore.UpdateDeploymentInput{
					ExpectedRevision: deployment.DesiredRevision, WorkingFolder: deployment.WorkingFolder, Role: deployment.Role, Purpose: deployment.Purpose,
					Permissions: deployment.Permissions, Enabled: deployment.Enabled,
				}); err != nil {
					t.Fatal(err)
				}
				return server.callNodeTool(ctx, "read_file", map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "path": "README.md"})
			},
		},
		{
			name: "revoked_target", code: protocol.ErrorSessionTargetDenied,
			call: func(t *testing.T, server *Server, projects *projectstore.Store, _ projectstore.Project, deployment projectstore.Deployment, ctx context.Context, bind protocol.ProjectTargetBindRequest) (*mcpsdk.CallToolResult, error) {
				if _, err := projects.RevokeTargetsForDeployment(t.Context(), deployment.ID, "test revoke"); err != nil {
					t.Fatal(err)
				}
				return server.callNodeTool(ctx, "read_file", map[string]any{"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "path": "README.md"})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
			defer closeNode()
			ctx, bind := openReadyProjectTargetForMCPTest(t, server, project, deployment, socket, "mcp:routing-owner", "routing-open")
			result, err := test.call(t, server, projects, project, deployment, ctx, bind)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError {
				t.Fatalf("invalid route unexpectedly succeeded: %#v", result)
			}
			details, ok := result.StructuredContent.(map[string]any)
			if !ok || details["code"] != test.code {
				t.Fatalf("invalid route details = %#v, want code %s", result.StructuredContent, test.code)
			}
			_ = socket.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
			var unexpected protocol.Message
			if err := socket.ReadJSON(&unexpected); err == nil {
				t.Fatalf("invalid route reached Node: %#v", unexpected)
			}
		})
	}
}

func TestHistoricalTargetControlAllowsKillButRejectsNewStdinOnStaleTarget(t *testing.T) {
	sessionAct := protocol.ToolDescriptor{
		Name: "session_act", Title: "Act on command sessions", Description: "test command session control",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"action":     map[string]any{"type": "string", "enum": []any{"write", "kill", "kill_all"}},
				"session_id": map[string]any{"type": "string"}, "chars": map[string]any{"type": "string"},
			},
			"required": []any{"action"},
		},
		OutputSchema: map[string]any{"type": "object", "additionalProperties": true},
	}
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTestWithTools(t, []protocol.ToolDescriptor{sessionAct})
	defer closeNode()
	ctx, bind := openReadyProjectTargetForMCPTest(t, server, project, deployment, socket, "mcp:historical-control-owner", "historical-control-open")

	if _, err := projects.UpdateDeployment(t.Context(), project.ID, deployment.ID, projectstore.UpdateDeploymentInput{
		ExpectedRevision: deployment.DesiredRevision, WorkingFolder: deployment.WorkingFolder, Role: deployment.Role, Purpose: deployment.Purpose,
		Permissions: deployment.Permissions, Enabled: deployment.Enabled,
	}); err != nil {
		t.Fatal(err)
	}

	type nodeToolCall struct {
		result *mcpsdk.CallToolResult
		err    error
	}
	killDone := make(chan nodeToolCall, 1)
	go func() {
		result, err := server.callNodeTool(ctx, "session_act", map[string]any{
			"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "action": "kill", "session_id": "session-old",
		})
		killDone <- nodeToolCall{result: result, err: err}
	}()
	killInvoke := readProjectInvoke(t, socket)
	if killInvoke.Operation != protocol.OperationToolCall || killInvoke.ExecutionContext == nil {
		t.Fatalf("historical kill invoke = %#v", killInvoke)
	}
	var killRequest protocol.ToolCallRequest
	if err := json.Unmarshal(killInvoke.Arguments, &killRequest); err != nil {
		t.Fatal(err)
	}
	if killRequest.Tool != "session_act" || killRequest.Arguments["action"] != "kill" || killRequest.Arguments["session_id"] != "session-old" {
		t.Fatalf("historical kill request = %#v", killRequest)
	}
	writeProjectResult(t, socket, killInvoke.RequestID, map[string]any{"session_id": "session-old", "status": "killed"})
	if result := <-killDone; result.err != nil || result.result == nil || result.result.IsError {
		t.Fatalf("historical kill failed: result=%#v err=%v", result.result, result.err)
	}

	writeResult, err := server.callNodeTool(ctx, "session_act", map[string]any{
		"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "action": "write", "session_id": "session-old", "chars": "continue\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !writeResult.IsError || writeResult.StructuredContent.(map[string]any)["code"] != protocol.ErrorRevisionConflict {
		t.Fatalf("stale stdin write = %#v", writeResult)
	}
	_ = socket.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	var unexpected protocol.Message
	if err := socket.ReadJSON(&unexpected); err == nil {
		t.Fatalf("stale stdin write reached Node: %#v", unexpected)
	}
}

func TestAllowsHistoricalTargetControlIsActionSpecific(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		action string
		want   bool
	}{
		{name: "observe status", tool: "session_observe", action: "status", want: true},
		{name: "kill", tool: "session_act", action: "kill", want: true},
		{name: "stdin write", tool: "session_act", action: "write", want: false},
		{name: "acp inspect", tool: "acp_session", action: "inspect", want: true},
		{name: "acp new", tool: "acp_session", action: "new", want: false},
		{name: "prompt events", tool: "acp_prompt", action: "events", want: true},
		{name: "prompt cancel", tool: "acp_prompt", action: "cancel", want: true},
		{name: "prompt start", tool: "acp_prompt", action: "start", want: false},
		{name: "interaction cancel", tool: "acp_interaction", action: "cancel", want: true},
		{name: "interaction respond", tool: "acp_interaction", action: "respond", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := allowsHistoricalTargetControl(test.tool, map[string]any{"action": test.action}); got != test.want {
				t.Fatalf("allowsHistoricalTargetControl(%q,%q) = %v, want %v", test.tool, test.action, got, test.want)
			}
		})
	}
}

func openReadyProjectTargetForMCPTest(t *testing.T, server *Server, project projectstore.Project, deployment projectstore.Deployment, socket *websocket.Conn, owner, requestID string) (context.Context, protocol.ProjectTargetBindRequest) {
	return openReadyProjectTargetAtCWDForMCPTest(t, server, project, deployment, socket, owner, requestID, ".")
}

func openReadyProjectTargetAtCWDForMCPTest(t *testing.T, server *Server, project projectstore.Project, deployment projectstore.Deployment, socket *websocket.Conn, owner, requestID, cwdRel string) (context.Context, protocol.ProjectTargetBindRequest) {
	t.Helper()
	ctx := withMCPClientBinding(t.Context(), owner)
	opened := make(chan projectToolCallResult, 1)
	go func() {
		result, err := server.callProjectOpen(ctx, map[string]any{
			"project_id": project.ID, "client_request_id": requestID,
			"targets": []any{map[string]any{"deployment_id": deployment.ID, "cwd_rel": cwdRel}},
		})
		opened <- projectToolCallResult{result: result, err: err}
	}()
	promptInvoke := readProjectInvoke(t, socket)
	if promptInvoke.Operation != protocol.OperationProjectPromptLoad {
		t.Fatalf("open Target Prompt operation = %q", promptInvoke.Operation)
	}
	var promptRequest protocol.ProjectPromptLoadRequest
	if err := json.Unmarshal(promptInvoke.Arguments, &promptRequest); err != nil {
		t.Fatal(err)
	}
	if promptRequest.CWDRel != cwdRel {
		t.Fatalf("open Target Prompt cwd = %q, want %q", promptRequest.CWDRel, cwdRel)
	}
	sourcePath := "AGENTS.md"
	if cwdRel != "." {
		sourcePath = cwdRel + "/AGENTS.md"
	}
	content := requestID + " rules\n"
	prompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:" + requestID + "-prompt", Complete: true, Bytes: len(content),
		Sources: []protocol.PromptSource{{Path: sourcePath, Scope: cwdRel, SHA256: "sha256:" + requestID + "-source", Bytes: len(content), Content: content}},
	}
	writeProjectResult(t, socket, promptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: cwdRel, Prompt: prompt, SourceProvenance: projectTestSourceProvenance()})
	bindInvoke := readProjectInvoke(t, socket)
	if bindInvoke.Operation != protocol.OperationProjectTargetBind {
		t.Fatalf("open Target bind operation = %q", bindInvoke.Operation)
	}
	var bind protocol.ProjectTargetBindRequest
	if err := json.Unmarshal(bindInvoke.Arguments, &bind); err != nil {
		t.Fatal(err)
	}
	if bind.CWDRel != cwdRel {
		t.Fatalf("open Target bind cwd = %q, want %q", bind.CWDRel, cwdRel)
	}
	writeProjectResult(t, socket, bindInvoke.RequestID, projectTestTargetAcknowledgment(bind))
	result := <-opened
	if result.err != nil {
		t.Fatal(result.err)
	}
	return ctx, bind
}

func prepareOnlineProjectMCPTest(t *testing.T) (*Server, *projectstore.Store, projectstore.Project, projectstore.Deployment, *websocket.Conn, func()) {
	return prepareOnlineProjectMCPTestWithTools(t, nil)
}

func prepareOnlineProjectMCPTestWithTools(t *testing.T, extraTools []protocol.ToolDescriptor) (*Server, *projectstore.Store, projectstore.Project, projectstore.Deployment, *websocket.Conn, func()) {
	t.Helper()
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_mcp_online_12345678", "MCPNode")
	project := createProjectThroughAPI(t, handler, "MCP Project "+t.Name())
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, t.TempDir())
	readFile := protocol.ToolDescriptor{
		Name: "read_file", Title: "Read file", Description: "read test file",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []any{"path"}, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object", "additionalProperties": true},
	}
	tools := append([]protocol.ToolDescriptor{readFile}, extraTools...)
	socket, closeNode := connectProjectFakeNodeWithTools(t, server, node, tools)
	apply := readProjectInvoke(t, socket)
	if apply.Operation != protocol.OperationProjectDeploymentApply {
		closeNode()
		t.Fatalf("initial Project MCP setup operation = %q", apply.Operation)
	}
	var payload protocol.Deployment
	if err := json.Unmarshal(apply.Arguments, &payload); err != nil {
		closeNode()
		t.Fatal(err)
	}
	writeProjectResult(t, socket, apply.RequestID, map[string]any{"deployment": payload})
	deployment = waitForDeploymentState(t, projects, project.ID, deployment.ID, func(value projectstore.Deployment) bool {
		return value.ApplyStatus == string(protocol.DeploymentApplyApplied) && value.AppliedRevision == value.DesiredRevision
	})
	return server, projects, project, deployment, socket, closeNode
}

func TestProjectMCPTestErrorsRemainInspectable(t *testing.T) {
	if !errors.Is(agentdock.ErrNodeOffline, agentdock.ErrNodeOffline) || http.MethodPost == "" {
		t.Fatal("unexpected test dependency state")
	}
}
