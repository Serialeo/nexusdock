package httpx

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/config"
)

func TestNonCurrentNodesAreAbsentFromModelEntrypoints(t *testing.T) {
	for _, version := range []string{"0.9.4", "99.0.0", "", "v" + agentdock.RequiredVersion} {
		t.Run(version, func(t *testing.T) {
			store, projects := newNodeRoutingTestStores(t)
			descriptor := platformContractDescriptor("exec_command", map[string]any{"command": map[string]any{"type": "string"}}, nil)
			node := pairHTTPTestNode(t, store, "device_invisible_version", "Hidden old node", version, descriptor)
			server := &Server{agentDock: store, agentDockHub: agentdock.NewHub(store), projects: projects, cfg: config.Config{MCPAppsEnabled: true}}
			ctx, route := bindNodeRoutingTargetForTest(t, projects, node)
			session, err := projects.GetWorkSession(ctx, "mcp:test-owner", route["work_session_id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			server.registerNodeTools(node, agentdock.Hello{Tools: []agentdock.ToolDescriptor{descriptor}})
			if len(server.publishedNodeToolNames()) != 0 {
				t.Fatal("non-current tool was published")
			}
			fleet, err := server.callFleetAgentDockContextWithTimeout(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(fleet["nodes"].([]any)) != 0 {
				t.Fatalf("non-current node appeared in fleet: %#v", fleet)
			}
			opened, err := server.callNodeOpen(ctx, map[string]any{"node_id": node.ID, "client_request_id": "hidden-node"})
			if err == nil || opened["code"] != "AGENTDOCK_NODE_NOT_FOUND" {
				t.Fatalf("node_open disclosed node: %#v %v", opened, err)
			}
			assertNoHiddenNodeDetails(t, opened, node.ID, node.Name)
			list, err := server.callProjectList(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range list["projects"].([]map[string]any) {
				if item["deployment_count"] != 0 || item["available_deployment_count"] != 0 {
					t.Fatalf("old deployment counted: %#v", item)
				}
			}
			opened, err = server.callProjectOpen(ctx, map[string]any{"project_id": session.ProjectID, "client_request_id": "hidden-project"})
			if err != nil {
				t.Fatal(err)
			}
			if len(opened["deployments"].([]map[string]any)) != 0 || len(opened["targets"].([]protocol.WorkTarget)) != 0 {
				t.Fatalf("old deployment appeared: %#v", opened)
			}
			replayed, err := server.projectOpenResult(ctx, "mcp:test-owner", session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(replayed["deployments"].([]map[string]any)) != 0 || len(replayed["targets"].([]protocol.WorkTarget)) != 0 {
				t.Fatalf("history disclosed old target: %#v", replayed)
			}
			refreshed, err := server.callProjectContext(ctx, route)
			if err == nil {
				t.Fatalf("old target context was accepted: %#v", refreshed)
			}
			assertNoHiddenNodeDetails(t, refreshed, node.ID, node.Name)
			called, err := server.callNodeTool(ctx, "exec_command", route)
			if err != nil || !called.IsError {
				t.Fatalf("old target call was accepted: %#v %v", called, err)
			}
			assertNoHiddenNodeDetails(t, called, node.ID, node.Name)
			for _, name := range continuationToolNames() {
				result, err := server.callWorkContinuation(ctx, name, map[string]any{"work_session_id": session.ID, "action": "status"})
				if err == nil || result["code"] != "WORK_CONTINUATION_DENIED" {
					t.Fatalf("%s disclosed old session: %#v %v", name, result, err)
				}
				assertNoHiddenNodeDetails(t, result, node.ID, node.Name, route["target_id"].(string))
			}
		})
	}
}

func TestNonCurrentNodeCannotPublishOrServeMCPAppResource(t *testing.T) {
	store := newHTTPTestAgentDockStore(t)
	descriptor := platformContractDescriptor("file_edit", map[string]any{}, nil)
	node := pairHTTPTestNode(t, store, "device_hidden_resource", "Hidden resource", "0.9.4", descriptor)
	node, err := store.UpdateHello(t.Context(), node.ID, agentdock.Hello{
		DeviceID: node.DeviceID, Version: node.Version, ProtocolVersion: agentdock.ConnectionProtocolVersion,
		UIResources: []agentdock.UIResourceCapability{{URI: protocol.FileChangeUIResourceURI, Contract: protocol.FileChangeUIContract, MIMEType: protocol.MCPAppMIMEType}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{agentDock: store, agentDockHub: agentdock.NewHub(store), cfg: config.Config{MCPAppsEnabled: true}}
	uris, err := server.publishedMCPAppResourceURIs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := uris[protocol.FileChangeUIResourceURI]; found {
		t.Fatal("old resource advertised from persisted Hello")
	}
	result, err := server.readMCPAppResourceWithTimeout(t.Context(), []agentdock.Node{node}, protocol.FileChangeUIResourceURI, 0)
	if err == nil || result != nil {
		t.Fatalf("old resource was served: %#v %v", result, err)
	}
}

func assertNoHiddenNodeDetails(t *testing.T, value any, hidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range hidden {
		if strings.Contains(string(encoded), item) {
			t.Fatalf("hidden node detail %q leaked: %s", item, encoded)
		}
	}
}
