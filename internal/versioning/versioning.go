package versioning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const commitMessage = "recall: 记录本地版本"

type ChangedFile struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

type Diff struct {
	OK         bool          `json:"ok"`
	GitRepo    bool          `json:"git_repo"`
	Dirty      bool          `json:"dirty"`
	Status     string        `json:"status"`
	Stat       string        `json:"stat"`
	Diff       string        `json:"diff"`
	CachedDiff string        `json:"cached_diff"`
	Files      []ChangedFile `json:"files"`
}

type Commit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Date      string `json:"date"`
	Author    string `json:"author"`
	Subject   string `json:"subject"`
}

type Log struct {
	OK      bool     `json:"ok"`
	GitRepo bool     `json:"git_repo"`
	Commits []Commit `json:"commits"`
	Count   int      `json:"count"`
}

type CommitFile struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

type CommitDetail struct {
	OK      bool         `json:"ok"`
	GitRepo bool         `json:"git_repo"`
	Commit  Commit       `json:"commit"`
	Files   []CommitFile `json:"files"`
	Stat    string       `json:"stat"`
	Diff    string       `json:"diff"`
}

type RecordResult struct {
	OK      bool   `json:"ok"`
	GitRepo bool   `json:"git_repo"`
	Created bool   `json:"created"`
	Commit  Commit `json:"commit,omitempty"`
}

type Manager struct {
	repoDir string
	logger  *slog.Logger
	gitMu   sync.Mutex
}

func NewManager(repoDir string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{repoDir: repoDir, logger: logger}
}

// MarkChanged records a local Git version after a successful Recall mutation.
// Versioning is intentionally local-only: NexusDock never reads or writes Git remotes.
func (m *Manager) MarkChanged(ctx context.Context) {
	if _, err := m.Record(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Warn("record recall version failed", "error", err)
	}
}

func memoryGitArgs(args ...string) []string {
	return append(args, "--", ".", ":(exclude).nexus", ":(exclude).nexus/**")
}

func (m *Manager) IsGitRepo() bool {
	info, err := os.Stat(filepath.Join(m.repoDir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

func (m *Manager) Diff(ctx context.Context) (Diff, error) {
	resp := Diff{OK: true, GitRepo: m.IsGitRepo()}
	if !resp.GitRepo {
		return resp, nil
	}
	status, err := m.git(ctx, memoryGitArgs("status", "--short", "--untracked-files=all")...)
	if err != nil {
		return resp, err
	}
	resp.Status = status
	resp.Dirty = strings.TrimSpace(status) != ""
	resp.Files = parseChangedFiles(status)
	if stat, err := m.git(ctx, memoryGitArgs("diff", "--stat")...); err == nil {
		resp.Stat = stat
	}
	if cachedDiff, err := m.git(ctx, memoryGitArgs("diff", "--cached", "--no-ext-diff")...); err == nil {
		resp.CachedDiff = cachedDiff
	}
	if diff, err := m.git(ctx, memoryGitArgs("diff", "--no-ext-diff")...); err == nil {
		resp.Diff = diff
	}
	return resp, nil
}

func parseChangedFiles(status string) []ChangedFile {
	seen := map[string]bool{}
	files := []ChangedFile{}
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		path := strings.TrimSpace(line[3:])
		if path == "" {
			continue
		}
		if strings.Contains(path, " -> ") {
			parts := strings.Split(path, " -> ")
			path = strings.TrimSpace(parts[len(parts)-1])
		}
		path = filepath.ToSlash(strings.Trim(path, `"`))
		if seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, ChangedFile{Status: code, Path: path})
	}
	return files
}

func (m *Manager) Log(ctx context.Context, limit int) (Log, error) {
	resp := Log{OK: true, GitRepo: m.IsGitRepo()}
	if !resp.GitRepo {
		return resp, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	format := "%H%x1f%h%x1f%ad%x1f%an%x1f%s%x1e"
	out, err := m.git(ctx, "log", "--date=iso-strict", "-n", strconv.Itoa(limit), "--pretty=format:"+format)
	if err != nil {
		return resp, err
	}
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x1f")
		if len(fields) < 5 {
			continue
		}
		resp.Commits = append(resp.Commits, Commit{Hash: fields[0], ShortHash: fields[1], Date: fields[2], Author: fields[3], Subject: fields[4]})
	}
	resp.Count = len(resp.Commits)
	return resp, nil
}

func validCommitRef(ref string) bool {
	if len(ref) < 7 || len(ref) > 64 {
		return false
	}
	for _, ch := range ref {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F') {
			continue
		}
		return false
	}
	return true
}

