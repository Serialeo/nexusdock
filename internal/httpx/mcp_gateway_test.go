package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	mcpcontract "github.com/Serialeo/agentdock-protocol/mcpcontract"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/core"
	projectstore "github.com/uvwt/nexusdock/internal/project"
	"github.com/uvwt/nexusdock/internal/recall"
	"github.com/uvwt/nexusdock/internal/versioning"
)

func TestToolArgumentsReportsMalformedJSONDetails(t *testing.T) {
	raw := json.RawMessage(`{"path":`)
	_, err := toolArguments(&mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Arguments: raw},
	})
	if err == nil {
		t.Fatal("malformed tool arguments were accepted")
	}
	message := err.Error()
	for _, want := range []string{fmt.Sprintf("(%d bytes, offset %d)", len(raw), len(raw)), "unexpected end of JSON input"} {
		if !strings.Contains(message, want) {
			t.Fatalf("malformed argument error %q does not contain %q", message, want)
		}
	}
}

func TestGatewayToolResultPreservesCompactFileEditEnvelope(t *testing.T) {
	diff := strings.Repeat("+large diff line\n", 700)
	result, err := gatewayToolResult("file_edit", map[string]any{
		"isError": false,
		"structuredContent": map[string]any{
			"action": "add", "path": "transform_full_multikappa.py", "changed": true,
			"files_changed": 1, "insertions": 217, "deletions": 0,
			"summary": "updated transform_full_multikappa.py", "diff_preview": diff,
		},
		"content": []map[string]any{{"type": "text", "text": "updated transform_full_multikappa.py"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || content.Text != "updated transform_full_multikappa.py" {
		t.Fatalf("proxied content = %#v", result.Content)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(encoded), "+large diff line"); got != 700 {
		t.Fatalf("proxied file_edit diff marker count = %d, want 700", got)
	}
}

func TestInitializeMCPGatewayHasNoBuiltInInstructions(t *testing.T) {
	server := &Server{mcpTools: make(map[string]publishedNodeTool), mcpResources: make(map[string]struct{})}
	server.initializeMCPGateway()

	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.mcpServer.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "nexusdock-test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result := clientSession.InitializeResult()
	if result == nil {
		t.Fatal("InitializeResult is nil")
	}
	if result.Instructions != "" {
		t.Fatalf("Instructions = %q, want empty built-in instructions", result.Instructions)
	}
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	central := map[string]bool{
		"agentdock_context": false, mcpcontract.ToolProjectList: false, mcpcontract.ToolProjectOpen: false,
		mcpcontract.ToolNodeOpen: false, mcpcontract.ToolProjectContext: false, "workflow_template_manage": false,
	}
	for _, tool := range tools.Tools {
		if tool.Name == "node_list" {
			t.Fatal("tools/list still exposes node_list")
		}
		if _, expected := central[tool.Name]; !expected {
			continue
		}
		central[tool.Name] = true
		input := tool.InputSchema.(map[string]any)
		properties := input["properties"].(map[string]any)
		if _, hasNodeID := properties["node_id"]; hasNodeID && tool.Name != mcpcontract.ToolNodeOpen {
			t.Fatalf("central tool %s unexpectedly requires node_id: %#v", tool.Name, input)
		}
	}
	for name, found := range central {
		if !found {
			t.Fatalf("tools/list missing central tool %s", name)
		}
	}
}

func TestInitializeMCPGatewayHasNoLegacyInstructionsChannel(t *testing.T) {
	server := &Server{mcpTools: make(map[string]publishedNodeTool), mcpResources: make(map[string]struct{})}
	server.initializeMCPGateway()
	if got := initializeInstructions(t, server.currentMCPServer()); got != "" {
		t.Fatalf("MCP initialize unexpectedly exposed legacy Instructions: %q", got)
	}
}

func initializeInstructions(t *testing.T, server *mcpsdk.Server) string {
	t.Helper()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "nexusdock-instructions-test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result := clientSession.InitializeResult()
	if result == nil {
		t.Fatal("InitializeResult is nil")
	}
	return result.Instructions
}

func TestNodeInputSchemaRequiresWorkSessionTargetAndRemovesRoutingOverrides(t *testing.T) {
	schema := nodeInputSchema(map[string]any{
		"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string"}, "node_id": map[string]any{"type": "string"},
			"working_folder": map[string]any{"type": "string"}, "permissions": map[string]any{"type": "object"},
		}, "required": []any{"path", "node_id"},
	})
	properties := schema["properties"].(map[string]any)
	for _, forbidden := range []string{"node_id", "working_folder", "permissions"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("routing override %s remained model-facing: %#v", forbidden, schema)
		}
	}
	required := schema["required"].([]any)
	if !reflect.DeepEqual(required, []any{"path", "work_session_id", "target_id"}) {
		t.Fatalf("required = %#v", required)
	}
}

func TestRecallUpdateFactPreviewsAndWrites(t *testing.T) {
	store, err := recall.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(recall.WriteRequest{Path: "profile.md", Content: "# Profile\n\neditor: old\n", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	manager := versioning.NewManager(store.Root(), slog.Default())
	server := &Server{store: store, versions: manager}
	preview, err := server.updateRecallFacts(t.Context(), "profile.md", map[string]any{"key": "editor", "value": "new"})
	if err != nil || preview["dry_run"] != true {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	unchanged, _ := store.Read("profile.md")
	if strings.Contains(unchanged.Content, "editor: new") {
		t.Fatal("preview mutated Recall")
	}
	result, err := server.updateRecallFacts(t.Context(), "profile.md", map[string]any{"key": "editor", "value": "new", "confirmed": true})
	if err != nil || result["written"] != true {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	updated, _ := store.Read("profile.md")
	if !strings.Contains(updated.Content, "editor:new") {
		t.Fatalf("updated content = %q", updated.Content)
	}
}

func TestUnavailableNodeToolsAreNeverPublishedOrRouted(t *testing.T) {
	for _, name := range []string{"file_publish"} {
		t.Run(name, func(t *testing.T) {
			store := newHTTPTestAgentDockStore(t)
			descriptor := agentdock.ToolDescriptor{
				Name:        name,
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			}
			node := pairHTTPTestNode(t, store, "device_retired_file_publish", "LegacyDock", agentdock.RequiredVersion, descriptor)
			server := &Server{
				agentDock: store,
				mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
				mcpTools:  make(map[string]publishedNodeTool),
			}
			if err := store.SavePublishedToolContract(t.Context(), agentdock.PublishedToolContract{ToolName: descriptor.Name, Descriptor: descriptor}); err != nil {
				t.Fatal(err)
			}
			server.mcpTools[descriptor.Name] = publishedNodeTool{Descriptor: descriptor}
			server.mcpServer.AddTool(nodeMCPTool(descriptor), server.nodeToolHandler(descriptor.Name))

			server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})
			if _, ok := server.publishedNodeTool(descriptor.Name); ok {
				t.Fatal("unavailable tool was published from Node Hello")
			}
			contracts, err := store.ListPublishedToolContracts(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, contract := range contracts {
				if contract.ToolName == descriptor.Name {
					t.Fatalf("unavailable tool persisted in fleet catalog: %#v", contracts)
				}
			}
			result, err := server.callNodeTool(t.Context(), descriptor.Name, map[string]any{"path": "README.md"})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError {
				t.Fatalf("unavailable tool direct routing unexpectedly succeeded: %#v", result)
			}
			details := result.StructuredContent.(map[string]any)
			if details["code"] != "UNKNOWN_TOOL" {
				t.Fatalf("unavailable tool routing error = %#v", details)
			}
		})
	}
}

func TestRegisterNodeToolsReplacesCurrentSnapshotWithoutKeepingOldContract(t *testing.T) {
	server := &Server{mcpTools: make(map[string]publishedNodeTool)}
	node := agentdock.Node{ID: "node_current", Enabled: true, Version: agentdock.RequiredVersion, ProtocolVersion: agentdock.ConnectionProtocolVersion}
	first := platformContractDescriptor("exec_command", map[string]any{"timeout": map[string]any{"type": "integer"}}, nil)
	second := platformContractDescriptor("exec_command", map[string]any{"timeout": map[string]any{"type": "number"}}, nil)
	server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{first}})
	server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{second}})
	published, ok := server.publishedNodeTool("exec_command")
	secondHash, _ := toolContractHash(second)
	if !ok || published.ContractHash != secondHash {
		t.Fatalf("current snapshot did not replace old contract: %#v", published)
	}
}

