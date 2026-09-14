package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/agentdock"
)

func TestBuiltinSnapshotReplacesFleetToolsAndNotifiesConnectedClient(t *testing.T) {
	s := newNodeTestServer(t)
	s.mcpTools = make(map[string]publishedNodeTool)
	s.initializeMCPGateway()
	pairing, err := s.agentDock.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.agentDock.Pair(t.Context(), agentdock.PairInput{Code: pairing.Code, DeviceID: "builtin-device", Name: "Builtins"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := protocol.ToolDescriptor{Name: "browser_session", Description: "browser test", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, OutputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}
	socket, closeNode := connectProjectFakeNodeWithTools(t, s, node, []protocol.ToolDescriptor{descriptor})
	defer closeNode()
	httpServer := httptest.NewServer(s.mcpHandler)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	changes := make(chan struct{}, 16)
	ack := make(chan struct{}, 1)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "builtin-test", Version: "1"}, &mcpsdk.ClientOptions{ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) { changes <- struct{}{} }})
	client.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "notifications/subscriptions/acknowledged" {
				select {
				case ack <- struct{}{}:
				default:
				}
			}
			return next(ctx, method, req)
		}
	})
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case <-ack:
	case <-ctx.Done():
		t.Fatal("no subscription")
	}
	for _, enabled := range []bool{false, true, false} {
		for len(changes) > 0 {
			<-changes
		}
		hello := &protocol.Hello{DeviceID: node.DeviceID, ProtocolVersion: protocol.ConnectionProtocolVersion, Tools: []protocol.ToolDescriptor{}, Capabilities: []string{}, UIResources: []protocol.UIResourceCapability{}}
		if enabled {
			hello.Tools = []protocol.ToolDescriptor{descriptor}
			hello.Capabilities = []string{descriptor.Name}
		}
		if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageNodeUpdated, ProtocolVersion: protocol.ConnectionProtocolVersion, Hello: hello}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changes:
		case <-ctx.Done():
			t.Fatal("fleet did not notify capability change")
		}
		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range listed.Tools {
			if tool.Name == descriptor.Name {
				found = true
			}
		}
		if found != enabled {
			t.Fatalf("fleet browser=%v want %v", found, enabled)
		}
		stored, err := s.agentDock.Get(ctx, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if containsString(stored.Capabilities, descriptor.Name) != enabled {
			t.Fatal("node snapshot not replaced")
		}
	}
	// Neither a stale fleet contract nor Full Access manufactures a provider.
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: descriptor.Name, Arguments: map[string]any{}})
	if err == nil && !result.IsError {
		t.Fatal("stale call accepted")
	}
	closeNode()
	if _, err := s.agentDockHub.Invoke(ctx, node.ID, protocol.OperationToolCall, map[string]any{"tool": descriptor.Name}); err == nil {
		t.Fatal("offline call accepted")
	}
	// Reconnection replaces the prior snapshot; no cached enabled choice is replayed by Nexus.
	reconnected, closeReconnected := connectProjectFakeNodeWithTools(t, s, node, nil)
	defer closeReconnected()
	_ = reconnected
	stored, err := s.agentDock.Get(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if containsString(stored.Capabilities, descriptor.Name) {
		t.Fatal("reconnect revived disabled browser")
	}
}

func TestBuiltinGUIWritesOnlyTargetNodeAndDoesNotChangeNodePermissions(t *testing.T) {
	s := newNodeTestServer(t)
	pairing, err := s.agentDock.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.agentDock.Pair(t.Context(), agentdock.PairInput{Code: pairing.Code, DeviceID: "builtin-gui-device", Name: "GUI"})
	if err != nil {
		t.Fatal(err)
	}
	socket, closeNode := connectProjectFakeNode(t, s, node)
	defer closeNode()
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/nodes/"+node.ID+"/builtins", strings.NewReader(`{"id":"browser","enabled":false}`))
	request.SetPathValue("nodeID", node.ID)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.runtimeBuiltins(response, request); close(done) }()
	invoke := readProjectInvoke(t, socket)
	var forwarded struct {
		Method string                 `json:"method"`
		Path   string                 `json:"path"`
		Body   protocol.BuiltinUpdate `json:"body"`
	}
	if err := json.Unmarshal(invoke.Arguments, &forwarded); err != nil {
		t.Fatal(err)
	}
	if invoke.Operation != protocol.OperationRuntimeRequest || forwarded.Method != http.MethodPost || forwarded.Path != "/internal/runtime/builtins" || forwarded.Body.Enabled == nil || *forwarded.Body.Enabled || forwarded.Body.ID != "browser" {
		t.Fatalf("wrong bridge request: %#v", forwarded)
	}
	if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageToolResult, RequestID: invoke.RequestID, Result: json.RawMessage(`{"ok":true,"builtins":[]}`)}); err != nil {
		t.Fatal(err)
	}
	<-done
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), node.ID) {
		t.Fatalf("response=%s", response.Body.String())
	}
	stored, err := s.agentDock.Get(t.Context(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FullAccess != node.FullAccess || stored.Enabled != node.Enabled {
		t.Fatal("capability setting changed node permissions")
	}
}
