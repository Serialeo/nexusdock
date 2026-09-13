package httpx

import (
	"errors"
	"reflect"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/config"
)

func visibilityDescriptor(visibility any) agentdock.ToolDescriptor {
	return agentdock.ToolDescriptor{
		Name: "controller_test", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Meta: map[string]any{"ui": map[string]any{"visibility": visibility, "resourceUri": "ui://test/controller"}},
	}
}

func TestToolVisibilityHashDistinguishesExecutionExposure(t *testing.T) {
	defaultDescriptor := visibilityDescriptor(nil)
	defaultDescriptor.Meta = nil
	defaultHash, err := toolContractHash(defaultDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []any{[]string{"model", "app"}, []any{"app", "model", "app"}} {
		hash, err := toolContractHash(visibilityDescriptor(visibility))
		if err != nil || hash != defaultHash {
			t.Fatalf("equivalent default visibility hash=%s err=%v", hash, err)
		}
	}
	appHash, err := toolContractHash(visibilityDescriptor([]string{"app"}))
	if err != nil {
		t.Fatal(err)
	}
	modelHash, err := toolContractHash(visibilityDescriptor([]string{"model"}))
	if err != nil {
		t.Fatal(err)
	}
	if appHash == modelHash || appHash == defaultHash || modelHash == defaultHash {
		t.Fatal("restricted tool exposure did not change execution hash")
	}
	for _, invalid := range []any{nil, "app", []string{}, []any{"app", 1}, []string{"Model"}, []string{"unknown"}} {
		descriptor := visibilityDescriptor(invalid)
		if _, err := toolContractHash(descriptor); !errors.Is(err, errUnsafeToolVisibility) {
			t.Fatalf("invalid visibility %#v hash error=%v", invalid, err)
		}
		if tool := nodeMCPToolWithApps(descriptor, true); tool != nil {
			t.Fatalf("invalid visibility published: %#v", tool)
		}
	}
}

func TestToolVisibilitySurvivesPresentationDivergence(t *testing.T) {
	first := visibilityDescriptor([]string{"app"})
	second := visibilityDescriptor([]string{"app"})
	second.Meta["ui"].(map[string]any)["resourceUri"] = "ui://test/different"
	merged, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{first, second})
	if err != nil {
		t.Fatal(err)
	}
	ui := merged.Meta["ui"].(map[string]any)
	if ui["resourceUri"] != nil || !reflect.DeepEqual(ui["visibility"], []string{"app"}) {
		t.Fatalf("merged ui=%#v", ui)
	}
	if nodeMCPToolWithApps(merged, false) != nil {
		t.Fatal("disabling Apps exposed app-only tool")
	}
	model := nodeMCPToolWithApps(visibilityDescriptor([]string{"model"}), false)
	if model == nil || !reflect.DeepEqual(model.Meta["ui"], map[string]any{"visibility": []string{"model"}}) {
		t.Fatalf("model restriction lost after Apps disabled: %#v", model)
	}

	// 即便较早的 provider 有 schema 冲突，也必须先发现后续 provider 的 visibility 冲突。
	drift := visibilityDescriptor([]string{"app"})
	drift.InputSchema["type"] = "array"
	modelDescriptor := visibilityDescriptor([]string{"model"})
	if _, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{first, drift, modelDescriptor}); !errors.Is(err, errUnsafeToolVisibility) {
		t.Fatalf("visibility conflict was masked: %v", err)
	}
}

func TestToolVisibilityConflictRetiresPreviouslyPublishedTool(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	app := visibilityDescriptor([]string{"app"})
	first := pairHTTPTestNode(t, store, "device_visfirst", "First", "1", app)
	second := pairHTTPTestNode(t, store, "device_vissecond", "Second", "1", app)
	server := &Server{cfg: config.Config{MCPAppsEnabled: true}, agentDock: store,
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil), mcpTools: make(map[string]publishedNodeTool)}
	server.registerNodeTools(first, agentdock.Hello{Tools: []agentdock.ToolDescriptor{app}})
	if _, ok := server.publishedNodeTool(app.Name); !ok {
		t.Fatal("initial app-only tool missing")
	}
	model := visibilityDescriptor([]string{"model"})
	second = updateHTTPTestNodeContract(t, store, second, "2", model)
	server.registerNodeTools(second, agentdock.Hello{Tools: []agentdock.ToolDescriptor{model}})
	if _, ok := server.publishedNodeTool(app.Name); ok {
		t.Fatal("visibility conflict retained old published tool")
	}
	contracts, err := store.ListPublishedToolContracts(t.Context())
	if err != nil || len(contracts) != 0 {
		t.Fatalf("unsafe persisted contract remains: %#v, %v", contracts, err)
	}
	// 同一旧 provider 再次 Hello 也不能趁无 published 条目重新公开工具。
	server.registerNodeTools(first, agentdock.Hello{Tools: []agentdock.ToolDescriptor{app}})
	if _, ok := server.publishedNodeTool(app.Name); ok {
		t.Fatal("repeated Hello republished conflicting tool")
	}
	second = updateHTTPTestNodeContract(t, store, second, "3", app)
	server.registerNodeTools(second, agentdock.Hello{Tools: []agentdock.ToolDescriptor{app}})
	if _, ok := server.publishedNodeTool(app.Name); !ok {
		t.Fatal("converged visibility did not restore tool")
	}
	invalid := visibilityDescriptor("app")
	second = updateHTTPTestNodeContract(t, store, second, "4", invalid)
	server.registerNodeTools(second, agentdock.Hello{Tools: []agentdock.ToolDescriptor{invalid}})
	if _, ok := server.publishedNodeTool(app.Name); ok {
		t.Fatal("malformed visibility retained old published tool")
	}
}

func TestToolVisibilityAppsToggleRemovesAndRestoresAppOnlyTool(t *testing.T) {
	server := &Server{cfg: config.Config{MCPAppsEnabled: true}, mcpTools: make(map[string]publishedNodeTool), mcpResources: make(map[string]struct{})}
	server.initializeMCPGateway()
	descriptor := visibilityDescriptor([]string{"app"})
	server.registerNodeTools(agentdock.Node{}, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.mcpServer.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "visibility-test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	findSessionTool(t, clientSession, descriptor.Name)
	server.setMCPAppsEnabled(false)
	for tool, err := range clientSession.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == descriptor.Name {
			t.Fatal("active session still lists app-only tool with Apps disabled")
		}
	}
	result, err := server.callNodeTool(t.Context(), descriptor.Name, nil)
	if err != nil || !result.IsError {
		t.Fatalf("stale app-only call accepted: %#v %v", result, err)
	}
	server.setMCPAppsEnabled(true)
	findSessionTool(t, clientSession, descriptor.Name)
}
