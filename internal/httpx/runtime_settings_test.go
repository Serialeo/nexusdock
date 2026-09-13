package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/config"
	"github.com/uvwt/nexusdock/internal/core"
	"github.com/uvwt/nexusdock/internal/recall"
	"github.com/uvwt/nexusdock/internal/settings"
	"github.com/uvwt/nexusdock/internal/stage3"
)

func newRuntimeSettingsHTTPServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	dataDir := t.TempDir()
	db, err := core.OpenSQLite(t.Context(), filepath.Join(dataDir, "nexus.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := core.EnsureSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	store, err := recall.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.NexusDataDir = dataDir
	cfg.RecallRepoDir = store.Root()
	runtimeSettings, err := settings.NewStore(db, dataDir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	nodeStore, err := agentdock.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg, store, nil, slog.Default(), WithSystemDatabase(db), WithRuntimeSettings(runtimeSettings), WithAgentDockNodes(nodeStore))
}

func TestRuntimeAISettingsAPIProtectsSecretsAndAppliesEmbeddingConfiguration(t *testing.T) {
	const (
		authToken      = "nexus-settings-test-token"
		embeddingToken = "embedding-settings-secret"
	)
	var embeddingAuthorization string
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embeddingAuthorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float64{1, 0}}}})
	}))
	defer embedding.Close()

	server := newRuntimeSettingsHTTPServer(t, config.Config{
		AuthToken: authToken, RequireAuth: true,
		EmbeddingModel: recall.DefaultEmbeddingModel, EmbeddingTimeout: 30 * time.Second,
		ModelTimeout: 60 * time.Second, EvolutionInterval: 6 * time.Hour,
	})
	handler := server.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/settings/ai", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settings status = %d, want 401", unauthorized.Code)
	}

	body := map[string]any{
		"embedding": map[string]any{
			"enabled": true, "endpoint": embedding.URL, "model": "test-embedding", "timeout_seconds": 15,
			"api_key": map[string]any{"action": "replace", "value": embeddingToken},
		},
		"stage3": map[string]any{
			"enabled": false, "endpoint": "", "model": "", "timeout_seconds": 60, "interval_minutes": 360,
			"api_key": map[string]any{"action": "keep"},
		},
	}
	payload, _ := json.Marshal(body)
	update := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/settings/ai", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(update, req)
	if update.Code != http.StatusOK {
		t.Fatalf("settings update status=%d body=%s", update.Code, update.Body.String())
	}
	if strings.Contains(update.Body.String(), embeddingToken) {
		t.Fatal("settings update response leaked embedding API key")
	}

	read := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/settings/ai", nil)
	req.Header.Set("Authorization", "Bearer "+authToken)
	handler.ServeHTTP(read, req)
	if read.Code != http.StatusOK || strings.Contains(read.Body.String(), embeddingToken) {
		t.Fatalf("settings read response invalid or leaked secret: status=%d body=%s", read.Code, read.Body.String())
	}
	var response struct {
		Settings settings.View `json:"settings"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Settings.Embedding.Enabled || !response.Settings.Embedding.APIKeyConfigured || response.Settings.Embedding.Model != "test-embedding" {
		t.Fatalf("unexpected settings response: %#v", response.Settings)
	}

	status := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/embeddings/status", nil)
	req.Header.Set("Authorization", "Bearer "+authToken)
	handler.ServeHTTP(status, req)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"reachable":true`) {
		t.Fatalf("embedding status=%d body=%s", status.Code, status.Body.String())
	}
	if embeddingAuthorization != "Bearer "+embeddingToken {
		t.Fatalf("embedding authorization=%q", embeddingAuthorization)
	}
}