func TestCallNodeToolUsesTargetSchemaWhenPublishedContractDiffers(t *testing.T) {
	store, projects := newNodeRoutingTestStores(t)
	target := pairHTTPTestNode(t, store, "device_abcdefgh", "DockAir", agentdock.RequiredVersion, agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"timeout": map[string]any{"type": "number"}},
		},
	})
	published := agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"timeout": map[string]any{"type": "integer"}},
		},
	}
	server := &Server{
		agentDock:    store,
		agentDockHub: agentdock.NewHub(store),
		projects:     projects,
		mcpServer:    mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:     make(map[string]publishedNodeTool),
	}
	// 模拟客户端仍持有旧公开契约；注册流程只读取真实节点的持久化 Hello 快照。
	publishedHash, err := toolContractHash(published)
	if err != nil {
		t.Fatal(err)
	}
	server.mcpTools[published.Name] = publishedNodeTool{
		Descriptor: published, ContractHash: publishedHash, AcceptedSemanticHashes: []string{publishedHash},
	}
	ctx, route := bindNodeRoutingTargetForTest(t, projects, target)
	route["timeout"] = 1
	result, err := server.callNodeTool(ctx, "exec_command", route)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("offline target should prove invocation was attempted: %#v", result)
	}
	details, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
	if details["error"] != agentdock.ErrNodeOffline.Error() {
		t.Fatalf("published hash blocked target-valid call: %#v", details)
	}
}

