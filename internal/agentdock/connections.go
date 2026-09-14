package agentdock

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/gorilla/websocket"
	"github.com/uvwt/nexusdock/internal/core"
)

const (
	maxConnectionMessageBytes = 8 << 20
	heartbeatInterval         = 30 * time.Second
)

var (
	ErrNodeOffline      = errors.New("AgentDock 节点当前离线")
	ErrNodeDisconnected = errors.New("AgentDock 节点连接已断开")
)

type pendingResult struct {
	result json.RawMessage
	err    error
}

type nodeConnection struct {
	nodeID       string
	socket       *websocket.Conn
	writeMu      sync.Mutex
	mu           sync.Mutex
	pending      map[string]chan pendingResult
	closed       bool
	snapshotHash [32]byte
}

type Hub struct {
	store         *Store
	mu            sync.RWMutex
	nodes         map[string]*nodeConnection
	onHello       func(Node, Hello)
	snapshotLocks map[string]*sync.Mutex
}

func (h *Hub) SetHelloHandler(handler func(Node, Hello)) {
	h.mu.Lock()
	h.onHello = handler
	h.mu.Unlock()
}

func NewHub(store *Store) *Hub {
	return &Hub{store: store, nodes: make(map[string]*nodeConnection), snapshotLocks: make(map[string]*sync.Mutex)}
}

// 按节点串行提交，避免慢节点的目录协调阻塞其他节点。
func (h *Hub) snapshotLock(nodeID string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	lock := h.snapshotLocks[nodeID]
	if lock == nil {
		lock = &sync.Mutex{}
		h.snapshotLocks[nodeID] = lock
	}
	return lock
}

func (h *Hub) Online(nodeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.nodes[nodeID]
	return ok
}

func (h *Hub) Disconnect(nodeID string) {
	h.mu.Lock()
	connection := h.nodes[nodeID]
	delete(h.nodes, nodeID)
	h.mu.Unlock()
	if connection != nil {
		connection.close(ErrNodeDisconnected)
	}
}

func (h *Hub) Accept(w http.ResponseWriter, r *http.Request, nodeID string) error {
	node, err := h.store.Get(r.Context(), nodeID)
	if err != nil {
		return err
	}
	if !node.Enabled {
		return ErrNodeDisabled
	}

	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		CheckOrigin: func(request *http.Request) bool {
			// 节点连接不是浏览器会话，不使用 Origin 作为身份依据；身份由 Device Token 固定绑定。
			return request.Header.Get("Origin") == ""
		},
	}
	socket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}
	socket.SetReadLimit(maxConnectionMessageBytes)
	_ = socket.SetReadDeadline(time.Now().Add(15 * time.Second))
	connection := &nodeConnection{nodeID: nodeID, socket: socket, pending: make(map[string]chan pendingResult)}

	var first connectionMessage
	if err := socket.ReadJSON(&first); err != nil {
		connection.close(err)
		return fmt.Errorf("读取 AgentDock 握手: %w", err)
	}
	if first.Type != protocol.MessageNodeHello || first.Hello == nil || first.ProtocolVersion != ConnectionProtocolVersion || first.Hello.ProtocolVersion != ConnectionProtocolVersion {
		connection.close(errors.New("invalid AgentDock handshake"))
		return errors.New("AgentDock 节点握手无效")
	}
	initialSnapshot, err := json.Marshal(first.Hello)
	if err != nil {
		connection.close(err)
		return fmt.Errorf("编码 AgentDock 初始快照: %w", err)
	}
	snapshotLock := h.snapshotLock(nodeID)
	snapshotLock.Lock()
	updated, err := h.store.UpdateHello(r.Context(), nodeID, *first.Hello)
	if err != nil {
		snapshotLock.Unlock()
		connection.close(err)
		return err
	}
	connection.snapshotHash = sha256.Sum256(initialSnapshot)
	_ = socket.SetReadDeadline(time.Now().Add(2 * heartbeatInterval))

	// AgentDock 要求首条消息为 node.ready；必须先写入成功，再发布可调用连接。
	// 仅持有本节点的快照锁，慢握手不会阻塞其他节点；失败也不替换旧连接。
	if err := connection.write(connectionMessage{
		Type: protocol.MessageNodeReady, ProtocolVersion: ConnectionProtocolVersion, HeartbeatMS: int(heartbeatInterval / time.Millisecond),
	}); err != nil {
		snapshotLock.Unlock()
		connection.close(err)
		return fmt.Errorf("确认 AgentDock 握手: %w", err)
	}

	h.mu.Lock()
	previous := h.nodes[nodeID]
	h.nodes[nodeID] = connection
	onHello := h.onHello
	h.mu.Unlock()
	if previous != nil {
		previous.close(errors.New("AgentDock 节点建立了新连接"))
	}
	if onHello != nil {
		onHello(updated, *first.Hello)
	}

	snapshotLock.Unlock()

	go h.readLoop(connection)
	return nil
}

