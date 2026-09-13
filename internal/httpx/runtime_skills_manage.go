package httpx

import (
	"net/http"
	"strings"
)

type runtimeSkillManageRequest struct {
	Action  string  `json:"action"`
	Version string  `json:"version,omitempty"`
	Key     string  `json:"key,omitempty"`
	Value   *string `json:"value,omitempty"`
}

func (s *Server) runtimeSkillEnvironment(w http.ResponseWriter, r *http.Request) {
	source, skillID, ok := runtimeManagedSkillIdentity(w, r)
	if !ok {
		return
	}
	_ = source
	payload, err := s.runtimePost(r.Context(), r.PathValue("nodeID"), "/internal/runtime/skills/manage", map[string]any{
		"action": "env_list", "skill": skillID,
	})
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	delete(payload, "value")
	payload["ok"] = true
	payload["node_id"] = r.PathValue("nodeID")
	payload["skill_id"] = skillID
	payload["source"] = source
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) runtimeSkillManage(w http.ResponseWriter, r *http.Request) {
	source, skillID, ok := runtimeManagedSkillIdentity(w, r)
	if !ok {
		return
	}
	var request runtimeSkillManageRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.Version = strings.TrimSpace(request.Version)
	request.Key = strings.TrimSpace(request.Key)
	payload := map[string]any{"action": request.Action, "skill": skillID}
	switch request.Action {
	case "activate":
		if request.Version == "" || request.Key != "" || request.Value != nil {
			writeError(w, http.StatusBadRequest, "INVALID_SKILL_ACTION", "activate requires version and does not accept environment fields")
			return
		}
		payload["version"] = request.Version
	case "rollback":
		if request.Version != "" || request.Key != "" || request.Value != nil {
			writeError(w, http.StatusBadRequest, "INVALID_SKILL_ACTION", "rollback does not accept version or environment fields")
			return
		}
	case "env_set":
		if request.Key == "" || request.Value == nil || request.Version != "" {
			writeError(w, http.StatusBadRequest, "INVALID_SKILL_ACTION", "env_set requires key and an explicit value")
			return
		}
		payload["key"] = request.Key
		payload["value"] = *request.Value
	case "env_unset":
		if request.Key == "" || request.Value != nil || request.Version != "" {
			writeError(w, http.StatusBadRequest, "INVALID_SKILL_ACTION", "env_unset requires key and no value")
			return
		}
		payload["key"] = request.Key
	default:
		writeError(w, http.StatusBadRequest, "INVALID_SKILL_ACTION", "action must be activate, rollback, env_set, or env_unset")
		return
	}
	result, err := s.runtimePost(r.Context(), r.PathValue("nodeID"), "/internal/runtime/skills/manage", payload)
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	// Defense in depth: the AgentDock runtime contract never returns secret values.
	delete(result, "value")
	result["ok"] = true
	result["node_id"] = r.PathValue("nodeID")
	result["skill_id"] = skillID
	result["source"] = source
	writeJSON(w, http.StatusOK, result)
}

func runtimeManagedSkillIdentity(w http.ResponseWriter, r *http.Request) (source, skillID string, ok bool) {
	source = strings.TrimSpace(r.PathValue("source"))
	if source != "agentdock-api" {
		writeError(w, http.StatusBadRequest, "SKILL_SOURCE_READ_ONLY", "only installed AgentDock Skill packages can be managed")
		return "", "", false
	}
	var err error
	skillID, err = cleanOpsName(r.PathValue("skillID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SKILL_ID", err.Error())
		return "", "", false
	}
	return source, skillID, true
}
