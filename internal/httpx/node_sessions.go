package httpx

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) nodeSessionList(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			writeError(w, http.StatusBadRequest, "INVALID_NODE_SESSION", "limit 必须是 1 到 500 的整数")
			return
		}
		limit = value
	}
	items, err := s.projects.ListNodeWorkSessions(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NODE_SESSION_LIST_FAILED", err.Error())
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		view, viewErr := s.projectSessionAdminView(r.Context(), item.Session.ProjectID, item.Session)
		if viewErr != nil {
			writeProjectSessionError(w, viewErr)
			return
		}
		view["node_id"] = item.NodeID
		if s.agentDock != nil {
			if node, nodeErr := s.agentDock.Get(r.Context(), item.NodeID); nodeErr == nil {
				view["node_name"] = node.Name
			}
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": views, "count": len(views)})
}

func (s *Server) nodeSessionGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	item, err := s.projects.GetNodeWorkSession(r.Context(), r.PathValue("workSessionID"))
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	targets, err := s.projects.ListProjectWorkTargets(r.Context(), item.Session.ProjectID, item.Session.ID)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sessionView, err := s.projectSessionAdminView(r.Context(), item.Session.ProjectID, item.Session)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sessionView["node_id"] = item.NodeID
	if s.agentDock != nil {
		if node, nodeErr := s.agentDock.Get(r.Context(), item.NodeID); nodeErr == nil {
			sessionView["node_name"] = node.Name
		}
	}
	targetViews := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		view, viewErr := s.projectTargetAdminView(r.Context(), item.Session.ProjectID, target)
		if viewErr != nil {
			writeProjectSessionError(w, viewErr)
			return
		}
		targetViews = append(targetViews, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": sessionView, "targets": targetViews})
}
