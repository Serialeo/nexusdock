package settings

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uvwt/nexusdock/internal/config"
	"github.com/uvwt/nexusdock/internal/stage3"
)

const (
	secretVersion  = byte(1)
	maxSecretBytes = 64 * 1024
)

var ErrUnavailable = errors.New("运行时 AI 设置存储不可用")
var ErrRevisionConflict = errors.New("运行时 AI 设置 revision conflict")

type ValidationError struct{ Message string }

func (e ValidationError) Error() string { return e.Message }

type SecretInput struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

type PromptInput struct {
	Action       string `json:"action"`
	Value        string `json:"value,omitempty"`
	present      bool
	nullValue    bool
	valuePresent bool
}

func (p *PromptInput) UnmarshalJSON(data []byte) error {
	p.present = true
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		p.nullValue = true
		return nil
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var wire struct {
		Action *string         `json:"action"`
		Value  json.RawMessage `json:"value"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return err
	}
	if wire.Action != nil {
		p.Action = *wire.Action
	}
	if valueRaw, exists := raw["value"]; exists {
		p.valuePresent = true
		if string(valueRaw) == "null" {
			p.nullValue = true
			return nil
		}
		if err := json.Unmarshal(valueRaw, &p.Value); err != nil {
			return err
		}
	}
	return nil
}

type EmbeddingInput struct {
	Enabled        bool        `json:"enabled"`
	Endpoint       string      `json:"endpoint"`
	Model          string      `json:"model"`
	TimeoutSeconds int         `json:"timeout_seconds"`
	APIKey         SecretInput `json:"api_key"`
}

type Stage3Input struct {
	Enabled         bool        `json:"enabled"`
	Endpoint        string      `json:"endpoint"`
	Model           string      `json:"model"`
	TimeoutSeconds  int         `json:"timeout_seconds"`
	IntervalMinutes int         `json:"interval_minutes"`
	APIKey          SecretInput `json:"api_key"`
	SystemPrompt    PromptInput `json:"system_prompt"`
	ReviewNodeID    string      `json:"review_node_id"`
}

type UpdateInput struct {
	Embedding        EmbeddingInput `json:"embedding"`
	Stage3           Stage3Input    `json:"stage3"`
	ExpectedRevision string         `json:"expected_revision,omitempty"`
}

type EmbeddingView struct {
	Enabled          bool   `json:"enabled"`
	Endpoint         string `json:"endpoint"`
	Model            string `json:"model"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

type Stage3View struct {
	Enabled               bool   `json:"enabled"`
	Endpoint              string `json:"endpoint"`
	Model                 string `json:"model"`
	TimeoutSeconds        int    `json:"timeout_seconds"`
	IntervalMinutes       int    `json:"interval_minutes"`
	APIKeyConfigured      bool   `json:"api_key_configured"`
	Configured            bool   `json:"configured"`
	SystemPrompt          string `json:"system_prompt"`
	BundledSystemPrompt   string `json:"bundled_system_prompt"`
	SystemPromptSource    string `json:"system_prompt_source"`
	SystemPromptUpdatedAt string `json:"system_prompt_updated_at,omitempty"`
	ReviewNodeID          string `json:"review_node_id"`
}

type View struct {
	Embedding EmbeddingView `json:"embedding"`
	Stage3    Stage3View    `json:"stage3"`
	Persisted bool          `json:"persisted"`
	Revision  string        `json:"revision"`
	UpdatedAt string        `json:"updated_at,omitempty"`
}

type Store struct {
	db       *sql.DB
	defaults config.Config
	cipher   cipher.AEAD
	now      func() time.Time
}

func NewStore(db *sql.DB, dataDir string, defaults config.Config) (*Store, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	key, err := loadOrCreateKey(filepath.Join(dataDir, "secrets", "runtime-ai-settings.key"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化运行时 AI 设置加密器: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化运行时 AI 设置 GCM: %w", err)
	}
	return &Store{db: db, defaults: defaults, cipher: aead, now: time.Now}, nil
}

func (s *Store) Load(ctx context.Context) (config.Config, View, error) {
	if s == nil || s.db == nil {
		return config.Config{}, View{}, ErrUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return config.Config{}, View{}, fmt.Errorf("开始读取运行时 AI 设置: %w", err)
	}
	defer tx.Rollback()
	cfg := s.defaults
	var embeddingEnabled, stage3Enabled int
	var embeddingTimeout, stage3Timeout, interval int
	var revision int64
	var updatedAt string
	err = tx.QueryRowContext(ctx, `SELECT embedding_enabled, embedding_endpoint, embedding_model, embedding_timeout_seconds,
		stage3_enabled, stage3_endpoint, stage3_model, stage3_timeout_seconds, stage3_interval_minutes,
		stage3_review_node_id, revision, updated_at
		FROM runtime_ai_settings WHERE singleton_id = 1`).Scan(
		&embeddingEnabled, &cfg.EmbeddingEndpoint, &cfg.EmbeddingModel, &embeddingTimeout,
		&stage3Enabled, &cfg.ModelEndpoint, &cfg.ModelName, &stage3Timeout, &interval,
		&cfg.Stage3ReviewNodeID, &revision, &updatedAt,
	)
	persisted := true
	if errors.Is(err, sql.ErrNoRows) {
		persisted = false
		revision = 0
	} else if err != nil {
		return config.Config{}, View{}, fmt.Errorf("读取运行时 AI 设置: %w", err)
	} else {
		cfg.EmbeddingEnabled = embeddingEnabled == 1
		cfg.EmbeddingTimeout = time.Duration(embeddingTimeout) * time.Second
		cfg.EvolutionEnabled = stage3Enabled == 1
		cfg.ModelTimeout = time.Duration(stage3Timeout) * time.Second
		cfg.EvolutionInterval = time.Duration(interval) * time.Minute
	}

	if value, found, err := s.loadSecret(ctx, tx, "embedding_api_key"); err != nil {
		return config.Config{}, View{}, err
	} else if found {
		cfg.EmbeddingAPIKey = value
	}
	if value, found, err := s.loadSecret(ctx, tx, "stage3_api_key"); err != nil {
		return config.Config{}, View{}, err
	} else if found {
		cfg.ModelAPIKey = value
	}
	prompt, promptSource, promptUpdatedAt, err := s.loadStage3Prompt(ctx, tx)
	if err != nil {
		return config.Config{}, View{}, err
	}
	cfg.ModelSystemPrompt = prompt
	if err := tx.Commit(); err != nil {
		return config.Config{}, View{}, fmt.Errorf("提交运行时 AI 设置读取快照: %w", err)
	}
	return cfg, viewOf(cfg, persisted, revision, updatedAt, promptSource, promptUpdatedAt), nil
}

func (s *Store) Update(ctx context.Context, input UpdateInput) (config.Config, View, error) {
	if s == nil || s.db == nil {
		return config.Config{}, View{}, ErrUnavailable
	}
	normalized, err := normalizeInput(input)
	if err != nil {
		return config.Config{}, View{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return config.Config{}, View{}, fmt.Errorf("开始更新运行时 AI 设置: %w", err)
	}
	defer tx.Rollback()

	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM runtime_ai_settings WHERE singleton_id = 1`).Scan(&currentRevision); errors.Is(err, sql.ErrNoRows) {
		currentRevision = 0
	} else if err != nil {
		return config.Config{}, View{}, fmt.Errorf("读取运行时 AI 设置 revision: %w", err)
	}
	if normalized.ExpectedRevision != "" && normalized.ExpectedRevision != runtimeSettingsRevision(currentRevision) {
		return config.Config{}, View{}, fmt.Errorf("%w: current=%s expected=%s", ErrRevisionConflict, runtimeSettingsRevision(currentRevision), normalized.ExpectedRevision)
	}
	nextRevision := currentRevision + 1

	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_ai_settings(
		singleton_id, embedding_enabled, embedding_endpoint, embedding_model, embedding_timeout_seconds,
		stage3_enabled, stage3_endpoint, stage3_model, stage3_timeout_seconds, stage3_interval_minutes, stage3_review_node_id, revision, updated_at
	) VALUES(1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(singleton_id) DO UPDATE SET
		embedding_enabled=excluded.embedding_enabled, embedding_endpoint=excluded.embedding_endpoint,
		embedding_model=excluded.embedding_model, embedding_timeout_seconds=excluded.embedding_timeout_seconds,
		stage3_enabled=excluded.stage3_enabled, stage3_endpoint=excluded.stage3_endpoint,
		stage3_model=excluded.stage3_model, stage3_timeout_seconds=excluded.stage3_timeout_seconds,
		stage3_interval_minutes=excluded.stage3_interval_minutes, stage3_review_node_id=excluded.stage3_review_node_id,
		revision=excluded.revision, updated_at=excluded.updated_at`,
		boolInt(normalized.Embedding.Enabled), normalized.Embedding.Endpoint, normalized.Embedding.Model, normalized.Embedding.TimeoutSeconds,
		boolInt(normalized.Stage3.Enabled), normalized.Stage3.Endpoint, normalized.Stage3.Model, normalized.Stage3.TimeoutSeconds,
		normalized.Stage3.IntervalMinutes, normalized.Stage3.ReviewNodeID, nextRevision, now)
	if err != nil {
		return config.Config{}, View{}, fmt.Errorf("保存运行时 AI 设置: %w", err)
	}
	for name, secret := range map[string]SecretInput{
		"embedding_api_key": normalized.Embedding.APIKey,
		"stage3_api_key":    normalized.Stage3.APIKey,
	} {
		if err := s.applySecret(ctx, tx, name, secret, now); err != nil {
			return config.Config{}, View{}, err
		}
	}
	if err := s.applyStage3Prompt(ctx, tx, normalized.Stage3.SystemPrompt, now); err != nil {
		return config.Config{}, View{}, err
	}
	if err := tx.Commit(); err != nil {
		return config.Config{}, View{}, fmt.Errorf("提交运行时 AI 设置: %w", err)
	}
	return s.Load(ctx)
}

func normalizeInput(input UpdateInput) (UpdateInput, error) {
	input.Embedding.Endpoint = strings.TrimSpace(input.Embedding.Endpoint)
	input.Embedding.Model = strings.TrimSpace(input.Embedding.Model)
	input.Stage3.Endpoint = strings.TrimSpace(input.Stage3.Endpoint)
	input.Stage3.Model = strings.TrimSpace(input.Stage3.Model)
	if input.Embedding.Enabled {
		if err := validateEndpoint(input.Embedding.Endpoint, "向量服务地址"); err != nil {
			return UpdateInput{}, err
		}
		if input.Embedding.Model == "" {
			return UpdateInput{}, ValidationError{Message: "向量模型不能为空"}
		}
	}
	if input.Stage3.Enabled {
		if err := validateEndpoint(input.Stage3.Endpoint, "Stage 3 模型地址"); err != nil {
			return UpdateInput{}, err
		}
		if input.Stage3.Model == "" {
			return UpdateInput{}, ValidationError{Message: "Stage 3 模型不能为空"}
		}
	}
	if input.Embedding.TimeoutSeconds < 1 || input.Embedding.TimeoutSeconds > 300 {
		return UpdateInput{}, ValidationError{Message: "向量请求超时必须在 1 到 300 秒之间"}
	}
	if input.Stage3.TimeoutSeconds < 1 || input.Stage3.TimeoutSeconds > 300 {
		return UpdateInput{}, ValidationError{Message: "Stage 3 请求超时必须在 1 到 300 秒之间"}
	}
	if input.Stage3.IntervalMinutes < 60 || input.Stage3.IntervalMinutes > 10080 {
		return UpdateInput{}, ValidationError{Message: "Stage 3 执行间隔必须在 60 到 10080 分钟之间"}
	}
	for _, secret := range []SecretInput{input.Embedding.APIKey, input.Stage3.APIKey} {
		action := strings.ToLower(strings.TrimSpace(secret.Action))
		if action != "keep" && action != "replace" && action != "clear" {
			return UpdateInput{}, ValidationError{Message: "API Key 操作必须是 keep、replace 或 clear"}
		}
		if action == "replace" {
			value := strings.TrimSpace(secret.Value)
			if value == "" {
				return UpdateInput{}, ValidationError{Message: "替换 API Key 时新值不能为空"}
			}
			if len(value) > maxSecretBytes {
				return UpdateInput{}, ValidationError{Message: "API Key 过长"}
			}
		}
	}
	input.Embedding.APIKey.Action = strings.ToLower(strings.TrimSpace(input.Embedding.APIKey.Action))
	input.Stage3.APIKey.Action = strings.ToLower(strings.TrimSpace(input.Stage3.APIKey.Action))
	promptAction := strings.ToLower(strings.TrimSpace(input.Stage3.SystemPrompt.Action))
	if input.Stage3.SystemPrompt.nullValue {
		return UpdateInput{}, ValidationError{Message: "Stage 3 System Prompt 不能为 null"}
	}
	if promptAction == "" {
		if input.Stage3.SystemPrompt.present && input.Stage3.SystemPrompt.valuePresent {
			return UpdateInput{}, ValidationError{Message: "Stage 3 System Prompt 提交 value 时必须显式指定 action"}
		}
		promptAction = "keep"
	}
	if promptAction != "keep" && promptAction != "replace" && promptAction != "reset" {
		return UpdateInput{}, ValidationError{Message: "Stage 3 System Prompt 操作必须是 keep、replace 或 reset"}
	}
	if promptAction == "replace" {
		if !utf8.ValidString(input.Stage3.SystemPrompt.Value) {
			return UpdateInput{}, ValidationError{Message: "Stage 3 System Prompt 必须是有效 UTF-8"}
		}
		if strings.TrimSpace(input.Stage3.SystemPrompt.Value) == "" {
			return UpdateInput{}, ValidationError{Message: "Stage 3 System Prompt 不能为空"}
		}
		if len([]byte(input.Stage3.SystemPrompt.Value)) > stage3.MaxSystemPromptBytes {
			return UpdateInput{}, ValidationError{Message: "Stage 3 System Prompt 不能超过 64 KiB"}
		}
	}
	input.Stage3.SystemPrompt.Action = promptAction
	input.Stage3.ReviewNodeID = strings.TrimSpace(input.Stage3.ReviewNodeID)
	input.ExpectedRevision = strings.TrimSpace(input.ExpectedRevision)
	if input.ExpectedRevision != "" {
		if _, err := parseRuntimeSettingsRevision(input.ExpectedRevision); err != nil {
			return UpdateInput{}, ValidationError{Message: "expected_revision 格式无效"}
		}
	}
	return input, nil
}

func validateEndpoint(value, label string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ValidationError{Message: label + "必须是有效的 HTTP 或 HTTPS URL，且不能包含用户凭据"}
	}
	return nil
}

func (s *Store) applySecret(ctx context.Context, tx *sql.Tx, name string, input SecretInput, updatedAt string) error {
	switch input.Action {
	case "keep":
		return nil
	case "clear", "replace":
		value := ""
		if input.Action == "replace" {
			value = strings.TrimSpace(input.Value)
		}
		sealed, err := s.seal(name, value)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_ai_setting_secrets(name, ciphertext, updated_at) VALUES(?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET ciphertext=excluded.ciphertext, updated_at=excluded.updated_at`, name, sealed, updatedAt)
		if err != nil {
			return fmt.Errorf("保存运行时 AI 密钥: %w", err)
		}
		return nil
	default:
		return ValidationError{Message: "未知 API Key 操作"}
	}
}

type runtimeSettingsQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) loadSecret(ctx context.Context, db runtimeSettingsQueryer, name string) (string, bool, error) {
	var sealed []byte
	if err := db.QueryRowContext(ctx, `SELECT ciphertext FROM runtime_ai_setting_secrets WHERE name = ?`, name).Scan(&sealed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("读取运行时 AI 密钥: %w", err)
	}
	plain, err := s.open(name, sealed)
	if err != nil {
		return "", false, err
	}
	return plain, true, nil
}

func (s *Store) loadStage3Prompt(ctx context.Context, db runtimeSettingsQueryer) (prompt, source, updatedAt string, err error) {
	var storedPrompt, storedUpdatedAt string
	err = db.QueryRowContext(ctx, `SELECT prompt, updated_at FROM stage3_prompt_overrides WHERE singleton_id = 1`).Scan(&storedPrompt, &storedUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return stage3.BundledDefaultPrompt(), "bundled_default", "", nil
	}
	if err != nil {
		return "", "", "", fmt.Errorf("读取 Stage 3 System Prompt: %w", err)
	}
	if !utf8.ValidString(storedPrompt) || strings.TrimSpace(storedPrompt) == "" || len([]byte(storedPrompt)) > stage3.MaxSystemPromptBytes {
		return "", "", "", errors.New("已保存的 Stage 3 System Prompt 无效")
	}
	return storedPrompt, "custom", storedUpdatedAt, nil
}

func (s *Store) applyStage3Prompt(ctx context.Context, tx *sql.Tx, input PromptInput, updatedAt string) error {
	switch input.Action {
	case "keep":
		return nil
	case "reset":
		if _, err := tx.ExecContext(ctx, `DELETE FROM stage3_prompt_overrides WHERE singleton_id = 1`); err != nil {
			return fmt.Errorf("恢复 Stage 3 bundled System Prompt: %w", err)
		}
		return nil
	case "replace":
		if _, err := tx.ExecContext(ctx, `INSERT INTO stage3_prompt_overrides(singleton_id, prompt, updated_at) VALUES(1, ?, ?)
			ON CONFLICT(singleton_id) DO UPDATE SET prompt=excluded.prompt, updated_at=excluded.updated_at`, input.Value, updatedAt); err != nil {
			return fmt.Errorf("保存 Stage 3 System Prompt: %w", err)
		}
		return nil
	default:
		return ValidationError{Message: "未知 Stage 3 System Prompt 操作"}
	}
}

func (s *Store) seal(name, value string) ([]byte, error) {
	nonce := make([]byte, s.cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成运行时 AI 密钥 nonce: %w", err)
	}
	sealed := s.cipher.Seal(nil, nonce, []byte(value), []byte(name))
	out := make([]byte, 1+len(nonce)+len(sealed))
	out[0] = secretVersion
	copy(out[1:], nonce)
	copy(out[1+len(nonce):], sealed)
	return out, nil
}

func (s *Store) open(name string, sealed []byte) (string, error) {
	if len(sealed) <= 1+s.cipher.NonceSize() || sealed[0] != secretVersion {
		return "", errors.New("运行时 AI 密钥格式无效")
	}
	nonceEnd := 1 + s.cipher.NonceSize()
	plain, err := s.cipher.Open(nil, sealed[1:nonceEnd], sealed[nonceEnd:], []byte(name))
	if err != nil {
		return "", errors.New("运行时 AI 密钥无法解密")
	}
	return string(plain), nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建运行时 AI 密钥目录: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("设置运行时 AI 密钥目录权限: %w", err)
	}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) != 32 {
			return nil, errors.New("运行时 AI 主密钥长度无效")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("设置运行时 AI 主密钥权限: %w", err)
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取运行时 AI 主密钥: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("生成运行时 AI 主密钥: %w", err)
	}
	file, err := os.CreateTemp(dir, ".runtime-ai-key-*")
	if err != nil {
		return nil, fmt.Errorf("创建运行时 AI 临时主密钥: %w", err)
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(temp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateKey(path)
		}
		return nil, fmt.Errorf("发布运行时 AI 主密钥: %w", err)
	}
	return key, nil
}

func viewOf(cfg config.Config, persisted bool, revision int64, updatedAt, promptSource, promptUpdatedAt string) View {
	return View{
		Embedding: EmbeddingView{
			Enabled: cfg.EmbeddingEnabled, Endpoint: cfg.EmbeddingEndpoint, Model: cfg.EmbeddingModel,
			TimeoutSeconds: int(cfg.EmbeddingTimeout / time.Second), APIKeyConfigured: strings.TrimSpace(cfg.EmbeddingAPIKey) != "",
		},
		Stage3: Stage3View{
			Enabled: cfg.EvolutionEnabled, Endpoint: cfg.ModelEndpoint, Model: cfg.ModelName,
			TimeoutSeconds: int(cfg.ModelTimeout / time.Second), IntervalMinutes: int(cfg.EvolutionInterval / time.Minute),
			APIKeyConfigured:      strings.TrimSpace(cfg.ModelAPIKey) != "",
			Configured:            cfg.EvolutionEnabled && strings.TrimSpace(cfg.ModelEndpoint) != "" && strings.TrimSpace(cfg.ModelName) != "",
			SystemPrompt:          cfg.ModelSystemPrompt,
			BundledSystemPrompt:   stage3.BundledDefaultPrompt(),
			SystemPromptSource:    promptSource,
			SystemPromptUpdatedAt: promptUpdatedAt,
			ReviewNodeID:          cfg.Stage3ReviewNodeID,
		},
		Persisted: persisted,
		Revision:  runtimeSettingsRevision(revision),
		UpdatedAt: updatedAt,
	}
}

func runtimeSettingsRevision(value int64) string {
	return "rev-" + strconv.FormatInt(value, 10)
}

func parseRuntimeSettingsRevision(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "rev-") {
		return 0, errors.New("revision must start with rev-")
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(value, "rev-"), 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("revision is invalid")
	}
	return n, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