func TestCallNodeToolAcceptsSameContractWithDifferentDescription(t *testing.T) {
	store, projects := newNodeRoutingTestStores(t)
	descriptor := agentdock.ToolDescriptor{
		Name: "read_file", Description: "macOS description",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "macOS path"}}},
	}
	targetDescriptor, err := cloneToolDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	targetDescriptor.Description = "Windows description"
	targetDescriptor.InputSchema["properties"].(map[string]any)["path"].(map[string]any)["description"] = "Windows path"
	target := pairHTTPTestNode(t, store, "device_ijklmnop", "DockWin", agentdock.RequiredVersion, targetDescriptor)
	server := &Server{
		agentDock:    store,
		agentDockHub: agentdock.NewHub(store),
		projects:     projects,
		mcpServer:    mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:     make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(agentdock.Node{ID: "node_source", Enabled: true, Version: agentdock.RequiredVersion, ProtocolVersion: agentdock.ConnectionProtocolVersion}, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})
	ctx, route := bindNodeRoutingTargetForTest(t, projects, target)
	route["path"] = "/tmp/a"
	result, err := server.callNodeTool(ctx, "read_file", route)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("offline target should prove invocation was attempted: %#v", result)
	}
	details := result.StructuredContent.(map[string]any)
	if details["error"] != agentdock.ErrNodeOffline.Error() {
		t.Fatalf("expected compatible contract to reach hub, got %#v", details)
	}
}

func TestCallNodeToolIgnoresUnrelatedProjectRowRevisionChanges(t *testing.T) {
	store, projects := newNodeRoutingTestStores(t)
	descriptor := agentdock.ToolDescriptor{
		Name:        "read_file",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
	}
	node := pairHTTPTestNode(t, store, "device_project_revision_independent", "RevisionIndependent", agentdock.RequiredVersion, descriptor)
	server := &Server{
		agentDock: store, agentDockHub: agentdock.NewHub(store), projects: projects,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil), mcpTools: make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})
	ctx, route := bindNodeRoutingTargetForTest(t, projects, node)
	session, err := projects.GetWorkSession(t.Context(), "mcp:test-owner", route["work_session_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	project, err := projects.GetProject(t.Context(), session.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.UpdateProject(t.Context(), project.ID, projectstore.UpdateProjectInput{
		ExpectedRevision: project.Revision, Name: project.Name + " renamed", OrchestrationPolicy: "new collaboration guidance", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	route["path"] = "/tmp/a"
	result, err := server.callNodeTool(ctx, "read_file", route)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("offline test target unexpectedly succeeded: %#v", result)
	}
	details := result.StructuredContent.(map[string]any)
	if details["error"] != agentdock.ErrNodeOffline.Error() {
		t.Fatalf("unrelated Project edit blocked Target before invocation: %#v", details)
	}
}