func TestWorkflowEmbeddingUsesRuntimeAPIKey(t *testing.T) {
	const token = "workflow-embedding-secret"
	var authorization string
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float64{1, 0}}}})
	}))
	defer embedding.Close()

	server := &Server{}
	vectors, err := server.embedWorkflowTemplateTexts(t.Context(), config.Config{
		EmbeddingEndpoint: embedding.URL,
		EmbeddingModel:    "test-embedding",
		EmbeddingAPIKey:   token,
		EmbeddingTimeout:  time.Second,
	}, []string{"workflow text"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 2 {
		t.Fatalf("unexpected vectors: %#v", vectors)
	}
	if authorization != "Bearer "+token {
		t.Fatalf("workflow embedding authorization=%q", authorization)
	}
}

func TestRuntimeAISettingsAPIAppliesAndResetsStage3SystemPrompt(t *testing.T) {
	const authToken = "nexus-stage3-prompt-test-token"
	server := newRuntimeSettingsHTTPServer(t, config.Config{
		AuthToken: authToken, RequireAuth: true,
		EmbeddingModel: recall.DefaultEmbeddingModel, EmbeddingTimeout: 30 * time.Second,
		ModelTimeout: 60 * time.Second, EvolutionInterval: 6 * time.Hour,
	})
	handler := server.Handler()

	put := func(prompt map[string]any) settings.View {
		t.Helper()
		body := map[string]any{
			"embedding": map[string]any{"enabled": false, "endpoint": "", "model": recall.DefaultEmbeddingModel, "timeout_seconds": 30, "api_key": map[string]any{"action": "keep"}},
			"stage3": map[string]any{
				"enabled": false, "endpoint": "", "model": "", "timeout_seconds": 60, "interval_minutes": 360,
				"api_key": map[string]any{"action": "keep"}, "system_prompt": prompt,
			},
		}
		payload, _ := json.Marshal(body)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/v1/settings/ai", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+authToken)
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("PUT settings status=%d body=%s", response.Code, response.Body.String())
		}
		var result struct {
			Settings settings.View `json:"settings"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Settings
	}

	custom := "HTTP custom Stage 3 prompt.\nExact value.  "
	view := put(map[string]any{"action": "replace", "value": custom})
	if view.Stage3.SystemPrompt != custom || view.Stage3.SystemPromptSource != "custom" || server.currentConfig().ModelSystemPrompt != custom {
		t.Fatalf("custom prompt not applied: view=%#v cfg=%q", view.Stage3, server.currentConfig().ModelSystemPrompt)
	}

	view = put(map[string]any{"action": "reset"})
	if view.Stage3.SystemPrompt != stage3.BundledDefaultPrompt() || view.Stage3.SystemPromptSource != "bundled_default" || server.currentConfig().ModelSystemPrompt != stage3.BundledDefaultPrompt() {
		t.Fatalf("bundled prompt not restored: view=%#v cfg=%q", view.Stage3, server.currentConfig().ModelSystemPrompt)
	}
}

func TestRuntimeAIConnectionTestsUseSavedSecretsAndStayAuthenticated(t *testing.T) {
	const (
		authToken      = "nexus-test-token"
		stage3Token    = "stage3-test-secret"
		embeddingToken = "embedding-test-secret"
	)
	var stage3Authorization, embeddingAuthorization, stage3Path string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage3Authorization = r.Header.Get("Authorization")
		stage3Path = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "OK"}}}})
	}))
	defer model.Close()
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embeddingAuthorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float64{1, 0}}}})
	}))
	defer embedding.Close()

	server := newRuntimeSettingsHTTPServer(t, config.Config{
		AuthToken: authToken, RequireAuth: true,
		EmbeddingModel: recall.DefaultEmbeddingModel, EmbeddingTimeout: time.Second,
		ModelTimeout: time.Second, EvolutionInterval: 6 * time.Hour,
	})
	handler := server.Handler()
	body := map[string]any{
		"embedding": map[string]any{
			"enabled": true, "endpoint": embedding.URL, "model": "embed-test", "timeout_seconds": 5,
			"api_key": map[string]any{"action": "replace", "value": embeddingToken},
		},
		"stage3": map[string]any{
			"enabled": true, "endpoint": model.URL + "/v1", "model": "chat-test", "timeout_seconds": 5, "interval_minutes": 360,
			"api_key": map[string]any{"action": "replace", "value": stage3Token},
		},
	}
	payload, _ := json.Marshal(body)
	update := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/settings/ai", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(update, req)
	if update.Code != http.StatusOK {
		t.Fatalf("settings update status=%d body=%s", update.Code, update.Body.String())
	}

	for _, test := range []struct {
		path   string
		target string
	}{
		{path: "/v1/settings/ai/test/stage3", target: "stage3"},
		{path: "/v1/settings/ai/test/embedding", target: "embedding"},
	} {
		unauthorized := httptest.NewRecorder()
		handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, test.path, nil))
		if unauthorized.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status=%d, want 401", test.target, unauthorized.Code)
		}

		response := httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, test.path, nil)
		req.Header.Set("Authorization", "Bearer "+authToken)
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("%s test status=%d body=%s", test.target, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), stage3Token) || strings.Contains(response.Body.String(), embeddingToken) {
			t.Fatalf("%s test response leaked a secret: %s", test.target, response.Body.String())
		}
		var result runtimeAITestResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.OK || result.Target != test.target || result.Message == "" {
			t.Fatalf("unexpected %s test result: %#v", test.target, result)
		}
	}
	if stage3Authorization != "Bearer "+stage3Token {
		t.Fatalf("stage3 authorization=%q", stage3Authorization)
	}
	if stage3Path != "/v1/chat/completions" {
		t.Fatalf("stage3 request path=%q", stage3Path)
	}
	if embeddingAuthorization != "Bearer "+embeddingToken {
		t.Fatalf("embedding authorization=%q", embeddingAuthorization)
	}
}

func TestRuntimeAISettingsAPIRevisionConflictAndReviewNodeValidation(t *testing.T) {
	const authToken = "nexus-ai-revision-token"
	server := newRuntimeSettingsHTTPServer(t, config.Config{
		AuthToken: authToken, RequireAuth: true,
		EmbeddingModel: recall.DefaultEmbeddingModel, EmbeddingTimeout: 30 * time.Second,
		ModelTimeout: 60 * time.Second, EvolutionInterval: 6 * time.Hour,
	})
	handler := server.Handler()
	reviewNode := pairProjectHTTPTestNode(t, server.agentDock, "device_stage3_review_node", "ReviewNode")

	get := func() settings.View {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/settings/ai", nil)
		req.Header.Set("Authorization", "Bearer "+authToken)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("GET status=%d body=%s", res.Code, res.Body.String())
		}
		var out struct {
			Settings settings.View `json:"settings"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Settings
	}
	put := func(revision, reviewNodeID string, want int) {
		t.Helper()
		body := map[string]any{
			"expected_revision": revision,
			"embedding":         map[string]any{"enabled": false, "endpoint": "", "model": recall.DefaultEmbeddingModel, "timeout_seconds": 30, "api_key": map[string]any{"action": "keep"}},
			"stage3": map[string]any{
				"enabled": false, "endpoint": "", "model": "", "timeout_seconds": 60, "interval_minutes": 360,
				"api_key": map[string]any{"action": "keep"}, "system_prompt": map[string]any{"action": "keep"}, "review_node_id": reviewNodeID,
			},
		}
		payload, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/v1/settings/ai", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+authToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", revision)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("PUT status=%d want=%d body=%s", res.Code, want, res.Body.String())
		}
	}

	initial := get()
	if initial.Revision != "rev-0" {
		t.Fatalf("initial revision=%q", initial.Revision)
	}
	put(initial.Revision, reviewNode.ID, http.StatusOK)
	current := get()
	if current.Revision != "rev-1" || current.Stage3.ReviewNodeID != reviewNode.ID {
		t.Fatalf("current=%#v", current)
	}
	put(initial.Revision, reviewNode.ID, http.StatusPreconditionFailed)
	enabled := false
	if _, err := server.agentDock.Update(t.Context(), reviewNode.ID, agentdock.UpdateInput{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	// Disabling a previously configured review node must not silently clear or replace it, and
	// unrelated settings saves may keep the stale reference. Stage 3 routing will refuse it.
	put(current.Revision, reviewNode.ID, http.StatusOK)
	final := get()
	if final.Revision != "rev-2" || final.Stage3.ReviewNodeID != reviewNode.ID {
		t.Fatalf("disabled review node was not preserved=%#v", final)
	}
	put(final.Revision, "node_missing", http.StatusBadRequest)
	unchanged := get()
	if unchanged.Revision != "rev-2" || unchanged.Stage3.ReviewNodeID != reviewNode.ID {
		t.Fatalf("invalid update changed state=%#v", unchanged)
	}
}

func TestRuntimeAISettingsAPIRejectsExplicitNullPrompt(t *testing.T) {
	const authToken = "nexus-ai-null-prompt-token"
	server := newRuntimeSettingsHTTPServer(t, config.Config{AuthToken: authToken, RequireAuth: true, EmbeddingModel: recall.DefaultEmbeddingModel, EmbeddingTimeout: 30 * time.Second, ModelTimeout: 60 * time.Second, EvolutionInterval: 6 * time.Hour})
	body := `{"embedding":{"enabled":false,"endpoint":"","model":"BAAI/bge-m3","timeout_seconds":30,"api_key":{"action":"keep"}},"stage3":{"enabled":false,"endpoint":"","model":"","timeout_seconds":60,"interval_minutes":360,"api_key":{"action":"keep"},"system_prompt":null,"review_node_id":""}}`
	req := httptest.NewRequest(http.MethodPut, "/v1/settings/ai", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestRuntimeAISettingsLateApplyReloadsLatestRevision(t *testing.T) {
	server := newRuntimeSettingsHTTPServer(t, config.Config{EmbeddingModel: recall.DefaultEmbeddingModel, EmbeddingTimeout: 30 * time.Second, ModelTimeout: 60 * time.Second, EvolutionInterval: 6 * time.Hour})
	first := settings.UpdateInput{
		Embedding:        settings.EmbeddingInput{Enabled: false, Model: recall.DefaultEmbeddingModel, TimeoutSeconds: 30, APIKey: settings.SecretInput{Action: "keep"}},
		Stage3:           settings.Stage3Input{Enabled: false, Model: "model-one", TimeoutSeconds: 60, IntervalMinutes: 360, APIKey: settings.SecretInput{Action: "keep"}, SystemPrompt: settings.PromptInput{Action: "replace", Value: "prompt one"}},
		ExpectedRevision: "rev-0",
	}
	if _, view, err := server.settings.Update(t.Context(), first); err != nil || view.Revision != "rev-1" {
		t.Fatalf("first commit: view=%#v err=%v", view, err)
	}
	second := first
	second.ExpectedRevision = "rev-1"
	second.Stage3.Model = "model-two"
	second.Stage3.SystemPrompt = settings.PromptInput{Action: "replace", Value: "prompt two"}
	if _, view, err := server.settings.Update(t.Context(), second); err != nil || view.Revision != "rev-2" {
		t.Fatalf("second commit: view=%#v err=%v", view, err)
	}
	cfg, view, err := server.applyCurrentRuntimeAISettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != "rev-2" || cfg.ModelName != "model-two" || cfg.ModelSystemPrompt != "prompt two" || server.currentConfig().ModelSystemPrompt != "prompt two" {
		t.Fatalf("B apply = cfg=%#v view=%#v current=%#v", cfg, view, server.currentConfig())
	}
	// Simulated late A apply still reloads the latest committed revision.
	cfg, view, err = server.applyCurrentRuntimeAISettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != "rev-2" || cfg.ModelName != "model-two" || server.currentConfig().ModelSystemPrompt != "prompt two" {
		t.Fatalf("late A apply regressed settings: cfg=%#v view=%#v", cfg, view)
	}
}