func (h *Hub) Invoke(ctx context.Context, nodeID, operation string, arguments any) (map[string]any, error) {
	return h.invoke(ctx, nodeID, operation, nil, arguments)
}

func (h *Hub) InvokeWithExecutionContext(ctx context.Context, nodeID, operation string, executionContext *protocol.ExecutionContext, arguments any) (map[string]any, error) {
	return h.invoke(ctx, nodeID, operation, executionContext, arguments)
}

func (h *Hub) invoke(ctx context.Context, nodeID, operation string, executionContext *protocol.ExecutionContext, arguments any) (map[string]any, error) {
	h.mu.RLock()
	connection := h.nodes[nodeID]
	h.mu.RUnlock()
	if connection == nil {
		return nil, ErrNodeOffline
	}
	if operation == "" {
		return nil, errors.New("节点操作不能为空")
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("编码节点调用参数: %w", err)
	}
	requestID, err := core.NewID("req")
	if err != nil {
		return nil, err
	}
	resultChannel := make(chan pendingResult, 1)
	if err := connection.addPending(requestID, resultChannel); err != nil {
		return nil, err
	}
	defer connection.removePending(requestID)

	if err := connection.write(connectionMessage{Type: protocol.MessageToolInvoke, RequestID: requestID, Operation: operation, ExecutionContext: executionContext, Arguments: encoded}); err != nil {
		connection.close(err)
		return nil, ErrNodeDisconnected
	}
	select {
	case <-ctx.Done():
		_ = connection.write(connectionMessage{Type: protocol.MessageToolCancel, RequestID: requestID})
		return nil, ctx.Err()
	case response := <-resultChannel:
		if response.err != nil {
			return nil, response.err
		}
		var result map[string]any
		if err := json.Unmarshal(response.result, &result); err != nil {
			return nil, fmt.Errorf("解析 AgentDock 节点结果: %w", err)
		}
		return result, nil
	}
}

func (h *Hub) readLoop(connection *nodeConnection) {
	defer func() {
		h.mu.Lock()
		if h.nodes[connection.nodeID] == connection {
			delete(h.nodes, connection.nodeID)
		}
		h.mu.Unlock()
		connection.close(ErrNodeDisconnected)
	}()
	for {
		var message connectionMessage
		if err := connection.socket.ReadJSON(&message); err != nil {
			return
		}
		_ = connection.socket.SetReadDeadline(time.Now().Add(2 * heartbeatInterval))
		switch message.Type {
		case protocol.MessageNodeUpdated:
			if message.Hello == nil || message.ProtocolVersion != ConnectionProtocolVersion || message.Hello.ProtocolVersion != ConnectionProtocolVersion {
				return
			}
			snapshotLock := h.snapshotLock(connection.nodeID)
			snapshotLock.Lock()
			h.mu.RLock()
			current := h.nodes[connection.nodeID] == connection
			onHello := h.onHello
			h.mu.RUnlock()
			if !current {
				snapshotLock.Unlock()
				return
			}
			snapshot, err := json.Marshal(message.Hello)
			if err != nil {
				snapshotLock.Unlock()
				return
			}
			hash := sha256.Sum256(snapshot)
			if hash == connection.snapshotHash {
				snapshotLock.Unlock()
				continue
			}
			updated, err := h.store.UpdateHello(context.Background(), connection.nodeID, *message.Hello)
			if err == nil {
				connection.snapshotHash = hash
				if onHello != nil {
					onHello(updated, *message.Hello)
				}
			}
			snapshotLock.Unlock()
			if err != nil {
				return
			}
		case protocol.MessageToolResult:
			connection.resolve(message.RequestID, pendingResult{result: message.Result})
		case protocol.MessageToolError:
			if message.Error == nil {
				message.Error = &RemoteError{Code: "NODE_BAD_RESPONSE", Message: "AgentDock 返回了空错误"}
			}
			connection.resolve(message.RequestID, pendingResult{err: message.Error})
		case protocol.MessageNodeHeartbeat:
			_ = h.store.Touch(context.Background(), connection.nodeID)
			_ = connection.write(connectionMessage{Type: protocol.MessageNodeHeartbeat})
		}
	}
}

func (c *nodeConnection) write(message connectionMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrNodeDisconnected
	}
	_ = c.socket.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.socket.WriteJSON(message)
}

func (c *nodeConnection) addPending(requestID string, result chan pendingResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrNodeDisconnected
	}
	c.pending[requestID] = result
	return nil
}

func (c *nodeConnection) removePending(requestID string) {
	c.mu.Lock()
	delete(c.pending, requestID)
	c.mu.Unlock()
}

func (c *nodeConnection) resolve(requestID string, result pendingResult) {
	c.mu.Lock()
	channel := c.pending[requestID]
	delete(c.pending, requestID)
	c.mu.Unlock()
	if channel != nil {
		channel <- result
	}
}

func (c *nodeConnection) close(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = make(map[string]chan pendingResult)
	c.mu.Unlock()
	_ = c.socket.Close()
	for _, channel := range pending {
		channel <- pendingResult{err: reason}
	}
}
