package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetRecentConversations(t *testing.T) {
	convs, err := getRecentConversations(10)
	if err != nil {
		t.Logf("getRecentConversations returned non-fatal error: %v", err)
	}
	t.Logf("Fetched %d recent conversations", len(convs))
}

func TestDirAndAskTokens(t *testing.T) {
	p := "/data/data/com.termux/files/home/projects/very/long/path/that/exceeds/64/bytes/easily"
	cb := registerDirToken(p)
	if len(cb) > 64 {
		t.Errorf("Callback data exceeds Telegram 64-byte limit: %d (%s)", len(cb), cb)
	}
	token := strings.TrimPrefix(cb, "dir:cd:")
	resolved := resolveDirToken(token)
	if resolved != filepath.Clean(p) {
		t.Errorf("Expected %s, got %s", filepath.Clean(p), resolved)
	}

	opt := "(Recommended) Use atomic file rename for session persistence"
	askCb := registerAskOption(opt)
	if len(askCb) > 64 {
		t.Errorf("Ask callback data exceeds 64 bytes: %d", len(askCb))
	}
	askTok := strings.TrimPrefix(askCb, "ask:opt:")
	if got := resolveAskOption(askTok); got != opt {
		t.Errorf("Expected %q, got %q", opt, got)
	}
}

func TestExtractAskQuestionOptions(t *testing.T) {
	args := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"question": "Which database?",
				"options":  []interface{}{"SQLite", "PostgreSQL"},
			},
		},
	}
	opts := extractAskQuestionOptions(args)
	if len(opts) != 2 || opts[0] != "SQLite" || opts[1] != "PostgreSQL" {
		t.Errorf("Unexpected options extracted: %+v", opts)
	}
}

func TestFormatTranscriptToolCall(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]interface{}
		contains string
	}{
		{"read_file", map[string]interface{}{"Path": "/tmp/main.go"}, "main.go"},
		{"edit_file", map[string]interface{}{"Path": "/tmp/manager.go"}, "manager.go"},
		{"run_command", map[string]interface{}{"toolSummary": "Run unit tests", "CommandLine": "go test ./..."}, "Run unit tests"},
		{"invoke_subagent", map[string]interface{}{"Role": "Codebase Researcher"}, "Codebase Researcher"},
		{"search_web", map[string]interface{}{"query": "golang context"}, "golang context"},
	}
	for _, tc := range cases {
		got := formatTranscriptToolCall(tc.name, tc.args)
		if !strings.Contains(got, tc.contains) {
			t.Errorf("formatTranscriptToolCall(%s) = %q, expected to contain %q", tc.name, got, tc.contains)
		}
	}
}

func TestBuildDashboards(t *testing.T) {
	cwd, _ := os.Getwd()
	allowedDirs = []string{cwd}
	defaultDir = cwd

	dirText, dirKb := buildDirKeyboard(cwd, allowedDirs, 0)
	if !strings.Contains(dirText, "Working Directory Explorer") || dirKb == nil {
		t.Errorf("buildDirKeyboard failed: %s", dirText)
	}

	filesText, filesKb := buildFilesKeyboard(cwd, 0)
	if !strings.Contains(filesText, "Directory Files") || filesKb == nil {
		t.Errorf("buildFilesKeyboard failed: %s", filesText)
	}

	gitText, gitKb := buildGitDashboard(cwd, "status")
	if !strings.Contains(gitText, "Git") || gitKb == nil {
		t.Errorf("buildGitDashboard failed: %s", gitText)
	}

	sysText, sysKb := buildSystemHealthDashboard(cwd)
	if !strings.Contains(sysText, "System &amp; Server Health Dashboard") || sysKb == nil {
		t.Errorf("buildSystemHealthDashboard failed: %s", sysText)
	}

	st := &SessionState{
		WorkingDir:       cwd,
		ConversationID:   "test-conv-12345678",
		IsBusy:           true,
		TaskStartTime:    time.Now().Add(-15 * time.Second),
		ActiveStep:       "📖 Reading <code>main.go</code>",
		TurnsCompleted:   4,
		LastTaskDuration: "8.2s",
	}
	statusText, statusKb := buildStatusDashboard(st)
	if !strings.Contains(statusText, "Antigravity Status Dashboard") || !strings.Contains(statusText, "4 turn(s)") || statusKb == nil {
		t.Errorf("buildStatusDashboard failed: %s", statusText)
	}

	previewText, previewKb := buildFilePreviewKeyboard(filepath.Join(cwd, "main.go"), 0)
	if !strings.Contains(previewText, "package main") || previewKb == nil {
		t.Errorf("buildFilePreviewKeyboard failed: %s", previewText)
	}

	quickText, quickKb := buildQuickActionsKeyboard(cwd)
	if !strings.Contains(quickText, "Quick Engineering Workflows") || quickKb == nil {
		t.Errorf("buildQuickActionsKeyboard failed: %s", quickText)
	}

	for _, gitAct := range []string{"status", "diff", "fulldiff", "log", "branches"} {
		gt, gk := buildGitDashboard(cwd, gitAct)
		if gt == "" || gk == nil {
			t.Errorf("buildGitDashboard(%s) failed", gitAct)
		}
	}

	findText, findKb := buildFindResultsKeyboard(cwd, "FormatTelegramHTML")
	if !strings.Contains(findText, "formatter.go") || findKb == nil {
		t.Errorf("buildFindResultsKeyboard failed: %s", findText)
	}

	if got := resolveModelArg("opus"); !strings.Contains(got, "Opus") {
		t.Errorf("resolveModelArg('opus') = %q, expected Opus model", got)
	}
}

func TestSymlinkSandboxProtection(t *testing.T) {
	tmpRoot := t.TempDir()
	allowedSub := filepath.Join(tmpRoot, "allowed")
	outsideSub := filepath.Join(tmpRoot, "outside")
	_ = os.MkdirAll(allowedSub, 0755)
	_ = os.MkdirAll(outsideSub, 0755)

	secretFile := filepath.Join(outsideSub, "secret.txt")
	_ = os.WriteFile(secretFile, []byte("top-secret"), 0644)

	symlinkPath := filepath.Join(allowedSub, "escape_link.txt")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Skipf("Symlink not supported: %v", err)
	}

	if isSubDir(allowedSub, symlinkPath) {
		t.Errorf("SECURITY BUG: Symlink pointing outside sandbox was allowed by isSubDir!")
	}
}
