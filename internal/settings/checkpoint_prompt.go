package settings

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxCheckpointPromptBytes = 8 * 1024

var ErrCheckpointUnavailable = errors.New("Checkpoint 提示词存储不可用")

type CheckpointStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewCheckpointStore(db *sql.DB) (*CheckpointStore, error) {
	if db == nil {
		return nil, ErrCheckpointUnavailable
	}
	return &CheckpointStore{db: db, now: time.Now}, nil
}

//go:embed checkpoint_prompt_default.txt
var defaultCheckpointPrompt string

var ErrCheckpointPromptRevisionConflict = errors.New("Checkpoint 提示词已被修改，请重新加载后再保存")

type CheckpointPromptView struct {
	Prompt        string `json:"prompt"`
	DefaultPrompt string `json:"default_prompt"`
	Source        string `json:"source"`
	Revision      string `json:"revision"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	MaxBytes      int    `json:"max_bytes"`
}

type CheckpointPromptUpdate struct {
	ExpectedRevision string  `json:"expected_revision"`
	Action           string  `json:"action"`
	Prompt           *string `json:"prompt,omitempty"`
}

func DefaultCheckpointPrompt() CheckpointPromptView {
	return checkpointPromptView(sql.NullString{}, 0, "")
}

func checkpointPromptView(prompt sql.NullString, revision int64, updatedAt string) CheckpointPromptView {
	view := CheckpointPromptView{Prompt: defaultCheckpointPrompt, DefaultPrompt: defaultCheckpointPrompt,
		Source: "bundled_default", Revision: fmt.Sprintf("rev-%d", revision), UpdatedAt: updatedAt, MaxBytes: MaxCheckpointPromptBytes}
	if prompt.Valid {
		view.Prompt, view.Source = prompt.String, "custom"
	}
	return view
}

func validateCheckpointPrompt(prompt string) error {
	if !utf8.ValidString(prompt) || strings.TrimSpace(prompt) == "" {
		return ValidationError{Message: "Checkpoint 提示词必须是非空 UTF-8 文本"}
	}
	if len(prompt) > MaxCheckpointPromptBytes {
		return ValidationError{Message: "Checkpoint 提示词不能超过 8 KiB"}
	}
	return nil
}

func (s *CheckpointStore) LoadCheckpointPrompt(ctx context.Context) (CheckpointPromptView, error) {
	if s == nil || s.db == nil {
		return CheckpointPromptView{}, ErrCheckpointUnavailable
	}
	var prompt sql.NullString
	var revision int64
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT prompt, revision, updated_at FROM checkpoint_prompt_settings WHERE singleton_id = 1`).Scan(&prompt, &revision, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultCheckpointPrompt(), nil
	}
	if err != nil {
		return CheckpointPromptView{}, fmt.Errorf("读取 Checkpoint 提示词: %w", err)
	}
	if prompt.Valid {
		if err := validateCheckpointPrompt(prompt.String); err != nil {
			return CheckpointPromptView{}, fmt.Errorf("已保存的 Checkpoint 提示词无效: %w", err)
		}
	}
	return checkpointPromptView(prompt, revision, updatedAt), nil
}

func (s *CheckpointStore) UpdateCheckpointPrompt(ctx context.Context, input CheckpointPromptUpdate) (CheckpointPromptView, error) {
	if s == nil || s.db == nil {
		return CheckpointPromptView{}, ErrCheckpointUnavailable
	}
	if input.ExpectedRevision == "" {
		return CheckpointPromptView{}, ValidationError{Message: "expected_revision 不能为空"}
	}
	var prompt sql.NullString
	switch input.Action {
	case "replace":
		if input.Prompt == nil {
			return CheckpointPromptView{}, ValidationError{Message: "replace 必须提供 prompt"}
		}
		if err := validateCheckpointPrompt(*input.Prompt); err != nil {
			return CheckpointPromptView{}, err
		}
		prompt = sql.NullString{String: *input.Prompt, Valid: true}
	case "reset":
		if input.Prompt != nil {
			return CheckpointPromptView{}, ValidationError{Message: "reset 不接受 prompt"}
		}
	default:
		return CheckpointPromptView{}, ValidationError{Message: "action 必须为 replace 或 reset"}
	}
	updatedAt := s.now().UTC().Format(time.RFC3339Nano)
	// 单条语句完成版本检查和写入；reset 保留递增版本，避免旧编辑器覆盖新设置。
	var revision int64
	var err error
	if input.ExpectedRevision == "rev-0" {
		err = s.db.QueryRowContext(ctx, `INSERT INTO checkpoint_prompt_settings(singleton_id, prompt, revision, updated_at)
			VALUES(1, ?, 1, ?) ON CONFLICT(singleton_id) DO NOTHING RETURNING revision`, prompt, updatedAt).Scan(&revision)
	} else {
		err = s.db.QueryRowContext(ctx, `UPDATE checkpoint_prompt_settings SET prompt=?, revision=revision+1, updated_at=?
			WHERE singleton_id=1 AND ?='rev-' || revision RETURNING revision`, prompt, updatedAt, input.ExpectedRevision).Scan(&revision)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return CheckpointPromptView{}, ErrCheckpointPromptRevisionConflict
	}
	if err != nil {
		return CheckpointPromptView{}, fmt.Errorf("保存 Checkpoint 提示词: %w", err)
	}
	return checkpointPromptView(prompt, revision, updatedAt), nil
}
