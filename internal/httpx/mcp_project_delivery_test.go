package httpx

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	mcpcontract "github.com/Serialeo/agentdock-protocol/mcpcontract"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

type projectMCPCall struct {
	result *mcpsdk.CallToolResult
	err    error
}

func TestProjectOpenMCPHandlerPersistsReturnedThenConsumesHostMetaAck(t *testing.T) {
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	updatedProject, err := projects.UpdateProject(t.Context(), project.ID, projectstore.UpdateProjectInput{
		ExpectedRevision: project.Revision, Name: project.Name, OrchestrationPolicy: "Linux handles implementation; do not duplicate work.", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	project = updatedProject
	ownerKey := "mcp:delivery-roundtrip-owner"

	mcpServer := server.currentMCPServer()
	if mcpServer == nil {
		t.Fatal("Nexus MCP server is unavailable")
	}
	mcpServer.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, request mcpsdk.Request) (mcpsdk.Result, error) {
			return next(withMCPClientBinding(ctx, ownerKey), method, request)
		}
	})
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "project-delivery-test", Version: "v1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	arguments := map[string]any{
		"project_id": project.ID, "client_request_id": "delivery-roundtrip-open",
		"targets": []any{map[string]any{"deployment_id": deployment.ID, "cwd_rel": "."}},
	}
	firstDone := make(chan projectMCPCall, 1)
	go func() {
		result, callErr := clientSession.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: mcpcontract.ToolProjectOpen, Arguments: arguments})
		firstDone <- projectMCPCall{result: result, err: callErr}
	}()

	promptInvoke := readProjectInvoke(t, socket)
	if promptInvoke.Operation != protocol.OperationProjectPromptLoad {
		t.Fatalf("first MCP Project operation = %q", promptInvoke.Operation)
	}
	prompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:delivery-prompt", Complete: true, Bytes: 11,
		Sources: []protocol.PromptSource{{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:delivery-source", Bytes: 11, Content: "root rules\n"}},
	}
	writeProjectResult(t, socket, promptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: ".", Prompt: prompt, SourceProvenance: projectTestSourceProvenance()})
	bindInvoke := readProjectInvoke(t, socket)
	if bindInvoke.Operation != protocol.OperationProjectTargetBind {
		t.Fatalf("second MCP Project operation = %q", bindInvoke.Operation)
	}
	var bind protocol.ProjectTargetBindRequest
	if err := json.Unmarshal(bindInvoke.Arguments, &bind); err != nil {
		t.Fatal(err)
	}
	writeProjectResult(t, socket, bindInvoke.RequestID, map[string]any{"target": map[string]any{
		"work_session_id": bind.WorkSessionID, "target_id": bind.TargetID, "project_id": bind.ProjectID,
		"deployment_id": bind.DeploymentID, "cwd_rel": bind.CWDRel, "deployment_revision": bind.DeploymentRevision, "context_revision": bind.ContextRevision,
	}})

	first := <-firstDone
	if first.err != nil || first.result == nil || first.result.IsError {
		t.Fatalf("first project_open MCP result=%#v err=%v", first.result, first.err)
	}
	structured, ok := first.result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("first structuredContent = %#v", first.result.StructuredContent)
	}
	firstNormalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectOpen, structured)
	projectView := firstNormalized["project"].(map[string]any)
	if projectView["orchestration_policy"] != project.OrchestrationPolicy {
		t.Fatalf("model-visible orchestration_policy = %#v", projectView["orchestration_policy"])
	}
	deploymentViews := firstNormalized["deployments"].([]any)
	if len(deploymentViews) != 1 {
		t.Fatalf("model-visible Deployment topology = %#v", deploymentViews)
	}
	deploymentView := deploymentViews[0].(map[string]any)
	if deploymentView["working_folder"] != deployment.WorkingFolder || deploymentView["applied_revision"] != deployment.AppliedRevision {
		t.Fatalf("model-visible Deployment = %#v", deploymentView)
	}
	targetViews := firstNormalized["targets"].([]any)
	if len(targetViews) != 1 {
		t.Fatalf("model-visible Targets = %#v", targetViews)
	}
	targetView := targetViews[0].(map[string]any)
	promptView := targetView["prompt"].(map[string]any)
	sources := promptView["sources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["content"] != "root rules\n" || sources[0].(map[string]any)["sha256"] != "sha256:delivery-source" {
		t.Fatalf("model-visible full Prompt sources = %#v", sources)
	}
	workSessionID := firstNormalized["work_session_id"].(string)
	contextRevision := firstNormalized["context_revision"].(string)
	delivery := firstNormalized["delivery"].(map[string]any)
	if delivery["status"] != string(protocol.ProjectContextReturned) || delivery["context_revision"] != contextRevision {
		t.Fatalf("first delivery = %#v", delivery)
	}
	persisted, err := projects.GetContextDelivery(t.Context(), ownerKey, workSessionID, "")
	if err != nil || persisted.Status != protocol.ProjectContextReturned || persisted.ContextRevision != contextRevision {
		t.Fatalf("persisted returned delivery = %#v err=%v", persisted, err)
	}

	second, err := clientSession.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Meta: mcpsdk.Meta{protocol.ProjectContextAckMetaKey: map[string]any{
			"work_session_id": workSessionID, "context_revision": contextRevision,
		}},
		Name: mcpcontract.ToolProjectOpen, Arguments: arguments,
	})
	if err != nil || second == nil || second.IsError {
		t.Fatalf("acknowledged project_open result=%#v err=%v", second, err)
	}
	secondStructured := second.StructuredContent.(map[string]any)
	secondNormalized := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectOpen, secondStructured)
	secondDelivery := secondNormalized["delivery"].(map[string]any)
	if secondDelivery["status"] != string(protocol.ProjectContextHostConsumed) || secondDelivery["context_revision"] != contextRevision {
		t.Fatalf("acknowledged delivery = %#v", secondDelivery)
	}
	consumed, err := projects.GetContextDelivery(t.Context(), ownerKey, workSessionID, "")
	if err != nil || consumed.Status != protocol.ProjectContextHostConsumed || consumed.HostConsumedAt == nil {
		t.Fatalf("persisted host_consumed delivery = %#v err=%v", consumed, err)
	}

	stale, err := clientSession.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Meta: mcpsdk.Meta{protocol.ProjectContextAckMetaKey: map[string]any{
			"work_session_id": workSessionID, "context_revision": "sha256:stale",
		}},
		Name: mcpcontract.ToolProjectOpen, Arguments: arguments,
	})
	if err != nil || stale == nil || !stale.IsError {
		t.Fatalf("stale Host ack result=%#v err=%v", stale, err)
	}
	staleDetails, ok := stale.StructuredContent.(map[string]any)
	if !ok || staleDetails["code"] != protocol.ErrorRevisionConflict {
		t.Fatalf("stale Host ack details = %#v", stale.StructuredContent)
	}

	fakeModelAck, err := clientSession.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: mcpcontract.ToolProjectOpen, Arguments: map[string]any{
		"project_id": project.ID, "client_request_id": "fake-model-ack", "host_consumed": true,
	}})
	if err != nil || fakeModelAck == nil || !fakeModelAck.IsError {
		t.Fatalf("model argument fake ack result=%#v err=%v", fakeModelAck, err)
	}

	contextArguments := map[string]any{"work_session_id": workSessionID, "target_id": bind.TargetID, "cwd_rel": "backend"}
	contextDone := make(chan projectMCPCall, 1)
	go func() {
		result, callErr := clientSession.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: mcpcontract.ToolProjectContext, Arguments: contextArguments})
		contextDone <- projectMCPCall{result: result, err: callErr}
	}()
	contextPromptInvoke := readProjectInvoke(t, socket)
	if contextPromptInvoke.Operation != protocol.OperationProjectPromptLoad {
		t.Fatalf("project_context Prompt operation = %q", contextPromptInvoke.Operation)
	}
	backendPrompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:delivery-backend-prompt", Complete: true, Bytes: 19,
		Sources: []protocol.PromptSource{
			{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:delivery-source", Bytes: 11, Content: "root rules\n"},
			{Path: "backend/AGENTS.md", Scope: "backend", SHA256: "sha256:delivery-backend-source", Bytes: 8, Content: "backend\n"},
		},
	}
	writeProjectResult(t, socket, contextPromptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: "backend", Prompt: backendPrompt, SourceProvenance: projectTestSourceProvenance()})
	rebindInvoke := readProjectInvoke(t, socket)
	if rebindInvoke.Operation != protocol.OperationProjectTargetRebind {
		t.Fatalf("project_context rebind operation = %q", rebindInvoke.Operation)
	}
	var rebind protocol.ProjectTargetRebindRequest
	if err := json.Unmarshal(rebindInvoke.Arguments, &rebind); err != nil {
		t.Fatal(err)
	}
	writeProjectResult(t, socket, rebindInvoke.RequestID, projectTestTargetRebindAcknowledgment(rebind))
	contextResult := <-contextDone
	if contextResult.err != nil || contextResult.result == nil || contextResult.result.IsError {
		t.Fatalf("first project_context MCP result=%#v err=%v", contextResult.result, contextResult.err)
	}
	contextStructured := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectContext, contextResult.result.StructuredContent.(map[string]any))
	contextTarget := contextStructured["target"].(map[string]any)
	targetContextRevision := contextTarget["context_revision"].(string)
	contextDelivery := contextStructured["delivery"].(map[string]any)
	if contextDelivery["status"] != string(protocol.ProjectContextReturned) || contextDelivery["context_revision"] != targetContextRevision {
		t.Fatalf("first project_context delivery = %#v", contextDelivery)
	}
	targetReturned, err := projects.GetContextDelivery(t.Context(), ownerKey, workSessionID, bind.TargetID)
	if err != nil || targetReturned.Status != protocol.ProjectContextReturned || targetReturned.ContextRevision != targetContextRevision {
		t.Fatalf("persisted Target returned delivery = %#v err=%v", targetReturned, err)
	}

	ackContextDone := make(chan projectMCPCall, 1)
	go func() {
		result, callErr := clientSession.CallTool(t.Context(), &mcpsdk.CallToolParams{
			Meta: mcpsdk.Meta{protocol.ProjectContextAckMetaKey: map[string]any{
				"work_session_id": workSessionID, "target_id": bind.TargetID, "context_revision": targetContextRevision,
			}},
			Name: mcpcontract.ToolProjectContext, Arguments: contextArguments,
		})
		ackContextDone <- projectMCPCall{result: result, err: callErr}
	}()
	contextPromptInvoke = readProjectInvoke(t, socket)
	if contextPromptInvoke.Operation != protocol.OperationProjectPromptLoad {
		t.Fatalf("ack project_context Prompt operation = %q", contextPromptInvoke.Operation)
	}
	writeProjectResult(t, socket, contextPromptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: "backend", Prompt: backendPrompt, SourceProvenance: projectTestSourceProvenance()})
	rebindInvoke = readProjectInvoke(t, socket)
	if rebindInvoke.Operation != protocol.OperationProjectTargetRebind {
		t.Fatalf("ack project_context rebind operation = %q", rebindInvoke.Operation)
	}
	if err := json.Unmarshal(rebindInvoke.Arguments, &rebind); err != nil {
		t.Fatal(err)
	}
	if rebind.ContextRevision != targetContextRevision {
		t.Fatalf("same context refresh changed revision: got %q want %q", rebind.ContextRevision, targetContextRevision)
	}
	writeProjectResult(t, socket, rebindInvoke.RequestID, projectTestTargetRebindAcknowledgment(rebind))
	ackContextResult := <-ackContextDone
	if ackContextResult.err != nil || ackContextResult.result == nil || ackContextResult.result.IsError {
		t.Fatalf("acknowledged project_context result=%#v err=%v", ackContextResult.result, ackContextResult.err)
	}
	ackContextStructured := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectContext, ackContextResult.result.StructuredContent.(map[string]any))
	if got := ackContextStructured["delivery"].(map[string]any)["status"]; got != string(protocol.ProjectContextHostConsumed) {
		t.Fatalf("acknowledged Target delivery status = %#v", got)
	}
	targetConsumed, err := projects.GetContextDelivery(t.Context(), ownerKey, workSessionID, bind.TargetID)
	if err != nil || targetConsumed.Status != protocol.ProjectContextHostConsumed || targetConsumed.HostConsumedAt == nil {
		t.Fatalf("persisted Target host_consumed delivery = %#v err=%v", targetConsumed, err)
	}

	changedBackendContent := "backend changed\n"
	changedPrompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:delivery-backend-prompt-v2", Complete: true, Bytes: 11 + len(changedBackendContent),
		Sources: []protocol.PromptSource{
			{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:delivery-source", Bytes: 11, Content: "root rules\n"},
			{Path: "backend/AGENTS.md", Scope: "backend", SHA256: "sha256:delivery-backend-source-v2", Bytes: len(changedBackendContent), Content: changedBackendContent},
		},
	}
	changedDone := make(chan projectMCPCall, 1)
	go func() {
		result, callErr := clientSession.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: mcpcontract.ToolProjectContext, Arguments: contextArguments})
		changedDone <- projectMCPCall{result: result, err: callErr}
	}()
	changedPromptInvoke := readProjectInvoke(t, socket)
	if changedPromptInvoke.Operation != protocol.OperationProjectPromptLoad {
		t.Fatalf("changed project_context Prompt operation = %q", changedPromptInvoke.Operation)
	}
	writeProjectResult(t, socket, changedPromptInvoke.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: "backend", Prompt: changedPrompt, SourceProvenance: projectTestSourceProvenance()})
	changedRebindInvoke := readProjectInvoke(t, socket)
	if changedRebindInvoke.Operation != protocol.OperationProjectTargetRebind {
		t.Fatalf("changed project_context rebind operation = %q", changedRebindInvoke.Operation)
	}
	var changedRebind protocol.ProjectTargetRebindRequest
	if err := json.Unmarshal(changedRebindInvoke.Arguments, &changedRebind); err != nil {
		t.Fatal(err)
	}
	if changedRebind.ContextRevision == targetContextRevision {
		t.Fatalf("changed Prompt retained stale Target context revision %q", targetContextRevision)
	}
	writeProjectResult(t, socket, changedRebindInvoke.RequestID, projectTestTargetRebindAcknowledgment(changedRebind))
	changedResult := <-changedDone
	if changedResult.err != nil || changedResult.result == nil || changedResult.result.IsError {
		t.Fatalf("changed project_context result=%#v err=%v", changedResult.result, changedResult.err)
	}
	changedStructured := assertCentralToolResultMatchesOutputSchema(t, mcpcontract.ToolProjectContext, changedResult.result.StructuredContent.(map[string]any))
	changedDelivery := changedStructured["delivery"].(map[string]any)
	if changedDelivery["status"] != string(protocol.ProjectContextReturned) || changedDelivery["context_revision"] != changedRebind.ContextRevision {
		t.Fatalf("changed Project Context did not reset delivery to returned: %#v", changedDelivery)
	}
	changedStored, err := projects.GetContextDelivery(t.Context(), ownerKey, workSessionID, bind.TargetID)
	if err != nil || changedStored.Status != protocol.ProjectContextReturned || changedStored.ContextRevision != changedRebind.ContextRevision || changedStored.HostConsumedAt != nil {
		t.Fatalf("changed Project Context persisted stale consumed evidence: %#v err=%v", changedStored, err)
	}

	sessions, err := projects.ListProjectWorkSessions(t.Context(), project.ID, 100)
	if err != nil || len(sessions) != 1 || sessions[0].ID != workSessionID {
		t.Fatalf("Project Context delivery created a second WorkSession: %#v err=%v", sessions, err)
	}
}

