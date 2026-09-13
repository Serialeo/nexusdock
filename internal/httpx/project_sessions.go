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
	view, err := asMap(session)
	if err != nil {
		return nil, err
	}
	delivery, err := s.projects.GetProjectContextDelivery(ctx, projectID, session.ID, "")
	if err == nil {
		view["delivery"] = delivery
		return view, nil
	}
	if errors.Is(err, projectstore.ErrContextDeliveryNotFound) {
		return view, nil
	}
	return nil, err
}

func (s *Server) projectTargetAdminView(ctx context.Context, projectID string, target projectstore.WorkTarget) (map[string]any, error) {
	view, err := asMap(target)
	if err != nil {
		return nil, err
	}
	delivery, err := s.projects.GetProjectContextDelivery(ctx, projectID, target.Target.WorkSessionID, target.Target.ID)
	if err == nil {
		view["delivery"] = delivery
		return view, nil
	}
	if errors.Is(err, projectstore.ErrContextDeliveryNotFound) {
		return view, nil
	}
	return nil, err
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