func (m *Manager) CommitDetail(ctx context.Context, hash string) (CommitDetail, error) {
	resp := CommitDetail{OK: true, GitRepo: m.IsGitRepo()}
	if !resp.GitRepo {
		return resp, nil
	}
	hash = strings.TrimSpace(hash)
	if !validCommitRef(hash) {
		return resp, errors.New("invalid commit hash")
	}
	format := "%H%x1f%h%x1f%ad%x1f%an%x1f%s"
	meta, err := m.git(ctx, "show", "--no-patch", "--date=iso-strict", "--pretty=format:"+format, hash)
	if err != nil {
		return resp, err
	}
	fields := strings.Split(meta, "\x1f")
	if len(fields) >= 5 {
		resp.Commit = Commit{Hash: fields[0], ShortHash: fields[1], Date: fields[2], Author: fields[3], Subject: fields[4]}
	}
	if stat, err := m.git(ctx, "show", "--stat", "--format=", "--summary", hash, "--"); err == nil {
		resp.Stat = stat
	}
	if diff, err := m.git(ctx, "show", "--no-ext-diff", "--format=", "--patch", hash, "--"); err == nil {
		resp.Diff = diff
	}
	if files, err := m.git(ctx, "show", "--name-status", "--format=", hash, "--"); err == nil {
		for _, line := range strings.Split(files, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) < 2 {
				continue
			}
			resp.Files = append(resp.Files, CommitFile{Status: parts[0], Path: filepath.ToSlash(parts[len(parts)-1])})
		}
	}
	return resp, nil
}

func (m *Manager) Record(ctx context.Context) (RecordResult, error) {
	result := RecordResult{OK: true, GitRepo: m.IsGitRepo()}
	if !result.GitRepo {
		return result, nil
	}
	dirty, err := m.isDirty(ctx)
	if err != nil || !dirty {
		return result, err
	}
	if err := m.guardPrivateNotesNotTracked(ctx); err != nil {
		return result, err
	}
	if err := m.guardSafeMarkdownVersion(ctx); err != nil {
		return result, err
	}
	if err := m.stageRecallChanges(ctx); err != nil {
		return result, err
	}
	if err := m.guardSafeMarkdownStagedDiff(ctx); err != nil {
		return result, err
	}
	if _, err := m.git(ctx, "commit", "-m", commitMessage); err != nil {
		dirty, dirtyErr := m.isDirty(ctx)
		if dirtyErr != nil || dirty {
			return result, err
		}
		return result, nil
	}
	log, err := m.Log(ctx, 1)
	if err != nil {
		return result, err
	}
	result.Created = true
	if len(log.Commits) > 0 {
		result.Commit = log.Commits[0]
	}
	return result, nil
}

func isPrivateNotePlaintextOrKeyPath(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(strings.TrimPrefix(rel, "./")))
	return rel == "private-notes/notes" || strings.HasPrefix(rel, "private-notes/notes/") ||
		rel == "private-notes/.keys" || strings.HasPrefix(rel, "private-notes/.keys/")
}

