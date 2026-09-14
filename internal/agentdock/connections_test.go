package agentdock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/gorilla/websocket"
)

func TestHubInvokesConnectedNode(t *testing.T) {
	store, _ := newTestStore(t)
	pairing, err := store.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.Pair(t.Context(), PairInput{Code: pairing.Code, DeviceID: "device_12345678", Name: "DockMini"})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	connected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := hub.Accept(w, r, node.ID); err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		close(connected)
	}))
	defer server.Close()

	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageNodeHello, ProtocolVersion: ConnectionProtocolVersion, Hello: &Hello{
		DeviceID: node.DeviceID, ProtocolVersion: ConnectionProtocolVersion, Capabilities: []string{"read_file"}, UIResources: []UIResourceCapability{},
	}}); err != nil {
		t.Fatal(err)
	}
	var ready connectionMessage
	if err := socket.ReadJSON(&ready); err != nil || ready.Type != protocol.MessageNodeReady {
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	<-connected

	done := make(chan error, 1)
	go func() {
		var invoke connectionMessage
		if err := socket.ReadJSON(&invoke); err != nil {
			done <- err
			return
		}
		done <- socket.WriteJSON(connectionMessage{Type: protocol.MessageToolResult, RequestID: invoke.RequestID, Result: []byte(`{"ok":true}`)})
	}()
	result, err := hub.Invoke(context.Background(), node.ID, "tool.call", map[string]any{"tool": "read_file"})
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true {
		t.Fatalf("result = %#v", result)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHubRejectsStructurallyInvalidBridgeV2UIResourceHandshake(t *testing.T) {
	tests := []struct {
		name  string
		hello map[string]any
	}{
		{
			name: "missing ui_resources",
			hello: map[string]any{
				"protocol_version": ConnectionProtocolVersion,
				"tools":            []any{},
			},
		},
		{
			name: "malformed renderer URI",
			hello: map[string]any{
				"protocol_version": ConnectionProtocolVersion,
				"tools":            []any{},
				"ui_resources": []any{map[string]any{
					"uri": "https://example.test/widget", "contract": "future.v1", "mime_type": "text/html",
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			pairing, err := store.CreatePairingCode(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			node, err := store.Pair(t.Context(), PairInput{Code: pairing.Code, DeviceID: "device_reject_" + strings.ReplaceAll(tt.name, " ", "_"), Name: "DockMini"})
			if err != nil {
				t.Fatal(err)
			}
			hub := NewHub(store)
			acceptErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				acceptErr <- hub.Accept(w, r, node.ID)
			}))
			defer server.Close()

			socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			hello := make(map[string]any, len(tt.hello)+2)
			for key, value := range tt.hello {
				hello[key] = value
			}
			hello["device_id"] = node.DeviceID
			if err := socket.WriteJSON(map[string]any{
				"type": protocol.MessageNodeHello, "protocol_version": ConnectionProtocolVersion, "hello": hello,
			}); err != nil {
				t.Fatal(err)
			}

			_ = socket.SetReadDeadline(time.Now().Add(time.Second))
			var ready connectionMessage
			if err := socket.ReadJSON(&ready); err == nil {
				t.Fatalf("invalid handshake unexpectedly received ready: %#v", ready)
			}
			select {
			case err := <-acceptErr:
				var validation ValidationError
				if !errors.As(err, &validation) {
					t.Fatalf("accept error = %#v, want ValidationError", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Accept did not reject invalid Bridge v4 handshake")
			}
			if hub.Online(node.ID) {
				t.Fatal("invalid Bridge v4 handshake marked node online")
			}
		})
	}
}

func TestHubRejectsPreviousBridgeProtocolGeneration(t *testing.T) {
	store, _ := newTestStore(t)
	pairing, err := store.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.Pair(t.Context(), PairInput{Code: pairing.Code, DeviceID: "device_previous_protocol", Name: "OldBridge"})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	acceptErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptErr <- hub.Accept(w, r, node.ID)
	}))
	defer server.Close()

	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := socket.WriteJSON(connectionMessage{
		Type: protocol.MessageNodeHello, ProtocolVersion: "3",
		Hello: &Hello{DeviceID: node.DeviceID, ProtocolVersion: "3", UIResources: []UIResourceCapability{}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acceptErr:
		if err == nil {
			t.Fatal("Bridge v3 handshake was accepted by Bridge v4 Nexus")
		}
	case <-time.After(time.Second):
		t.Fatal("Bridge v3 handshake was not rejected promptly")
	}
	if hub.Online(node.ID) {
		t.Fatal("Bridge v3 node was marked online")
	}
}

func TestSlowNodeSnapshotDoesNotBlockOtherNodesAndDuplicatesAreCoalesced(t *testing.T) {
	store, _ := newTestStore(t)
	nodes := make([]Node, 2)
	for i, id := range []string{"slow-device", "fast-device"} {
		pairing, err := store.CreatePairingCode(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		pair, err := store.Pair(t.Context(), PairInput{Code: pairing.Code, DeviceID: id, Name: id})
		if err != nil {
			t.Fatal(err)
		}
		nodes[i], err = store.Get(t.Context(), pair.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	hub := NewHub(store)
	slowEntered, release := make(chan struct{}), make(chan struct{})
	var fastCallbacks atomic.Int32
	hub.SetHelloHandler(func(node Node, hello Hello) {
		if node.ID == nodes[0].ID {
			close(slowEntered)
			<-release
		} else {
			fastCallbacks.Add(1)
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = hub.Accept(w, r, r.URL.Query().Get("node")) }))
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		server.Close()
	}()
	connect := func(node Node) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"?node="+node.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		hello := &Hello{DeviceID: node.DeviceID, ProtocolVersion: ConnectionProtocolVersion, Capabilities: []string{}, UIResources: []UIResourceCapability{}}
		if err := c.WriteJSON(connectionMessage{Type: protocol.MessageNodeHello, ProtocolVersion: ConnectionProtocolVersion, Hello: hello}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	slow := connect(nodes[0])
	_ = slow
	select {
	case <-slowEntered:
	case <-time.After(time.Second):
		t.Fatal("slow callback not reached")
	}
	fast := connect(nodes[1])
	_ = fast.SetReadDeadline(time.Now().Add(time.Second))
	var ready connectionMessage
	if err := fast.ReadJSON(&ready); err != nil {
		t.Fatalf("unrelated node blocked: %v", err)
	}
	hello := &Hello{DeviceID: nodes[1].DeviceID, ProtocolVersion: ConnectionProtocolVersion, Capabilities: []string{}, UIResources: []UIResourceCapability{}}
	for i := 0; i < 4; i++ {
		if err := fast.WriteJSON(connectionMessage{Type: protocol.MessageNodeUpdated, ProtocolVersion: ConnectionProtocolVersion, Hello: hello}); err != nil {
			t.Fatal(err)
		}
	}
	// 心跳排在快照之后，收到确认说明前面的重复快照均已处理。
	if err := fast.WriteJSON(connectionMessage{Type: protocol.MessageNodeHeartbeat}); err != nil {
		t.Fatal(err)
	}
	if err := fast.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if fastCallbacks.Load() != 1 {
		t.Fatalf("duplicate reconciliations=%d", fastCallbacks.Load())
	}
}
