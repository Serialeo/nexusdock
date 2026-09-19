package httpx

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	projectstore "github.com/uvwt/nexusdock/internal/project"
)

func (s *Server) projectSessionList(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	projectID := strings.TrimSpace(r.PathValue("projectID"))
	if _, err := s.projects.GetProject(r.Context(), projectID); err != nil {
		writeProjectError(w, err)
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			writeError(w, http.StatusBadRequest, "INVALID_PROJECT", "limit 必须是 1 到 500 的整数")
			return
		}
		limit = value
	}
	items, err := s.projects.ListProjectWorkSessions(r.Context(), projectID, limit)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		view, viewErr := s.projectSessionAdminView(r.Context(), projectID, item)
		if viewErr != nil {
			writeProjectSessionError(w, viewErr)
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project_id": projectID, "sessions": views, "count": len(views)})
}

func (s *Server) projectSessionGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	projectID := strings.TrimSpace(r.PathValue("projectID"))
	workSessionID := strings.TrimSpace(r.PathValue("workSessionID"))
	if _, err := s.projects.GetProject(r.Context(), projectID); err != nil {
		writeProjectError(w, err)
		return
	}
	session, err := s.projects.GetProjectWorkSession(r.Context(), projectID, workSessionID)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	targets, err := s.projects.ListProjectWorkTargets(r.Context(), projectID, workSessionID)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sessionView, err := s.projectSessionAdminView(r.Context(), projectID, session)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	targetViews := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		view, viewErr := s.projectTargetAdminView(r.Context(), projectID, target)
		if viewErr != nil {
			writeProjectSessionError(w, viewErr)
			return
		}
		targetViews = append(targetViews, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project_id": projectID, "session": sessionView, "targets": targetViews})
}

func (s *Server) projectSessionAdminView(ctx context.Context, projectID string, session projectstore.WorkSession) (map[string]any, error) {
	// WorkSession 的内部 revision、幂等 request 和 owner binding 只服务于路由与一致性检查。
	// 管理界面只返回用户能理解和采取行动的信息，避免把内部哈希当成产品状态展示。
	view := map[string]any{
		"work_session_id": session.ID,
		"status":          session.Status,
		"created_at":      session.CreatedAt,
		"updated_at":      session.UpdatedAt,
	}
	delivery, err := s.projects.GetProjectContextDelivery(ctx, projectID, session.ID, "")
	if err == nil {
		view["delivery"] = projectContextDeliveryAdminView(delivery)
		return view, nil
	}
	if errors.Is(err, projectstore.ErrContextDeliveryNotFound) {
		return view, nil
	}
	return nil, err
}

func (s *Server) projectTargetAdminView(ctx context.Context, projectID string, target projectstore.WorkTarget) (map[string]any, error) {
	sources := make([]map[string]any, 0, len(target.Target.Prompt.Sources))
	for _, source := range target.Target.Prompt.Sources {
		sources = append(sources, map[string]any{
			"path":  source.Path,
			"scope": source.Scope,
			"bytes": source.Bytes,
		})
	}
	view := map[string]any{
		"target": map[string]any{
			"target_id":     target.Target.ID,
			"deployment_id": target.Target.DeploymentID,
			"node_id":       target.Target.NodeID,
			"cwd_rel":       target.Target.CWDRel,
			"status":        target.Target.Status,
			"permissions":   target.Target.Permissions,
			"prompt": map[string]any{
				"complete": target.Target.Prompt.Complete,
				"bytes":    target.Target.Prompt.Bytes,
				"sources":  sources,
			},
		},
		"created_at": target.CreatedAt,
		"updated_at": target.UpdatedAt,
	}
	if target.LastError != "" {
		view["last_error"] = target.LastError
	}
	delivery, err := s.projects.GetProjectContextDelivery(ctx, projectID, target.Target.WorkSessionID, target.Target.ID)
	if err == nil {
		view["delivery"] = projectContextDeliveryAdminView(delivery)
		return view, nil
	}
	if errors.Is(err, projectstore.ErrContextDeliveryNotFound) {
		return view, nil
	}
	return nil, err
}

func projectContextDeliveryAdminView(delivery projectstore.ContextDelivery) map[string]any {
	view := map[string]any{
		"status":      delivery.Status,
		"returned_at": delivery.ReturnedAt,
		"updated_at":  delivery.UpdatedAt,
	}
	if delivery.HostConsumedAt != nil {
		view["host_consumed_at"] = delivery.HostConsumedAt
	}
	return view
}

func writeProjectSessionError(w http.ResponseWriter, err error) {
	switch err {
	case projectstore.ErrWorkSessionNotFound:
		writeError(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
	case projectstore.ErrWorkTargetNotFound:
		writeError(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "SESSION_LIST_FAILED", err.Error())
	}
}