func TestCallNodeToolValidatesArgumentsAgainstTargetNodeVariantBeforeInvoke(t *testing.T) {
	store, projects := newNodeRoutingTestStores(t)
	linux := platformContractDescriptor("exec_command", map[string]any{
		"command": map[string]any{"type": "string"},
	}, []any{"command"})
	windows := platformContractDescriptor("exec_command", map[string]any{
		"command":       map[string]any{"type": "string"},
		"windows_shell": map[string]any{"type": "string", "enum": []any{"powershell", "cmd"}},
	}, []any{"command"})
	linuxNode := pairHTTPTestNode(t, store, "device_target_linux", "Linux", agentdock.RequiredVersion, linux)
	windowsNode := pairHTTPTestNode(t, store, "device_target_windows", "Windows", agentdock.RequiredVersion, windows)
	server := &Server{
		agentDock:    store,
		agentDockHub: agentdock.NewHub(store),
		projects:     projects,
		mcpServer:    mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:     make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(linuxNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{linux}})
	server.registerNodeTools(windowsNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{windows}})

	published, ok := server.publishedNodeTool("exec_command")
	if !ok {
		t.Fatal("exec_command was not published")
	}
	if _, ok := published.Descriptor.InputSchema["properties"].(map[string]any)["windows_shell"]; !ok {
		t.Fatalf("test requires a Fleet union schema containing a Windows-only property: %#v", published.Descriptor.InputSchema)
	}
	ctx, route := bindNodeRoutingTargetForTest(t, projects, linuxNode)
	route["command"] = "pwd"
	route["windows_shell"] = "powershell"
	result, err := server.callNodeTool(ctx, "exec_command", route)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("Linux target accepted Windows-only Fleet argument: %#v", result)
	}
	details := result.StructuredContent.(map[string]any)
	if details["code"] != "TARGET_TOOL_ARGUMENT_INVALID" || details["target_id"] != route["target_id"] {
		t.Fatalf("target validation error = %#v", details)
	}

	// A request valid for the Linux variant proceeds to the hub. The node is deliberately offline,
	// so ErrNodeOffline proves Nexus did not reject the target-local schema-valid arguments.
	delete(route, "windows_shell")
	result, err = server.callNodeTool(ctx, "exec_command", route)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent.(map[string]any)["error"] != agentdock.ErrNodeOffline.Error() {
		t.Fatalf("target-local valid arguments did not reach the hub: %#v", result)
	}
}

func TestCallNodeToolChecksCurrentTargetVisibilityBeforeInvoke(t *testing.T) {
	store, projects := newNodeRoutingTestStores(t)
	targetDescriptor := platformContractDescriptor("controller_test", map[string]any{}, nil)
	targetDescriptor.Meta = map[string]any{"ui": map[string]any{"visibility": []string{"app"}}}
	target := pairHTTPTestNode(t, store, "device_target_app_only", "AppOnly", agentdock.RequiredVersion, targetDescriptor)
	publicDescriptor, err := cloneToolDescriptor(targetDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	publicDescriptor.Meta = nil
	publicHash, _ := toolContractHash(publicDescriptor)
	server := &Server{
		agentDock: store, agentDockHub: agentdock.NewHub(store), projects: projects,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools: map[string]publishedNodeTool{publicDescriptor.Name: {
			Descriptor: publicDescriptor, ContractHash: publicHash, AcceptedSemanticHashes: []string{publicHash},
		}},
	}
	ctx, route := bindNodeRoutingTargetForTest(t, projects, target)
	result, err := server.callNodeTool(ctx, targetDescriptor.Name, route)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("app-only target was invoked through stale model-visible contract: %#v", result)
	}
	details := result.StructuredContent.(map[string]any)
	if details["code"] != "TOOL_NOT_AVAILABLE" || details["target_id"] != route["target_id"] {
		t.Fatalf("target visibility error = %#v", details)
	}
}

