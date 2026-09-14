package httpx

import (
	"net/http"

	protocol "github.com/Serialeo/agentdock-protocol"
)

// 仅经现有管理员 Runtime 路径代理到目标节点；Nexus 不保存或回放用户开关。
func (s *Server) runtimeBuiltins(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nodeID")
	var payload map[string]any
	var err error
	if r.Method == http.MethodGet {
		payload, err = s.runtimeGet(r.Context(), nodeID, "/internal/runtime/builtins", nil)
	} else {
		var update protocol.BuiltinUpdate
		if !decodeJSON(w, r, &update) {
			return
		}
		if update.Enabled == nil || (update.ID != "browser" && update.ID != "acp") {
			writeError(w, http.StatusBadRequest, "INVALID_BUILTIN_UPDATE", "请选择内置能力并指定 enabled")
			return
		}
		payload, err = s.runtimePost(r.Context(), nodeID, "/internal/runtime/builtins", update)
	}
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	payload["node_id"] = nodeID
	writeJSON(w, http.StatusOK, payload)
}