func (m *Manager) guardPrivateNotesNotTracked(ctx context.Context) error {
	out, err := m.git(ctx, "ls-files", "--cached", "--others", "--exclude-standard", "--", "private-notes/notes", "private-notes/.keys")
	if err != nil {
		return err
	}
	unsafe := []string{}
	for _, rel := range strings.Split(out, "\n") {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" {
			continue
		}
		unsafe = append(unsafe, rel)
		if len(unsafe) >= 20 {
			break
		}
	}
	if len(unsafe) > 0 {
		return fmt.Errorf("refusing to record tracked or non-ignored private note plaintext or keys: %s", strings.Join(unsafe, ", "))
	}
	return nil
}

func (m *Manager) guardSafeMarkdownVersion(ctx context.Context) error {
	out, err := m.git(ctx, "ls-files", "*.md")
	if err != nil {
		return err
	}
	zero := []string{}
	for _, rel := range strings.Split(out, "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(m.repoDir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.Size() == 0 {
			zero = append(zero, rel)
			if len(zero) >= 20 {
				break
			}
		}
	}
	if len(zero) > 0 {
		return fmt.Errorf("refusing to record zero-byte tracked markdown files: %s", strings.Join(zero, ", "))
	}
	return nil
}

func (m *Manager) stageRecallChanges(ctx context.Context) error {
	if _, err := m.git(ctx, memoryGitArgs("add", "-u")...); err != nil {
		return err
	}
	out, err := m.git(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	batch := []string{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := m.git(ctx, append([]string{"add", "--"}, batch...)...)
		batch = batch[:0]
		return err
	}
	for _, rel := range strings.Split(out, "\n") {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" || strings.HasPrefix(rel, ".nexus/") {
			continue
		}
		if isPrivateNotePlaintextOrKeyPath(rel) {
			return fmt.Errorf("refusing to stage private note plaintext or key: %s", rel)
		}
		batch = append(batch, rel)
		if len(batch) >= 100 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func (m *Manager) guardSafeMarkdownStagedDiff(ctx context.Context) error {
	out, err := m.git(ctx, "diff", "--cached", "--numstat", "--", "*.md")
	if err != nil {
		return err
	}
	changedFiles, additions, deletions := 0, 0, 0
	deleteOnlyFiles := []string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		added, addErr := strconv.Atoi(fields[0])
		deleted, delErr := strconv.Atoi(fields[1])
		if addErr != nil || delErr != nil {
			continue
		}
		changedFiles++
		additions += added
		deletions += deleted
		if added == 0 && deleted > 0 {
			deleteOnlyFiles = append(deleteOnlyFiles, strings.Join(fields[2:], " "))
		}
	}
	if len(deleteOnlyFiles) >= 10 && deletions >= 100 {
		return fmt.Errorf("refusing suspicious bulk markdown delete-only version: files=%d additions=%d deletions=%d examples=%s", changedFiles, additions, deletions, strings.Join(limitStrings(deleteOnlyFiles, 20), ", "))
	}
	if changedFiles >= 20 && deletions >= 300 && deletions > additions*3 {
		return fmt.Errorf("refusing suspicious bulk markdown deletion: files=%d additions=%d deletions=%d", changedFiles, additions, deletions)
	}
	return nil
}

func limitStrings(values []string, max int) []string {
	if len(values) <= max {
		return values
	}
	limited := append([]string{}, values[:max]...)
	limited = append(limited, fmt.Sprintf("...and %d more", len(values)-max))
	return limited
}

func (m *Manager) isDirty(ctx context.Context) (bool, error) {
	out, err := m.git(ctx, memoryGitArgs("status", "--porcelain")...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (m *Manager) git(ctx context.Context, args ...string) (string, error) {
	m.gitMu.Lock()
	defer m.gitMu.Unlock()

	cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	gitArgs := []string{"-c", "safe.directory=" + m.repoDir, "-c", "user.name=NexusDock", "-c", "user.email=nexusdock@local"}
	gitArgs = append(gitArgs, args...)
	cmd := exec.CommandContext(cmdCtx, "git", gitArgs...)
	cmd.Dir = m.repoDir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, errors.New(text)
	}
	return text, nil
}