func newHTTPTestAgentDockStore(t *testing.T) *agentdock.Store {
	t.Helper()
	db, err := core.OpenSQLite(t.Context(), ":memory:", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.EnsureSchema(t.Context(), db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	store, err := agentdock.NewStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store
}

func newNodeRoutingTestStores(t *testing.T) (*agentdock.Store, *projectstore.Store) {
	t.Helper()
	db, err := core.OpenSQLite(t.Context(), ":memory:", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.EnsureSchema(t.Context(), db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	nodes, err := agentdock.NewStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	projects, err := projectstore.NewStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return nodes, projects
}

func bindNodeRoutingTargetForTest(t *testing.T, projects *projectstore.Store, node agentdock.Node) (context.Context, map[string]any) {
	t.Helper()
	project, err := projects.CreateProject(t.Context(), projectstore.CreateProjectInput{Name: "Gateway Project " + node.ID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	permissions := protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadWrite, Shell: true, Browser: true, DynamicMCP: true, ACP: true}
	deployment, err := projects.CreateDeployment(t.Context(), projectstore.CreateDeploymentInput{
		ProjectID: project.ID, NodeID: node.ID, WorkingFolder: "/tmp/nexusdock-gateway-test", Permissions: permissions, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = projects.RecordApplyResult(t.Context(), project.ID, deployment.ID, deployment.DesiredRevision, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := "mcp:test-owner"
	session, _, err := projects.BeginWorkSession(t.Context(), owner, project.ID, "request-"+node.ID, "sha256:gateway-request-"+node.ID, project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target, err := projects.PutWorkTarget(t.Context(), owner, projectstore.WorkTarget{Target: protocol.WorkTarget{
		WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: deployment.ID, NodeID: node.ID, CWDRel: ".",
		DeploymentRevision: deployment.AppliedRevision, ContextRevision: "sha256:gateway-context-" + node.ID, Status: protocol.TargetReady,
		Permissions: permissions, Prompt: protocol.ProjectPrompt{PromptRevision: "sha256:gateway-prompt", Complete: true, Sources: []protocol.PromptSource{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.UpdateWorkSessionContext(t.Context(), owner, session.ID, project.Revision, protocol.WorkSessionReady, "sha256:gateway-session"); err != nil {
		t.Fatal(err)
	}
	ctx := withMCPClientBinding(t.Context(), owner)
	return ctx, map[string]any{"work_session_id": session.ID, "target_id": target.Target.ID}
}

func pairHTTPTestNode(t *testing.T, store *agentdock.Store, deviceID, name, version string, descriptor agentdock.ToolDescriptor) agentdock.Node {
	t.Helper()
	pairing, err := store.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.Pair(t.Context(), agentdock.PairInput{Code: pairing.Code, DeviceID: deviceID, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	node, err = store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: version, ProtocolVersion: agentdock.ConnectionProtocolVersion,
		Capabilities: []string{descriptor.Name}, Tools: []agentdock.ToolDescriptor{descriptor}, UIResources: []agentdock.UIResourceCapability{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestLegacyAgentDockGuidanceIsNotACentralTool(t *testing.T) {
	for _, tool := range nexusToolDefinitions() {
		if tool.Name == "agentdock_guidance" {
			t.Fatal("legacy agentdock_guidance unexpectedly remained model-facing")
		}
	}
}

func TestOlderNodeCannotLimitCurrentToolContract(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	old := platformContractDescriptor("exec_command", map[string]any{"timeout": map[string]any{"type": "integer"}}, nil)
	current := platformContractDescriptor("exec_command", map[string]any{"timeout": map[string]any{"type": "number"}}, nil)
	oldNode := pairHTTPTestNode(t, store, "device_old_contract", "Old", "0.9.4", old)
	currentNode := pairHTTPTestNode(t, store, "device_current_contract", "Current", agentdock.RequiredVersion, current)
	server := &Server{agentDock: store, mcpTools: make(map[string]publishedNodeTool)}
	for _, oldFirst := range []bool{true, false} {
		if oldFirst {
			server.registerNodeTools(oldNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{old}})
		}
		server.registerNodeTools(currentNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{current}})
		server.registerNodeTools(oldNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{old}})
		published, ok := server.publishedNodeTool("exec_command")
		currentHash, _ := toolContractHash(current)
		if !ok || published.ContractHash != currentHash || len(published.AcceptedSemanticHashes) != 1 {
			t.Fatalf("older node limited current schema: %#v", published)
		}
	}
	server.handleAgentDockDisconnect(currentNode.ID)
	if _, ok := server.publishedNodeTool("exec_command"); ok {
		t.Fatal("older node restored retired tool after current node disconnected")
	}
}

func TestRegisterNodeToolsRetiresToolWhenLastProviderDropsCapability(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	descriptor := agentdock.ToolDescriptor{
		Name: "browser_act",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"session_id": map[string]any{"type": "string"}},
		},
	}
	node := pairHTTPTestNode(t, store, "device_retire_tool", "DockMini", agentdock.RequiredVersion, descriptor)
	server := &Server{
		agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})

	updated, err := store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: agentdock.RequiredVersion, ProtocolVersion: agentdock.ConnectionProtocolVersion, UIResources: []agentdock.UIResourceCapability{},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.registerNodeTools(updated, agentdock.Hello{})
	if _, ok := server.publishedNodeTool("browser_act"); ok {
		t.Fatal("browser_act should retire after the last provider drops the capability")
	}
	contracts, err := store.ListPublishedToolContracts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) != 0 {
		t.Fatalf("published contracts after retirement = %#v", contracts)
	}
}

func TestReconcileRetiresToolWhenLastLiveProviderIsDisabled(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	descriptor := agentdock.ToolDescriptor{
		Name:        "exec_command",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	node := pairHTTPTestNode(t, store, "device_disabled_provider", "DockMini", agentdock.RequiredVersion, descriptor)
	server := &Server{
		agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})

	disabled := false
	if _, err := store.Update(t.Context(), node.ID, agentdock.UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	server.handleAgentDockDisconnect(node.ID)
	server.reconcileNodeToolContracts([]string{"exec_command"})
	if _, ok := server.publishedNodeTool("exec_command"); ok {
		t.Fatal("disabled provider retained a stale published tool contract")
	}
}

func updateHTTPTestNodeContract(t *testing.T, store *agentdock.Store, node agentdock.Node, version string, descriptor agentdock.ToolDescriptor) agentdock.Node {
	t.Helper()
	updated, err := store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: version, ProtocolVersion: agentdock.ConnectionProtocolVersion,
		Capabilities: []string{descriptor.Name}, Tools: []agentdock.ToolDescriptor{descriptor}, UIResources: []agentdock.UIResourceCapability{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func TestInitializeMCPGatewayRequiresCurrentProtocolHelloBeforePublishingNodeTools(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	descriptor := agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"timeout": map[string]any{"type": "integer"}},
		},
	}
	node := pairHTTPTestNode(t, store, "device_restart_current", "CurrentNode", agentdock.RequiredVersion, descriptor)
	previous, err := store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: agentdock.RequiredVersion, ProtocolVersion: "3",
		Capabilities: []string{descriptor.Name}, Tools: []agentdock.ToolDescriptor{descriptor}, UIResources: []agentdock.UIResourceCapability{},
	})
	if err != nil {
		t.Fatal(err)
	}
	node = previous
	if err := store.SavePublishedToolContract(t.Context(), agentdock.PublishedToolContract{
		ToolName: descriptor.Name, Descriptor: descriptor, SourceNodeID: node.ID, SourceVersion: "previous-generation",
	}); err != nil {
		t.Fatal(err)
	}

	restarted := &Server{agentDock: store, mcpTools: make(map[string]publishedNodeTool)}
	restarted.initializeMCPGateway()
	if _, ok := restarted.publishedNodeTool(descriptor.Name); ok {
		t.Fatal("previous-generation persisted contract was restored before a current Bridge v4 Hello")
	}
	contracts, err := store.ListPublishedToolContracts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) != 0 {
		t.Fatalf("previous-generation catalog survived startup reset: %#v", contracts)
	}

	current, err := store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: agentdock.RequiredVersion, ProtocolVersion: agentdock.ConnectionProtocolVersion,
		Capabilities: []string{descriptor.Name}, Tools: []agentdock.ToolDescriptor{descriptor}, UIResources: []agentdock.UIResourceCapability{},
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.registerNodeTools(current, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})
	published, ok := restarted.publishedNodeTool(descriptor.Name)
	if !ok {
		t.Fatal("current Bridge v4 Hello did not republish node tool")
	}
	hash, _ := toolContractHash(descriptor)
	if published.ContractHash != hash || !containsToolContractHash(published.AcceptedSemanticHashes, hash) {
		t.Fatalf("republished current contract = %#v", published)
	}
}

func TestInitializeMCPGatewayRetiresPersistedToolWithoutProvider(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	descriptor := agentdock.ToolDescriptor{
		Name:        "browser_act",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	if err := store.SavePublishedToolContract(t.Context(), agentdock.PublishedToolContract{
		ToolName: "browser_act", Descriptor: descriptor, SourceNodeID: "node_gone", SourceVersion: agentdock.RequiredVersion,
	}); err != nil {
		t.Fatal(err)
	}

	server := &Server{agentDock: store, mcpTools: make(map[string]publishedNodeTool)}
	server.initializeMCPGateway()
	if _, ok := server.publishedNodeTool("browser_act"); ok {
		t.Fatal("startup should retire persisted tools that no node provides")
	}
	contracts, err := store.ListPublishedToolContracts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) != 0 {
		t.Fatalf("persisted stale contracts after startup = %#v", contracts)
	}
}

func TestRegisterNodeToolsMergesCompatiblePlatformVariantContracts(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	linuxDescriptor := platformContractDescriptor("exec_command", map[string]any{
		"command": map[string]any{"type": "string"},
		"workdir": map[string]any{"type": "string", "description": "host path"},
	}, []any{"command"})
	windowsDescriptor := platformContractDescriptor("exec_command", map[string]any{
		"command":       map[string]any{"type": "string"},
		"workdir":       map[string]any{"type": "string", "description": "Windows path"},
		"windows_shell": map[string]any{"type": "string", "enum": []any{"powershell", "cmd"}},
	}, []any{"command"})
	linuxNode := pairHTTPTestNode(t, store, "device_platform_linux", "DockLinux", agentdock.RequiredVersion, linuxDescriptor)
	windowsNode := pairHTTPTestNode(t, store, "device_platform_windows", "DockWin", agentdock.RequiredVersion, windowsDescriptor)
	server := &Server{
		agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}

	server.registerNodeTools(linuxNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{linuxDescriptor}})
	server.registerNodeTools(windowsNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{windowsDescriptor}})

	published, ok := server.publishedNodeTool("exec_command")
	if !ok {
		t.Fatal("exec_command was not published")
	}
	properties := published.Descriptor.InputSchema["properties"].(map[string]any)
	if _, ok := properties["windows_shell"]; !ok {
		t.Fatalf("fleet schema did not include compatible optional property: %#v", properties)
	}
	linuxHash, _ := toolContractHash(linuxDescriptor)
	windowsHash, _ := toolContractHash(windowsDescriptor)
	if !containsToolContractHash(published.AcceptedSemanticHashes, linuxHash) || !containsToolContractHash(published.AcceptedSemanticHashes, windowsHash) {
		t.Fatalf("accepted hashes = %#v", published.AcceptedSemanticHashes)
	}
	contracts, err := store.ListPublishedToolContracts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) != 1 || len(contracts[0].AcceptedSemanticHashes) != 2 {
		t.Fatalf("persisted fleet generation = %#v", contracts)
	}
	if contracts[0].SourceNodeID != "" || contracts[0].SourceVersion != "" {
		t.Fatalf("synthetic fleet contract should not claim one provider as source: %#v", contracts[0])
	}

	restarted := &Server{agentDock: store, mcpTools: make(map[string]publishedNodeTool)}
	restarted.initializeMCPGateway()
	if _, ok := restarted.publishedNodeTool("exec_command"); ok {
		t.Fatal("restart restored a contract before any live Hello")
	}
	restarted.registerNodeTools(linuxNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{linuxDescriptor}})
	restarted.registerNodeTools(windowsNode, agentdock.Hello{Tools: []agentdock.ToolDescriptor{windowsDescriptor}})
	restored, ok := restarted.publishedNodeTool("exec_command")
	if !ok || restored.ContractHash != published.ContractHash || !reflect.DeepEqual(restored.AcceptedSemanticHashes, published.AcceptedSemanticHashes) {
		t.Fatalf("live Hello did not rebuild full fleet generation: before=%#v after=%#v", published, restored)
	}
}

func TestResetPersistedNodeToolCatalogDropsPreviousProtocolGeneration(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	for _, name := range []string{"exec_command", "search_text"} {
		descriptor := platformContractDescriptor(name, map[string]any{
			"value": map[string]any{"type": "string", "description": "previous-generation provider presentation"},
		}, nil)
		if err := store.SavePublishedToolContract(t.Context(), agentdock.PublishedToolContract{
			ToolName: name, Descriptor: descriptor, SourceNodeID: "previous_generation", SourceVersion: "0.8.2",
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{
		agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}
	if err := server.resetPersistedNodeToolCatalog(t.Context()); err != nil {
		t.Fatal(err)
	}
	if names := server.publishedNodeToolNames(); len(names) != 0 {
		t.Fatalf("previous-generation tools restored in memory: %#v", names)
	}
	contracts, err := store.ListPublishedToolContracts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(contracts) != 0 {
		t.Fatalf("previous-generation persisted contracts survived reset: %#v", contracts)
	}
}
