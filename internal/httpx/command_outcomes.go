package httpx

import (
	"context"
	"fmt"
	"sync"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
)

// StartWorkContinuation 独立于 iframe 收集命令事实。关闭聊天不会停止收集或丢掉待续接结果。
// 返回的等待函数在应用关闭数据库之前使用，避免后台事务访问已关闭的存储。
func (s *Server) StartWorkContinuation(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			s.collectCommandOutcomes(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { <-done }
}

func (s *Server) collectCommandOutcomes(ctx context.Context) {
	if s.projects == nil || s.agentDock == nil || s.agentDockHub == nil || ctx.Err() != nil {
		return
	}
	nodes, err := s.agentDock.List(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("读取命令结果节点目录失败", "error", err)
		}
		return
	}
	// 有界并发隔离慢节点；同一轮全部结束后才开始下一轮，不累计后台任务。
	slots := make(chan struct{}, 4)
	var workers sync.WaitGroup
	for _, node := range nodes {
		if !node.Enabled || !node.IsCurrent() || !s.agentDockHub.Online(node.ID) {
			continue
		}
		capabilities, capErr := s.agentDock.BridgeCapabilities(ctx, node.ID)
		if capErr != nil {
			if s.logger != nil {
				s.logger.Warn("读取命令结果 Bridge 能力失败", "node_id", node.ID, "error", capErr)
			}
			continue
		}
		if !containsString(capabilities, protocol.CommandOutcomesCapability) {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			workers.Wait()
			return
		}
		workers.Add(1)
		go func(nodeID string) {
			defer workers.Done()
			defer func() { <-slots }()
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := s.collectNodeCommandOutcomes(callCtx, nodeID); err != nil && ctx.Err() == nil && s.logger != nil {
				s.logger.Warn("收集 AgentDock 命令结果失败，将在后续轮次重试", "node_id", nodeID, "error", err)
			}
		}(node.ID)
	}
	workers.Wait()
}

func (s *Server) collectNodeCommandOutcomes(ctx context.Context, nodeID string) error {
	result, err := s.agentDockHub.Invoke(ctx, nodeID, protocol.OperationCommandOutcomesRead, protocol.CommandOutcomesReadRequest{PendingOnly: true, Limit: 32})
	if err != nil {
		return err
	}
	var batch protocol.CommandOutcomesReadResult
	if err := decodeMap(result, &batch); err != nil {
		return fmt.Errorf("decode command outcomes: %w", err)
	}
	// nodeID 来自已认证的 Hub 路由，绝不接受事件体自称的节点身份。
	ids, err := s.projects.RecordCommandOutcomes(ctx, nodeID, batch.Outcomes)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// 事务提交后才 ACK。ACK 丢失时重读同一 event_id，存储会幂等去重。
	_, err = s.agentDockHub.Invoke(ctx, nodeID, protocol.OperationCommandOutcomesAck, protocol.CommandOutcomesAckRequest{EventIDs: ids})
	return err
}
