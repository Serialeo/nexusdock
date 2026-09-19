package agentdock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/gorilla/websocket"
)

func TestHubRejectsNonCurrentReleaseBeforePublishing(t *testing.T) {
	for _, version := range []string{"0.9.4", "99.0.0", "", "v" + RequiredVersion} {
		t.Run(version, func(t *testing.T) {
			store, _ := newTestStore(t)
			code, err := store.CreatePairingCode(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			node, err := store.Pair(t.Context(), PairInput{Code: code.Code, DeviceID: "device_release_gate", Name: "Release"})
			if err != nil {
				t.Fatal(err)
			}
			hub := NewHub(store)
			published := make(chan struct{}, 1)
			hub.SetHelloHandler(func(Node, Hello) { published <- struct{}{} })
			accepted := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { accepted <- hub.Accept(w, r, node.ID) }))
			defer server.Close()
			socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageNodeHello, ProtocolVersion: ConnectionProtocolVersion, Hello: &Hello{DeviceID: node.DeviceID, Version: version, ProtocolVersion: ConnectionProtocolVersion, UIResources: []UIResourceCapability{}}}); err != nil {
				t.Fatal(err)
			}
			_ = socket.SetReadDeadline(time.Now().Add(time.Second))
			var ready connectionMessage
			if err := socket.ReadJSON(&ready); err == nil {
				t.Fatalf("non-current node received ready: %#v", ready)
			}
			select {
			case err := <-accepted:
				if err == nil {
					t.Fatal("non-current release accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("handshake did not finish")
			}
			if hub.Online(node.ID) {
				t.Fatal("rejected node is online")
			}
			select {
			case <-published:
				t.Fatal("rejected node published Hello")
			default:
			}
			saved, err := store.Get(t.Context(), node.ID)
			if err != nil || saved.Version != version || saved.IsCurrent() {
				t.Fatalf("rejected version not recorded: %#v %v", saved, err)
			}
		})
	}
}

func TestUpdatedSnapshotWithOlderReleaseDisconnectsCurrentNode(t *testing.T) {
	store, _ := newTestStore(t)
	code, err := store.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.Pair(t.Context(), PairInput{Code: code.Code, DeviceID: "device_release_update", Name: "Update"})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	published := make(chan string, 2)
	disconnected := make(chan struct{}, 1)
	accepted := make(chan error, 1)
	hub.SetHelloHandler(func(n Node, _ Hello) { published <- n.Version })
	hub.SetDisconnectHandler(func(string) { disconnected <- struct{}{} })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { accepted <- hub.Accept(w, r, node.ID) }))
	defer server.Close()
	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	hello := &Hello{DeviceID: node.DeviceID, Version: RequiredVersion, ProtocolVersion: ConnectionProtocolVersion, UIResources: []UIResourceCapability{}}
	if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageNodeHello, ProtocolVersion: ConnectionProtocolVersion, Hello: hello}); err != nil {
		t.Fatal(err)
	}
	var ready connectionMessage
	if err := socket.ReadJSON(&ready); err != nil || ready.Type != protocol.MessageNodeReady {
		t.Fatalf("current handshake: %#v %v", ready, err)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if got := <-published; got != RequiredVersion {
		t.Fatal(got)
	}
	hello.Version = "0.9.4"
	if err := socket.WriteJSON(connectionMessage{Type: protocol.MessageNodeUpdated, ProtocolVersion: ConnectionProtocolVersion, Hello: hello}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("old snapshot did not disconnect node")
	}
	if hub.Online(node.ID) {
		t.Fatal("old snapshot retained live node")
	}
	select {
	case <-published:
		t.Fatal("old snapshot reached tool catalog")
	default:
	}
	saved, err := store.Get(t.Context(), node.ID)
	if err != nil || saved.IsCurrent() {
		t.Fatalf("old snapshot retained current directory state: %#v %v", saved, err)
	}
}
