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
		mcpcontract.ToolProjectContext: false, "workflow_template_manage": false,
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
		if _, hasNodeID := properties["node_id"]; hasNodeID {
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
			node := pairHTTPTestNode(t, store, "device_retired_file_publish", "LegacyDock", "0.8.2", descriptor)
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

func TestRegisterNodeToolsKeepsFirstPublishedContract(t *testing.T) {
	server := &Server{
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}
	first := agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"timeout": map[string]any{"type": "integer"}},
		},
	}
	second := agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"timeout": map[string]any{"type": "number"}},
		},
	}

	server.registerNodeTools(agentdock.Node{ID: "node_old", Version: "1.8.3"}, agentdock.Hello{Tools: []agentdock.ToolDescriptor{first}})
	server.registerNodeTools(agentdock.Node{ID: "node_new", Version: "1.9.0"}, agentdock.Hello{Tools: []agentdock.ToolDescriptor{second}})

	published, ok := server.publishedNodeTool("exec_command")
	if !ok {
		t.Fatal("exec_command was not published")
	}
	firstHash, _ := toolContractHash(first)
	if published.ContractHash != firstHash || len(published.AcceptedSemanticHashes) != 1 || published.AcceptedSemanticHashes[0] != firstHash {
		t.Fatalf("published contract changed: %#v", published)
	}
}

