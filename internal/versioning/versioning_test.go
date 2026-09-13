package versioning

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "gc.auto", "0")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Recall\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@local", "commit", "-m", "init")
	return dir
}

func TestDiffAndRecordLocalVersion(t *testing.T) {
	dir := initRepo(t)
	mgr := NewManager(dir, slog.Default())
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "profile.md"), []byte("# Profile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := mgr.Diff(ctx)
	if err != nil || !diff.GitRepo || !diff.Dirty || len(diff.Files) != 1 {
		t.Fatalf("dirty diff = %#v err=%v", diff, err)
	}
	result, err := mgr.Record(ctx)
	if err != nil || !result.Created || result.Commit.Subject != commitMessage {
		t.Fatalf("record = %#v err=%v", result, err)
	}
	diff, err = mgr.Diff(ctx)
	if err != nil || diff.Dirty {
		t.Fatalf("clean diff = %#v err=%v", diff, err)
	}
}

func TestRecordNeverTouchesConfiguredRemote(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "remote", "add", "origin", "https://127.0.0.1:1/must-not-be-contacted.git")
	if err := os.WriteFile(filepath.Join(dir, "local.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(dir, slog.Default())
	if result, err := mgr.Record(context.Background()); err != nil || !result.Created {
		t.Fatalf("local record unexpectedly depended on remote: result=%#v err=%v", result, err)
	}
}

func TestRuntimeStateExcludedFromVersion(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(dir, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nexus", "runtime.db"), []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.md"), []byte("# Profile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(dir, slog.Default())
	if _, err := mgr.Record(ctx); err != nil {
		t.Fatal(err)
	}
	if files := runGit(t, dir, "show", "--pretty=", "--name-only", "HEAD"); files != "profile.md" {
		t.Fatalf("runtime state leaked into version: %q", files)
	}
}

func TestRecordRejectsUnsafeMarkdownLoss(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		path := filepath.Join(dir, "recall", "bulk", fmt.Sprintf("doc-%02d.md", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("line\n", 20)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "recall/bulk")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@local", "commit", "-m", "seed")
	if err := os.RemoveAll(filepath.Join(dir, "recall", "bulk")); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(dir, slog.Default())
	if _, err := mgr.Record(ctx); err == nil || !strings.Contains(err.Error(), "bulk markdown") {
		t.Fatalf("expected bulk deletion guard, got %v", err)
	}
	if got := runGit(t, dir, "log", "-1", "--pretty=%s"); got != "seed" {
		t.Fatalf("unsafe version was committed: %q", got)
	}
}

func TestRecordRejectsPrivateNotePlaintextAndKeys(t *testing.T) {
	for _, rel := range []string{
		"private-notes/notes/recovery/secret.md",
		"private-notes/.keys/private-notes-age-identity.txt",
	} {
		t.Run(rel, func(t *testing.T) {
			dir := initRepo(t)
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "profile.md"), []byte("# Dirty\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mgr := NewManager(dir, slog.Default())
			if _, err := mgr.Record(context.Background()); err == nil || !strings.Contains(err.Error(), "private note plaintext or keys") {
				t.Fatalf("expected private note guard, got %v", err)
			}
		})
	}
}

func TestMarkChangedRecordsVersion(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "changed.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	NewManager(dir, slog.Default()).MarkChanged(context.Background())
	if got := runGit(t, dir, "log", "-1", "--pretty=%s"); got != commitMessage {
		t.Fatalf("MarkChanged did not record local version: %q", got)
	}
}
