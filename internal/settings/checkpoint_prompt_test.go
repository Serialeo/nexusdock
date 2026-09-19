package settings

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/nexusdock/internal/core"
)

func newCheckpointPromptTestStore(t *testing.T) *CheckpointStore {
	t.Helper()
	db, err := core.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "nexus.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := core.EnsureSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	store, err := NewCheckpointStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCheckpointPromptPersistsExactTextAndResetKeepsRevision(t *testing.T) {
	store := newCheckpointPromptTestStore(t)
	initial, err := store.LoadCheckpointPrompt(t.Context())
	if err != nil || initial.Revision != "rev-0" || initial.Prompt == "" || initial.Source != "bundled_default" {
		t.Fatalf("initial=%#v err=%v", initial, err)
	}
	custom := "自定义 checkpoint\n保留文本和末尾空格。  "
	saved, err := store.UpdateCheckpointPrompt(t.Context(), CheckpointPromptUpdate{ExpectedRevision: initial.Revision, Action: "replace", Prompt: &custom})
	if err != nil || saved.Revision != "rev-1" || saved.Prompt != custom {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
	reopened, _ := NewCheckpointStore(store.db)
	loaded, err := reopened.LoadCheckpointPrompt(t.Context())
	if err != nil || loaded != saved {
		t.Fatalf("reload=%#v err=%v", loaded, err)
	}
	if _, err := store.UpdateCheckpointPrompt(t.Context(), CheckpointPromptUpdate{ExpectedRevision: initial.Revision, Action: "reset"}); !errors.Is(err, ErrCheckpointPromptRevisionConflict) {
		t.Fatalf("stale reset=%v", err)
	}
	reset, err := store.UpdateCheckpointPrompt(t.Context(), CheckpointPromptUpdate{ExpectedRevision: saved.Revision, Action: "reset"})
	if err != nil || reset.Revision != "rev-2" || reset.Source != "bundled_default" || reset.Prompt != initial.Prompt {
		t.Fatalf("reset=%#v err=%v", reset, err)
	}
	if _, err := store.UpdateCheckpointPrompt(t.Context(), CheckpointPromptUpdate{ExpectedRevision: saved.Revision, Action: "replace", Prompt: &custom}); !errors.Is(err, ErrCheckpointPromptRevisionConflict) {
		t.Fatalf("reset lost version: %v", err)
	}
}

func TestCheckpointPromptRejectsInvalidAndConcurrentEdits(t *testing.T) {
	store := newCheckpointPromptTestStore(t)
	for _, prompt := range []string{" \n\t", strings.Repeat("中", MaxCheckpointPromptBytes/3+1), string([]byte{0xff})} {
		if _, err := store.UpdateCheckpointPrompt(t.Context(), CheckpointPromptUpdate{ExpectedRevision: "rev-0", Action: "replace", Prompt: &prompt}); err == nil {
			t.Fatal("accepted invalid prompt")
		}
	}
	for _, input := range []CheckpointPromptUpdate{{Action: "reset"}, {ExpectedRevision: "rev-0", Action: "replace"}, {ExpectedRevision: "rev-0", Action: "clear"}} {
		if _, err := store.UpdateCheckpointPrompt(t.Context(), input); err == nil {
			t.Fatalf("accepted %#v", input)
		}
	}
	results := make(chan error, 2)
	for _, text := range []string{"editor one", "editor two"} {
		go func() {
			_, err := store.UpdateCheckpointPrompt(t.Context(), CheckpointPromptUpdate{ExpectedRevision: "rev-0", Action: "replace", Prompt: &text})
			results <- err
		}()
	}
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrCheckpointPromptRevisionConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