func TestCallNodeToolReturnsContractMismatchBeforeInvoke(t *testing.T) {
	store, projects := newNodeRoutingTestStores(t)
	target := pairHTTPTestNode(t, store, "device_abcdefgh", "DockAir", "1.9.0", agentdock.ToolDescriptor{
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
		t.Fatalf("result should be an MCP tool error: %#v", result)
	}
	details, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
	if details["code"] != "TOOL_CONTRACT_MISMATCH" {
		t.Fatalf("code = %#v", details["code"])
	}
	if details["error"] != "目标 AgentDock 的工具契约不在 Nexus 当前已发布的兼容集合中，请刷新 GPT 工具；若仍不一致，请检查相关设备的 AgentDock 版本或工具契约。" {
		t.Fatalf("error = %#v", details["error"])
	}
	differences, ok := details["differences"].([]any)
	if !ok || len(differences) != 1 {
		t.Fatalf("differences = %#v", details["differences"])
	}
	difference := differences[0].(map[string]any)
	if difference["path"] != "inputSchema.properties.timeout.type" || difference["published"] != "integer" || difference["node"] != "number" {
		t.Fatalf("difference = %#v", difference)
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
	target := pairHTTPTestNode(t, store, "device_ijklmnop", "DockWin", "1.8.3", targetDescriptor)
	server := &Server{
		agentDock:    store,
		agentDockHub: agentdock.NewHub(store),
		projects:     projects,
		mcpServer:    mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:     make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(agentdock.Node{ID: "node_source", Version: "1.8.3"}, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})
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
	node := pairHTTPTestNode(t, store, "device_project_revision_independent", "RevisionIndependent", "1.8.3", descriptor)
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
	linuxNode := pairHTTPTestNode(t, store, "device_target_linux", "Linux", "2.0.0", linux)
	windowsNode := pairHTTPTestNode(t, store, "device_target_windows", "Windows", "2.0.0", windows)
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

func TestRegisterNodeToolsPromotesOnlyAfterProvidersConverge(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	oldDescriptor := agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"timeout": map[string]any{"type": "integer"}},
		},
	}
	newDescriptor := agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"timeout": map[string]any{"type": "number"}},
		},
	}
	first := pairHTTPTestNode(t, store, "device_qrstuvwx", "DockMini", "1.8.3", oldDescriptor)
	second := pairHTTPTestNode(t, store, "device_yzabcdef", "DockAir", "1.8.3", oldDescriptor)
	server := &Server{
		agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(first, agentdock.Hello{Tools: []agentdock.ToolDescriptor{oldDescriptor}})
	oldHash, _ := toolContractHash(oldDescriptor)
	newHash, _ := toolContractHash(newDescriptor)

	first = updateHTTPTestNodeContract(t, store, first, "1.9.0", newDescriptor)
	server.registerNodeTools(first, agentdock.Hello{Tools: []agentdock.ToolDescriptor{newDescriptor}})
	published, _ := server.publishedNodeTool("exec_command")
	if published.ContractHash != oldHash {
		t.Fatalf("mixed providers changed public contract: %#v", published)
	}

	second = updateHTTPTestNodeContract(t, store, second, "1.9.0", newDescriptor)
	server.registerNodeTools(second, agentdock.Hello{Tools: []agentdock.ToolDescriptor{newDescriptor}})
	published, _ = server.publishedNodeTool("exec_command")
	if published.ContractHash != newHash || len(published.AcceptedSemanticHashes) != 1 || published.AcceptedSemanticHashes[0] != newHash {
		t.Fatalf("converged providers did not promote new contract: %#v", published)
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
	node := pairHTTPTestNode(t, store, "device_retire_tool", "DockMini", "1.9.0", descriptor)
	server := &Server{
		agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})

	updated, err := store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: "1.9.1", ProtocolVersion: agentdock.ConnectionProtocolVersion, UIResources: []agentdock.UIResourceCapability{},
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

func TestReconcileKeepsToolWhenLastProviderIsDisabled(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	descriptor := agentdock.ToolDescriptor{
		Name:        "exec_command",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	node := pairHTTPTestNode(t, store, "device_disabled_provider", "DockMini", "1.9.0", descriptor)
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
	server.reconcileNodeToolContracts([]string{"exec_command"})
	if _, ok := server.publishedNodeTool("exec_command"); !ok {
		t.Fatal("disabled provider should keep its published tool contract")
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
	node := pairHTTPTestNode(t, store, "device_restart_current", "CurrentNode", "2.0.0", descriptor)
	previous, err := store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: "1.9.0", ProtocolVersion: "3",
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
		DeviceID: node.DeviceID, Version: "2.0.0", ProtocolVersion: agentdock.ConnectionProtocolVersion,
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
		ToolName: "browser_act", Descriptor: descriptor, SourceNodeID: "node_gone", SourceVersion: "1.8.3",
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
	linuxNode := pairHTTPTestNode(t, store, "device_platform_linux", "DockLinux", "2.0.0", linuxDescriptor)
	windowsNode := pairHTTPTestNode(t, store, "device_platform_windows", "DockWin", "2.0.0", windowsDescriptor)
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
	for _, node := range []agentdock.Node{linuxNode, windowsNode} {
		mismatch, err := server.nodeToolContractMismatch(t.Context(), node, "exec_command")
		if err != nil {
			t.Fatal(err)
		}
		if mismatch != nil {
			t.Fatalf("compatible platform variant %s rejected: %#v", node.Name, mismatch)
		}
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
	restored, ok := restarted.publishedNodeTool("exec_command")
	if !ok || restored.ContractHash != published.ContractHash || !reflect.DeepEqual(restored.AcceptedSemanticHashes, published.AcceptedSemanticHashes) {
		t.Fatalf("restart did not restore full fleet generation: before=%#v after=%#v", published, restored)
	}
}

func TestNodeToolContractRequiresAcceptedVariantMembership(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	variantA := platformContractDescriptor("exec_command", map[string]any{
		"command": map[string]any{"type": "string"},
		"alpha":   map[string]any{"type": "string"},
	}, []any{"command"})
	variantB := platformContractDescriptor("exec_command", map[string]any{
		"command": map[string]any{"type": "string"},
		"beta":    map[string]any{"type": "string"},
	}, []any{"command"})
	nodeA := pairHTTPTestNode(t, store, "device_variant_a", "DockMini", "1.8.3", variantA)
	nodeB := pairHTTPTestNode(t, store, "device_variant_b", "DockWin", "1.9.0", variantB)
	server := &Server{
		agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil),
		mcpTools:  make(map[string]publishedNodeTool),
	}
	server.registerNodeTools(nodeA, agentdock.Hello{Tools: []agentdock.ToolDescriptor{variantA}})
	server.registerNodeTools(nodeB, agentdock.Hello{Tools: []agentdock.ToolDescriptor{variantB}})

	published, _ := server.publishedNodeTool("exec_command")
	publicHash, _ := toolContractHash(published.Descriptor)
	if containsToolContractHash(published.AcceptedSemanticHashes, publicHash) {
		t.Fatalf("test requires a synthetic public contract distinct from provider variants: %#v", published)
	}

	// 节点现场漂移成刚好等于 Fleet 并集，也不能因为公共 schema 能覆盖就绕过 generation membership。
	driftedNode := updateHTTPTestNodeContract(t, store, nodeA, "2.0.0", published.Descriptor)
	driftedHash, _ := toolContractHash(published.Descriptor)
	if driftedHash != published.ContractHash {
		t.Fatalf("drifted hash = %s, public hash = %s", driftedHash, published.ContractHash)
	}
	mismatch, err := server.nodeToolContractMismatch(t.Context(), driftedNode, "exec_command")
	if err != nil {
		t.Fatal(err)
	}
	if mismatch == nil || mismatch.Code != "TOOL_CONTRACT_MISMATCH" {
		t.Fatalf("unaccepted live variant was not rejected: %#v", mismatch)
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
