package httpx

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

func TestContinuationPresentationKeepsBindingSecretOutOfModelContent(t *testing.T) {
	server, projects, project, deployment, _, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	server.setMCPAppsEnabled(true)
	ctx := withMCPClientBinding(t.Context(), "continuation-owner")
	session, _, err := projects.BeginWorkSession(ctx, "continuation-owner", project.ID, "continuation-open", "digest", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = projects.UpdateWorkSessionContext(ctx, "continuation-owner", session.ID, project.Revision, protocol.WorkSessionReady, "context-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = projects.PutWorkTarget(ctx, "continuation-owner", projectstore.WorkTarget{Target: protocol.WorkTarget{
		ID: "continuation-target", WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: deployment.ID, NodeID: deployment.NodeID,
		CWDRel: ".", DeploymentRevision: deployment.AppliedRevision, ContextRevision: "context-1", Status: protocol.TargetReady, Permissions: deployment.Permissions,
	}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.continuationToolResult(ctx, "present_work_continuation", map[string]any{"work_session_id": session.ID})
	if err != nil || response.IsError {
		t.Fatalf("present: %#v %v", response, err)
	}
	private, ok := response.Meta[continuationPrivateMeta].(map[string]any)
	if !ok {
		t.Fatalf("missing private binding: %#v", response.Meta)
	}
	secret, _ := private["binding_secret"].(string)
	if secret == "" {
		t.Fatal("empty presentation secret")
	}
	model, _ := json.Marshal([]any{response.Content, response.StructuredContent})
	if strings.Contains(string(model), secret) || strings.Contains(string(model), "binding_secret") {
		t.Fatalf("presentation secret leaked to model: %s", model)
	}
	foreign := withMCPClientBinding(t.Context(), "different-owner")
	denied, err := server.continuationToolResult(foreign, "present_work_continuation", map[string]any{"work_session_id": session.ID})
	if err != nil || !denied.IsError {
		t.Fatalf("cross-owner present: %#v %v", denied, err)
	}
	server.setMCPAppsEnabled(false)
	denied, err = server.continuationToolResult(ctx, "present_work_continuation", map[string]any{"work_session_id": session.ID})
	if err != nil || !denied.IsError {
		t.Fatalf("disabled present: %#v %v", denied, err)
	}
}

func TestContinuationToolVisibilityAndOnlyPresenterCreatesCard(t *testing.T) {
	for _, apps := range []bool{false, true} {
		found := map[string]bool{}
		for _, definition := range nexusToolDefinitionsWithApps(apps) {
			if !isContinuationTool(definition.Name) {
				continue
			}
			found[definition.Name] = true
			ui, _ := definition.Meta["ui"].(map[string]any)
			_, hasResource := ui["resourceUri"]
			if hasResource != (definition.Name == "present_work_continuation") {
				t.Fatalf("unexpected card binding %s: %#v", definition.Name, ui)
			}
			visibility, _ := ui["visibility"].([]string)
			expected := "model"
			if continuationAppOnly(definition.Name) {
				expected = "app"
			}
			if len(visibility) != 1 || visibility[0] != expected {
				t.Fatalf("visibility %s: %#v", definition.Name, ui)
			}
		}
		for _, name := range continuationToolNames() {
			expected := apps || (!continuationAppOnly(name) && name != "present_work_continuation")
			if found[name] != expected {
				t.Fatalf("apps=%v tool=%s found=%v", apps, name, found[name])
			}
		}
	}
}

func TestCommandOutcomeCollectorCommitsBeforeAckAndReplaysAckLoss(t *testing.T) {
	server, projects, project, deployment, socket, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	ctx := t.Context()
	session, _, err := projects.BeginWorkSession(ctx, "collector-owner", project.ID, "collector-request", "hash", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = projects.UpdateWorkSessionContext(ctx, "collector-owner", session.ID, project.Revision, protocol.WorkSessionReady, "ctx")
	if err != nil {
		t.Fatal(err)
	}
	target, err := projects.PutWorkTarget(ctx, "collector-owner", projectstore.WorkTarget{Target: protocol.WorkTarget{ID: "collector-target", WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: deployment.ID, NodeID: deployment.NodeID, CWDRel: ".", DeploymentRevision: deployment.AppliedRevision, ContextRevision: "ctx", Status: protocol.TargetReady, Permissions: deployment.Permissions}})
	if err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	outcome := protocol.CommandOutcome{EventID: "event-collector", CommandSessionID: "command-collector", ExecutionContext: protocol.ExecutionContext{WorkSessionID: session.ID, TargetID: target.Target.ID, ProjectID: project.ID, DeploymentID: deployment.ID, DeploymentRevision: deployment.AppliedRevision, ContextRevision: "ctx"}, State: protocol.CommandOutcomeCompleted, ExitCode: &exitCode, Stdout: "durable result", StartedAt: now, FinishedAt: now, UpdatedAt: now, PendingReport: true}
	for round := 0; round < 2; round++ {
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		done := make(chan error, 1)
		go func() { done <- server.collectNodeCommandOutcomes(callCtx, deployment.NodeID) }()
		read := readProjectInvoke(t, socket)
		if read.Operation != protocol.OperationCommandOutcomesRead {
			t.Fatalf("read=%s", read.Operation)
		}
		writeProjectResult(t, socket, read.RequestID, protocol.CommandOutcomesReadResult{Outcomes: []protocol.CommandOutcome{outcome}})
		ack := readProjectInvoke(t, socket)
		if ack.Operation != protocol.OperationCommandOutcomesAck {
			t.Fatalf("ack=%s", ack.Operation)
		}
		var ackInput protocol.CommandOutcomesAckRequest
		if err := json.Unmarshal(ack.Arguments, &ackInput); err != nil || len(ackInput.EventIDs) != 1 || ackInput.EventIDs[0] != outcome.EventID {
			t.Fatalf("ack input=%#v %v", ackInput, err)
		}
		if round == 0 {
			// ACK 丢失之前，先注册 await，证明已完成结果已持久可见，无需重新执行命令。
			_, err = projects.ControlContinuation(ctx, "collector-owner", protocol.WorkContinuationInput{WorkSessionID: session.ID, Action: "enable", Confirmed: true, MaxRounds: 3, MaxFailures: 2})
			if err != nil {
				t.Fatal(err)
			}
			result, awaitErr := projects.ControlContinuation(ctx, "collector-owner", protocol.WorkContinuationInput{WorkSessionID: session.ID, Action: "await", Sources: []protocol.ContinuationSource{{TargetID: target.Target.ID, CommandSessionID: outcome.CommandSessionID}}})
			if awaitErr != nil || result.Wake == nil {
				t.Fatalf("finished-before-await: %#v %v", result, awaitErr)
			}
			cancel()
			if <-done == nil {
				t.Fatal("lost ACK should report failure")
			}
			var cancelled protocol.Message
			if err := socket.ReadJSON(&cancelled); err != nil || cancelled.Type != protocol.MessageToolCancel || cancelled.RequestID != ack.RequestID {
				t.Fatalf("cancel=%#v err=%v", cancelled, err)
			}
		} else {
			writeProjectResult(t, socket, ack.RequestID, protocol.CommandOutcomesAckResult{AcknowledgedEventIDs: ackInput.EventIDs})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			cancel()
		}
	}
}

func TestContinuationCompleteCycleMatchesEveryPublishedOutputContract(t *testing.T) {
	server, projects, project, deployment, _, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	server.setMCPAppsEnabled(true)
	ctx := withMCPClientBinding(t.Context(), "cycle-owner")
	session, _, err := projects.BeginWorkSession(ctx, "cycle-owner", project.ID, "cycle-open", "hash", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = projects.UpdateWorkSessionContext(ctx, "cycle-owner", session.ID, project.Revision, protocol.WorkSessionReady, "context")
	if err != nil {
		t.Fatal(err)
	}
	target, err := projects.PutWorkTarget(ctx, "cycle-owner", projectstore.WorkTarget{Target: protocol.WorkTarget{ID: "cycle-target", WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: deployment.ID, NodeID: deployment.NodeID, CWDRel: ".", DeploymentRevision: deployment.AppliedRevision, ContextRevision: "context", Status: protocol.TargetReady, Permissions: deployment.Permissions}})
	if err != nil {
		t.Fatal(err)
	}
	node, nodeErr := server.agentDock.Get(ctx, deployment.NodeID)
	if nodeErr != nil {
		t.Fatal(nodeErr)
	}
	if _, err = server.agentDock.UpdateHello(ctx, deployment.NodeID, protocol.Hello{DeviceID: node.DeviceID, Version: agentdock.RequiredVersion, ProtocolVersion: protocol.ConnectionProtocolVersion, UIResources: []protocol.UIResourceCapability{}, Capabilities: []string{}, Tools: []protocol.ToolDescriptor{}, BridgeCapabilities: []string{protocol.CommandOutcomesCapability}}); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any) (map[string]any, map[string]any) {
		t.Helper()
		response, err := server.continuationToolResult(ctx, name, args)
		if err != nil || response.IsError {
			t.Fatalf("%s: %#v %v", name, response, err)
		}
		result, err := asMap(response.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		assertCentralToolResultMatchesOutputSchema(t, name, result)
		meta, _ := response.Meta[continuationPrivateMeta].(map[string]any)
		return result, meta
	}
	control := func(action string, extra map[string]any) map[string]any {
		t.Helper()
		args := map[string]any{"work_session_id": session.ID, "action": action}
		for k, v := range extra {
			args[k] = v
		}
		result, _ := call("work_continuation", args)
		return result
	}
	control("status", nil)
	control("enable", map[string]any{"confirmed": true, "max_rounds": 3, "max_failures": 2})
	_, private := call("present_work_continuation", map[string]any{"work_session_id": session.ID})
	base := map[string]any{"binding_id": "cycle-view"}
	for k, v := range private {
		base[k] = v
	}
	app := func(name string, extra map[string]any) map[string]any {
		t.Helper()
		args := map[string]any{}
		for k, v := range base {
			args[k] = v
		}
		for k, v := range extra {
			args[k] = v
		}
		result, _ := call(name, args)
		return result
	}
	app("work_continuation_bind", map[string]any{"user_enabled": true})
	app("work_continuation_heartbeat", nil)
	app("work_continuation_state", nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	code := 0
	outcome := protocol.CommandOutcome{EventID: "cycle-event", CommandSessionID: "cycle-command", ExecutionContext: protocol.ExecutionContext{WorkSessionID: session.ID, TargetID: target.Target.ID, ProjectID: project.ID, DeploymentID: deployment.ID, DeploymentRevision: deployment.AppliedRevision, ContextRevision: "context"}, State: protocol.CommandOutcomeCompleted, ExitCode: &code, Stdout: "finished", StartedAt: now, FinishedAt: now, UpdatedAt: now, PendingReport: true}
	if _, err := projects.RecordCommandOutcomes(ctx, deployment.NodeID, []protocol.CommandOutcome{outcome}); err != nil {
		t.Fatal(err)
	}
	control("await", map[string]any{"sources": []protocol.ContinuationSource{{TargetID: target.Target.ID, CommandSessionID: outcome.CommandSessionID}}})
	claim := app("work_wake_acquire", nil)
	attempt := map[string]any{"wake_id": claim["wake_id"], "attempt_id": claim["attempt_id"]}
	prepared := app("work_wake_prepare", attempt)
	message, _ := prepared["automatic_message"].(string)
	index := strings.Index(message, "{")
	if index < 0 {
		t.Fatal("missing resume envelope")
	}
	envelope := map[string]any{}
	if err := json.Unmarshal([]byte(message[index:]), &envelope); err != nil {
		t.Fatal(err)
	}
	call("consume_work_wake", envelope)
	attempt["delivery_status"] = "dispatch_accepted"
	app("work_wake_finish", attempt)
	control("settle", map[string]any{"wake_id": claim["wake_id"], "checkpoint": "Checked durable command outcome; no rerun required."})
	app("work_continuation_pause", nil)
	control("pause", nil)
	control("recover", map[string]any{"confirmed": true})
}
