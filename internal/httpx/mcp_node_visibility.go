package httpx

import (
	"context"
	"errors"

	"github.com/uvwt/nexusdock/internal/agentdock"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

// 非当前节点在模型边界等同于不存在；管理员目录仍可查看版本并完成升级。
func (s *Server) currentAgentDockNode(ctx context.Context, nodeID string) (agentdock.Node, error) {
	if s.agentDock == nil {
		return agentdock.Node{}, agentdock.ErrNodeNotFound
	}
	node, err := s.agentDock.Get(ctx, nodeID)
	if err != nil {
		return agentdock.Node{}, err
	}
	if !node.Enabled || !node.IsCurrent() {
		return agentdock.Node{}, agentdock.ErrNodeNotFound
	}
	return node, nil
}

func (s *Server) currentProjectDeployments(ctx context.Context, projectID string) ([]projectstore.Deployment, error) {
	deployments, err := s.projects.ListDeployments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	visible := make([]projectstore.Deployment, 0, len(deployments))
	for _, deployment := range deployments {
		if _, err := s.currentAgentDockNode(ctx, deployment.NodeID); err != nil {
			if errors.Is(err, agentdock.ErrNodeNotFound) {
				continue
			}
			return nil, err
		}
		visible = append(visible, deployment)
	}
	return visible, nil
}

func (s *Server) currentWorkTargets(ctx context.Context, ownerKey, sessionID string) ([]projectstore.WorkTarget, error) {
	targets, err := s.projects.ListWorkTargets(ctx, ownerKey, sessionID)
	if err != nil {
		return nil, err
	}
	visible := make([]projectstore.WorkTarget, 0, len(targets))
	for _, target := range targets {
		if _, err := s.currentAgentDockNode(ctx, target.Target.NodeID); err != nil {
			if errors.Is(err, agentdock.ErrNodeNotFound) {
				continue
			}
			return nil, err
		}
		visible = append(visible, target)
	}
	return visible, nil
}

// Continuation 的 wake 与 outcomes 属于同一次交付，不能裁掉其中旧 Target 后继续消费。
func (s *Server) requireCurrentContinuationTargets(ctx context.Context, ownerKey, sessionID string) error {
	targets, err := s.projects.ListWorkTargets(ctx, ownerKey, sessionID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := s.currentAgentDockNode(ctx, target.Target.NodeID); err != nil {
			return errors.New("WorkSession is unavailable")
		}
	}
	return nil
}
