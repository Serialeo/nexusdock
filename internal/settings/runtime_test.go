package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/nexusdock/internal/config"
	"github.com/uvwt/nexusdock/internal/core"
	"github.com/uvwt/nexusdock/internal/stage3"
)

func newRuntimeSettingsTestStore(t *testing.T, defaults config.Config) (*Store, *sql.DB) {
	t.Helper()
	db, err := core.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "nexus.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := core.EnsureSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	store, err := NewStore(db, dataDir, defaults)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo, err := os.Stat(filepath.Join(dataDir, "secrets", "runtime-ai-settings.key"))
	if err != nil || keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("runtime settings key permission = %v, err=%v", keyInfo, err)
	}
	return store, db
}

func TestRuntimeSettingsFallsBackToEnvironmentDefaults(t *testing.T) {
	defaults := config.Config{
		EmbeddingEnabled: true, EmbeddingEndpoint: "http://embedding.local/v1/embeddings", EmbeddingModel: "default-embedding",
		EmbeddingAPIKey: "env-embedding-key", EmbeddingTimeout: 25 * time.Second,
		EvolutionEnabled: true, ModelEndpoint: "https://model.local/v1/chat/completions", ModelName: "default-model",
		ModelAPIKey: "env-model-key", ModelTimeout: 45 * time.Second, EvolutionInterval: 6 * time.Hour,
	}
	store, _ := newRuntimeSettingsTestStore(t, defaults)
	cfg, view, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Persisted || view.Revision != "rev-0" || cfg.EmbeddingModel != "default-embedding" || cfg.ModelName != "default-model" {
		t.Fatalf("unexpected fallback settings: cfg=%#v view=%#v", cfg, view)
	}
	if !view.Embedding.APIKeyConfigured || !view.Stage3.APIKeyConfigured {
		t.Fatalf("environment secrets should only be exposed as configured flags: %#v", view)
	}
}

