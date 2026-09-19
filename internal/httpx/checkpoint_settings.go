package httpx

import (
	"errors"
	"net/http"

	"github.com/uvwt/nexusdock/internal/settings"
)

type checkpointPromptResponse struct {
	OK       bool                          `json:"ok"`
	Settings settings.CheckpointPromptView `json:"settings"`
}

func (s *Server) getCheckpointPrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.checkpointSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "CHECKPOINT_SETTINGS_UNAVAILABLE", "Checkpoint 提示词存储不可用")
		return
	}
	view, err := s.checkpointSettings.LoadCheckpointPrompt(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CHECKPOINT_SETTINGS_READ_FAILED", "读取 checkpoint 提示词失败")
		return
	}
	writeJSON(w, http.StatusOK, checkpointPromptResponse{OK: true, Settings: view})
}

func (s *Server) updateCheckpointPrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.checkpointSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "CHECKPOINT_SETTINGS_UNAVAILABLE", "Checkpoint 提示词存储不可用")
		return
	}
	var input settings.CheckpointPromptUpdate
	if !decodeJSON(w, r, &input) {
		return
	}
	view, err := s.checkpointSettings.UpdateCheckpointPrompt(r.Context(), input)
	if err != nil {
		var validation settings.ValidationError
		switch {
		case errors.Is(err, settings.ErrCheckpointPromptRevisionConflict):
			writeError(w, http.StatusConflict, "CHECKPOINT_PROMPT_REVISION_CONFLICT", err.Error())
		case errors.As(err, &validation):
			writeError(w, http.StatusBadRequest, "INVALID_CHECKPOINT_PROMPT", validation.Error())
		default:
			writeError(w, http.StatusInternalServerError, "CHECKPOINT_SETTINGS_UPDATE_FAILED", "保存 checkpoint 提示词失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, checkpointPromptResponse{OK: true, Settings: view})
}
