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
	disconnected := make(chan string, 1)
	hub.SetDisconnectHandler(func(nodeID string) { disconnected <- nodeID })
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
	if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageNodeHello, ProtocolVersion: ConnectionProtocolVersion, Hello: &Hello{
		DeviceID: node.DeviceID, Version: RequiredVersion, ProtocolVersion: ConnectionProtocolVersion, Capabilities: []string{"read_file"}, UIResources: []UIResourceCapability{},
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
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case nodeID := <-disconnected:
		if nodeID != node.ID {
			t.Fatalf("disconnected node = %q, want %q", nodeID, node.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect handler was not called")
	}
}

func TestDisconnectCallbackSerializesWithReconnectHello(t *testing.T) {
	store, _ := newTestStore(t)
	pairing, err := store.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.Pair(t.Context(), PairInput{Code: pairing.Code, DeviceID: "disconnect-reconnect", Name: "Reconnect"})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	hellos := make(chan struct{}, 2)
	hub.SetHelloHandler(func(Node, Hello) { hellos <- struct{}{} })
	disconnectEntered := make(chan struct{})
	releaseDisconnect := make(chan struct{})
	var disconnects atomic.Int32
	hub.SetDisconnectHandler(func(string) {
		if disconnects.Add(1) == 1 {
			close(disconnectEntered)
			<-releaseDisconnect
		}
	})
	defer func() {
		select {
		case <-releaseDisconnect:
		default:
			close(releaseDisconnect)
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = hub.Accept(w, r, node.ID)
	}))
	defer server.Close()
	connect := func() *websocket.Conn {
		t.Helper()
		socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageNodeHello, ProtocolVersion: ConnectionProtocolVersion, Hello: &Hello{
			DeviceID: node.DeviceID, Version: RequiredVersion, ProtocolVersion: ConnectionProtocolVersion, Capabilities: []string{"task_manage"}, UIResources: []UIResourceCapability{},
		}}); err != nil {
			t.Fatal(err)
		}
		return socket
	}
	first := connect()
	var ready connectionMessage
	if err := first.ReadJSON(&ready); err != nil || ready.Type != protocol.MessageNodeReady {
		t.Fatalf("first ready=%#v err=%v", ready, err)
	}
	<-hellos
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-disconnectEntered:
	case <-time.After(time.Second):
		t.Fatal("disconnect callback did not start")
	}

	second := connect()
	defer second.Close()
	secondReady := make(chan error, 1)
	go func() {
		var message connectionMessage
		err := second.ReadJSON(&message)
		if err == nil && message.Type != protocol.MessageNodeReady {
			err = errors.New("unexpected reconnect response")
		}
		secondReady <- err
	}()
	select {
	case err := <-secondReady:
		t.Fatalf("reconnect passed the in-flight disconnect callback: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseDisconnect)
	select {
	case err := <-secondReady:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnect did not resume after disconnect callback")
	}
	select {
	case <-hellos:
	case <-time.After(time.Second):
		t.Fatal("reconnect Hello was not published")
	}
}

func TestHandshakeReadyPrecedesInvocationsDuringCatalogPublication(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		reconnect bool
	}{
		{name: "initial runtime request", operation: protocol.OperationRuntimeRequest},
		{name: "reconnected tool call", operation: protocol.OperationToolCall, reconnect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			pairing, err := store.CreatePairingCode(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			node, err := store.Pair(t.Context(), PairInput{Code: pairing.Code, DeviceID: "ready-order", Name: "ReadyOrder"})
			if err != nil {
				t.Fatal(err)
			}
			hub := NewHub(store)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			accepted := make(chan error, 2)
			publishing, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				accepted <- hub.Accept(w, r, node.ID)
			}))
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				hub.Disconnect(node.ID)
				server.Close()
			}()
			connect := func() *websocket.Conn {
				t.Helper()
				socket, _, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = socket.Close() })
				_ = socket.SetReadDeadline(time.Now().Add(5 * time.Second))
				if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageNodeHello, ProtocolVersion: ConnectionProtocolVersion,
					Hello: &Hello{DeviceID: node.DeviceID, Version: RequiredVersion, ProtocolVersion: ConnectionProtocolVersion, UIResources: []UIResourceCapability{}},
				}); err != nil {
					t.Fatal(err)
				}
				return socket
			}
			if test.reconnect {
				previous := connect()
				var ready connectionMessage
				if err := previous.ReadJSON(&ready); err != nil || ready.Type != protocol.MessageNodeReady {
					t.Fatalf("previous ready=%#v err=%v", ready, err)
				}
				select {
				case err := <-accepted:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			hub.SetHelloHandler(func(Node, Hello) {
				close(publishing)
				<-release
			})
			socket := connect()
			select {
			case <-publishing:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// 固定停在目录回调内发起真实调用，旧实现会把 tool.invoke 写在 node.ready 前面。
			invoked := make(chan error, 1)
			go func() {
				_, err := hub.Invoke(ctx, node.ID, test.operation, map[string]any{})
				invoked <- err
			}()
			var first, invoke connectionMessage
			if err := socket.ReadJSON(&first); err != nil || first.Type != protocol.MessageNodeReady {
				t.Fatalf("first Bridge message must be node.ready: %#v, err=%v", first, err)
			}
			if err := socket.ReadJSON(&invoke); err != nil || invoke.Type != protocol.MessageToolInvoke || invoke.Operation != test.operation {
				t.Fatalf("invoke=%#v err=%v", invoke, err)
			}
			close(release)
			if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageToolResult, RequestID: invoke.RequestID, Result: []byte(`{"ok":true}`)}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-invoked:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
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
				"version":          RequiredVersion,
				"protocol_version": ConnectionProtocolVersion,
				"tools":            []any{},
			},
		},
		{
			name: "malformed renderer URI",
			hello: map[string]any{
				"version":          RequiredVersion,
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
		Hello: &Hello{DeviceID: node.DeviceID, Version: RequiredVersion, ProtocolVersion: "3", UIResources: []UIResourceCapability{}},
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
		hello := &Hello{DeviceID: node.DeviceID, Version: RequiredVersion, ProtocolVersion: ConnectionProtocolVersion, Capabilities: []string{}, UIResources: []UIResourceCapability{}}
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
	hello := &Hello{DeviceID: nodes[1].DeviceID, Version: RequiredVersion, ProtocolVersion: ConnectionProtocolVersion, Capabilities: []string{}, UIResources: []UIResourceCapability{}}
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