func TestRuntimeSettingsEncryptsSecretsAndSupportsKeepReplaceClear(t *testing.T) {
	defaults := config.Config{EmbeddingModel: "BAAI/bge-m3", EmbeddingTimeout: 30 * time.Second, ModelTimeout: 60 * time.Second, EvolutionInterval: 6 * time.Hour}
	store, db := newRuntimeSettingsTestStore(t, defaults)
	input := UpdateInput{
		Embedding: EmbeddingInput{Enabled: true, Endpoint: "http://embedding.local/v1/embeddings", Model: "bge-m3", TimeoutSeconds: 20, APIKey: SecretInput{Action: "replace", Value: "embedding-secret-value"}},
		Stage3:    Stage3Input{Enabled: true, Endpoint: "https://model.local/v1/chat/completions", Model: "gpt-example", TimeoutSeconds: 40, IntervalMinutes: 360, APIKey: SecretInput{Action: "replace", Value: "model-secret-value"}},
	}
	cfg, view, err := store.Update(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddingAPIKey != "embedding-secret-value" || cfg.ModelAPIKey != "model-secret-value" || !view.Persisted {
		t.Fatalf("updated settings mismatch: cfg=%#v view=%#v", cfg, view)
	}
	if !view.Embedding.APIKeyConfigured || !view.Stage3.APIKeyConfigured {
		t.Fatalf("configured flags missing: %#v", view)
	}

	rows, err := db.QueryContext(t.Context(), `SELECT ciphertext FROM runtime_ai_setting_secrets ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ciphertext []byte
		if err := rows.Scan(&ciphertext); err != nil {
			t.Fatal(err)
		}
		text := string(ciphertext)
		if strings.Contains(text, "embedding-secret-value") || strings.Contains(text, "model-secret-value") {
			t.Fatal("database contains plaintext runtime AI secret")
		}
	}

	input.Embedding.APIKey = SecretInput{Action: "keep"}
	input.Stage3.APIKey = SecretInput{Action: "clear"}
	cfg, view, err = store.Update(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddingAPIKey != "embedding-secret-value" || cfg.ModelAPIKey != "" {
		t.Fatalf("keep/clear semantics failed: embedding=%q model=%q", cfg.EmbeddingAPIKey, cfg.ModelAPIKey)
	}
	if !view.Embedding.APIKeyConfigured || view.Stage3.APIKeyConfigured {
		t.Fatalf("configured flags after clear mismatch: %#v", view)
	}
}

func TestRuntimeSettingsRejectsInvalidInput(t *testing.T) {
	store, _ := newRuntimeSettingsTestStore(t, config.Config{})
	_, _, err := store.Update(context.Background(), UpdateInput{
		Embedding: EmbeddingInput{Enabled: true, Endpoint: "file:///tmp/embed", Model: "bge", TimeoutSeconds: 30, APIKey: SecretInput{Action: "keep"}},
		Stage3:    Stage3Input{Enabled: false, TimeoutSeconds: 60, IntervalMinutes: 360, APIKey: SecretInput{Action: "keep"}},
	})
	var validation ValidationError
	if err == nil || !strings.Contains(err.Error(), "HTTP") || !strings.Contains(err.Error(), "向量") {
		t.Fatalf("invalid endpoint accepted: %v", err)
	}
	_ = validation
}

func TestRuntimeSettingsStage3PromptOverrideKeepAndReset(t *testing.T) {
	defaults := config.Config{
		EmbeddingModel: "BAAI/bge-m3", EmbeddingTimeout: 30 * time.Second,
		ModelTimeout: 60 * time.Second, EvolutionInterval: 6 * time.Hour,
	}
	store, db := newRuntimeSettingsTestStore(t, defaults)

	cfg, view, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelSystemPrompt != stage3.BundledDefaultPrompt() || view.Stage3.SystemPrompt != stage3.BundledDefaultPrompt() {
		t.Fatalf("default prompt mismatch: cfg=%q view=%q", cfg.ModelSystemPrompt, view.Stage3.SystemPrompt)
	}
	if view.Stage3.BundledSystemPrompt != stage3.BundledDefaultPrompt() || view.Stage3.SystemPromptSource != "bundled_default" || view.Stage3.SystemPromptUpdatedAt != "" {
		t.Fatalf("default prompt metadata = %#v", view.Stage3)
	}

	custom := "Custom Stage 3 prompt.\nPreserve this exact text.  "
	input := validRuntimeSettingsInput()
	input.Stage3.SystemPrompt = PromptInput{Action: "replace", Value: custom}
	cfg, view, err = store.Update(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelSystemPrompt != custom || view.Stage3.SystemPrompt != custom || view.Stage3.SystemPromptSource != "custom" || view.Stage3.SystemPromptUpdatedAt == "" {
		t.Fatalf("custom prompt state = cfg=%q view=%#v", cfg.ModelSystemPrompt, view.Stage3)
	}
	var stored string
	if err := db.QueryRowContext(t.Context(), `SELECT prompt FROM stage3_prompt_overrides WHERE singleton_id = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != custom {
		t.Fatalf("stored prompt = %q, want exact %q", stored, custom)
	}

	input.Stage3.Model = "changed-model"
	input.Stage3.SystemPrompt = PromptInput{Action: "keep"}
	cfg, view, err = store.Update(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelName != "changed-model" || cfg.ModelSystemPrompt != custom || view.Stage3.SystemPromptSource != "custom" {
		t.Fatalf("keep lost custom prompt: cfg=%#v view=%#v", cfg, view.Stage3)
	}

	input.Stage3.SystemPrompt = PromptInput{Action: "reset"}
	cfg, view, err = store.Update(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelSystemPrompt != stage3.BundledDefaultPrompt() || view.Stage3.SystemPrompt != stage3.BundledDefaultPrompt() || view.Stage3.SystemPromptSource != "bundled_default" || view.Stage3.SystemPromptUpdatedAt != "" {
		t.Fatalf("reset prompt state = cfg=%q view=%#v", cfg.ModelSystemPrompt, view.Stage3)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM stage3_prompt_overrides`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("prompt override rows after reset = %d", count)
	}
}

func TestRuntimeSettingsRejectsInvalidStage3PromptOverride(t *testing.T) {
	store, _ := newRuntimeSettingsTestStore(t, config.Config{})
	for name, prompt := range map[string]string{
		"empty":    " \n\t ",
		"oversize": strings.Repeat("a", stage3.MaxSystemPromptBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			input := validRuntimeSettingsInput()
			input.Stage3.SystemPrompt = PromptInput{Action: "replace", Value: prompt}
			if _, _, err := store.Update(t.Context(), input); err == nil {
				t.Fatalf("invalid prompt accepted: %s", name)
			}
		})
	}
}

func validRuntimeSettingsInput() UpdateInput {
	return UpdateInput{
		Embedding: EmbeddingInput{Enabled: false, TimeoutSeconds: 30, APIKey: SecretInput{Action: "keep"}},
		Stage3: Stage3Input{
			Enabled: false, TimeoutSeconds: 60, IntervalMinutes: 360,
			APIKey: SecretInput{Action: "keep"}, SystemPrompt: PromptInput{Action: "keep"},
		},
	}
}

func TestRuntimeSettingsRevisionCASAndReviewNodePersistence(t *testing.T) {
	store, _ := newRuntimeSettingsTestStore(t, config.Config{})
	input := validRuntimeSettingsInput()
	input.ExpectedRevision = "rev-0"
	input.Stage3.ReviewNodeID = "node_review"
	cfg, view, err := store.Update(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != "rev-1" || view.Stage3.ReviewNodeID != "node_review" || cfg.Stage3ReviewNodeID != "node_review" {
		t.Fatalf("first revision/review state: cfg=%#v view=%#v", cfg, view)
	}
	stale := input
	stale.Stage3.ReviewNodeID = "node_stale"
	stale.ExpectedRevision = "rev-0"
	if _, _, err := store.Update(t.Context(), stale); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	cfg, view, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != "rev-1" || cfg.Stage3ReviewNodeID != "node_review" {
		t.Fatalf("stale update changed settings: cfg=%#v view=%#v", cfg, view)
	}
}

func TestRuntimeSettingsPromptJSONPresenceSemantics(t *testing.T) {
	base := `{"embedding":{"enabled":false,"endpoint":"","model":"","timeout_seconds":30,"api_key":{"action":"keep"}},"stage3":{"enabled":false,"endpoint":"","model":"","timeout_seconds":60,"interval_minutes":360,"api_key":{"action":"keep"}%s}}`
	for name, fragment := range map[string]string{
		"omitted":        ``,
		"explicit_empty": `,"system_prompt":{}`,
	} {
		t.Run(name, func(t *testing.T) {
			var input UpdateInput
			if err := json.Unmarshal([]byte(fmt.Sprintf(base, fragment)), &input); err != nil {
				t.Fatal(err)
			}
			if _, err := normalizeInput(input); err != nil {
				t.Fatalf("compatible keep form rejected: %v", err)
			}
		})
	}
	for name, fragment := range map[string]string{
		"null":            `,"system_prompt":null`,
		"value_no_action": `,"system_prompt":{"value":"custom"}`,
		"null_value":      `,"system_prompt":{"action":"replace","value":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			var input UpdateInput
			if err := json.Unmarshal([]byte(fmt.Sprintf(base, fragment)), &input); err != nil {
				// Wrong explicit types may be rejected by JSON decoding itself, which is also correct.
				return
			}
			if _, err := normalizeInput(input); err == nil {
				t.Fatalf("invalid prompt form accepted: %s", fragment)
			}
		})
	}
}