func TestProjectContextDeliveryBudgetsRejectAggregateBodiesAndOversizedJSON(t *testing.T) {
	chunk := strings.Repeat("x", 220<<10)
	targets := make([]projectstore.WorkTarget, 0, 5)
	for index := 0; index < 5; index++ {
		targets = append(targets, projectstore.WorkTarget{Target: protocol.WorkTarget{
			ID: "target-budget-" + string(rune('a'+index)), Status: protocol.TargetReady,
			Prompt: protocol.ProjectPrompt{
				PromptRevision: "sha256:prompt", Complete: true, Bytes: len(chunk),
				Sources: []protocol.PromptSource{{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:source", Bytes: len(chunk), Content: chunk}},
			},
		}})
	}
	if err := validateProjectPromptContextBudget(targets); err == nil {
		t.Fatal("aggregate Project Prompt bodies above 1 MiB were accepted")
	}

	within := targets[:4]
	if err := validateProjectPromptContextBudget(within); err != nil {
		t.Fatalf("Project Prompt bodies below 1 MiB were rejected: %v", err)
	}
	if err := validateProjectContextDeliveryEnvelope(map[string]any{"body": strings.Repeat("x", protocol.MaxProjectContextDeliveryBytes+1)}); err == nil {
		t.Fatal("oversized model-visible Project Context JSON was accepted")
	}
}
