package httpx

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uvwt/nexusdock/internal/config"
	"github.com/uvwt/nexusdock/internal/recall"
	"github.com/uvwt/nexusdock/internal/settings"
	"github.com/uvwt/nexusdock/internal/stage3"
)

func revisionIfMatch(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return value
}

type runtimeAITestResult struct {
	OK        bool   `json:"ok"`
	Target    string `json:"target"`
	Model     string `json:"model,omitempty"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

func (s *Server) getRuntimeAISettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "SETTINGS_UNAVAILABLE", "运行时 AI 设置存储不可用")
		return
	}
	_, view, err := s.settings.Load(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS_READ_FAILED", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+view.Revision+`"`)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": view})
}

func (s *Server) updateRuntimeAISettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "SETTINGS_UNAVAILABLE", "运行时 AI 设置存储不可用")
		return
	}
	var request settings.UpdateInput
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.ExpectedRevision == "" {
		request.ExpectedRevision = revisionIfMatch(r)
	}
	if reviewNodeID := strings.TrimSpace(request.Stage3.ReviewNodeID); reviewNodeID != "" {
		_, current, err := s.settings.Load(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SETTINGS_READ_FAILED", err.Error())
			return
		}
		// A review node can become disabled or be deleted after it was saved. Keeping that stale
		// reference while changing unrelated AI settings is allowed: Stage 3 routing treats it as
		// unavailable and never falls back. Selecting a new review node still requires an enabled node.
		if reviewNodeID != current.Stage3.ReviewNodeID {
			if s.agentDock == nil {
				writeError(w, http.StatusBadRequest, "INVALID_RUNTIME_SETTINGS", "Stage 3 review node 不可用")
				return
			}
			node, err := s.agentDock.Get(r.Context(), reviewNodeID)
			if err != nil || !node.Enabled {
				writeError(w, http.StatusBadRequest, "INVALID_RUNTIME_SETTINGS", "Stage 3 review_node_id 必须引用已启用的 AgentDock 节点")
				return
			}
		}
	}
	if _, _, err := s.settings.Update(r.Context(), request); err != nil {
		var validation settings.ValidationError
		switch {
		case errors.Is(err, settings.ErrRevisionConflict):
			writeError(w, http.StatusPreconditionFailed, "RUNTIME_SETTINGS_REVISION_CONFLICT", "AI 设置已被其他编辑器更新，请重新读取后再保存")
		case errors.As(err, &validation):
			writeError(w, http.StatusBadRequest, "INVALID_RUNTIME_SETTINGS", validation.Error())
		default:
			writeError(w, http.StatusInternalServerError, "SETTINGS_UPDATE_FAILED", err.Error())
		}
		return
	}
	_, view, err := s.applyCurrentRuntimeAISettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SETTINGS_UPDATE_FAILED", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+view.Revision+`"`)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": view})
}

func (s *Server) applyCurrentRuntimeAISettings(ctx context.Context) (config.Config, settings.View, error) {
	s.aiApplyMu.Lock()
	defer s.aiApplyMu.Unlock()
	cfg, view, err := s.settings.Load(ctx)
	if err != nil {
		return config.Config{}, settings.View{}, err
	}
	s.applyRuntimeAIConfig(cfg)
	return cfg, view, nil
}

func (s *Server) testStage3Connection(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	started := time.Now()
	result := runtimeAITestResult{Target: "stage3", Model: cfg.ModelName}
	client, err := stage3.NewClient(stage3.Config{
		Endpoint:     cfg.ModelEndpoint,
		Model:        cfg.ModelName,
		APIKey:       cfg.ModelAPIKey,
		Timeout:      cfg.ModelTimeout,
		SystemPrompt: cfg.ModelSystemPrompt,
	})
	if err == nil {
		err = client.Probe(r.Context())
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Message = "模型连接测试失败：" + stage3.RedactText(err.Error())
		writeJSON(w, http.StatusOK, result)
		return
	}
	result.OK = true
	result.Message = "模型连接正常，认证和模型名均可用。"
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) testEmbeddingConnection(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	result := runtimeAITestResult{Target: "embedding"}
	embedding := s.currentEmbedding()
	if embedding == nil {
		result.Message = "向量服务尚未配置。"
		writeJSON(w, http.StatusOK, result)
		return
	}
	status := embedding.Status(r.Context())
	result.LatencyMS = time.Since(started).Milliseconds()
	if model, ok := status["model"].(string); ok {
		result.Model = model
	}
	reachable, _ := status["reachable"].(bool)
	if !reachable {
		if message, ok := status["error"].(string); ok && message != "" {
			result.Message = "向量连接测试失败：" + stage3.RedactText(message)
		} else {
			result.Message = "向量服务未启用或当前不可达。"
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	result.OK = true
	result.Message = "向量服务连接正常，Embedding 请求可用。"
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) currentConfig() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.aiCfgSet {
		return s.cfg
	}
	return s.aiCfg
}

func (s *Server) currentEmbedding() *recall.EmbeddingService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.embedding
}

func (s *Server) applyRuntimeAIConfig(cfg config.Config) {
	embedding := recall.NewEmbeddingService(s.store, recall.EmbeddingConfig{
		Enabled: cfg.EmbeddingEnabled, Endpoint: cfg.EmbeddingEndpoint, Model: cfg.EmbeddingModel, APIKey: cfg.EmbeddingAPIKey,
		IndexPath: cfg.EmbeddingIndexFile, Timeout: cfg.EmbeddingTimeout,
	})

	s.mu.Lock()
	s.aiCfg = cfg
	s.aiCfgSet = true
	s.embedding = embedding
	s.mu.Unlock()

	s.notifyStage3ConfigChanged()
}

func (s *Server) notifyStage3ConfigChanged() {
	if s.stage3Wake == nil {
		return
	}
	select {
	case s.stage3Wake <- struct{}{}:
	default:
	}
}
