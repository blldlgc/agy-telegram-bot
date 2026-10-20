package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ConversationSummary struct {
	ID           string
	Title        string
	LastModified string
}

// findSqliteBinary locates sqlite3 binary across standard and Android paths
func findSqliteBinary() string {
	candidates := []string{
		"sqlite3",
		"/data/data/com.termux/files/usr/bin/sqlite3",
		"/system/bin/sqlite3",
		"/system/xbin/sqlite3",
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "sqlite3"
}

// ModelChoice represents an AI model option
type ModelChoice struct {
	ID   string
	Name string
}

var AvailableModels = []ModelChoice{
	{ID: "Gemini 3.8 Flash (Medium)", Name: "⚡ Gemini 3.8 Flash (Medium)"},
	{ID: "Gemini 3.8 Flash (High)", Name: "⚡ Gemini 3.8 Flash (High)"},
	{ID: "Gemini 4 Argon (Medium)", Name: "🧠 Gemini 4 Argon (Medium)"},
	{ID: "Gemini 4 Argon (High)", Name: "🚀 Gemini 4 Argon (High)"},
	{ID: "Claude Sonnet 5.5 (Medium)", Name: "🎨 Claude Sonnet 5.5 (Medium)"},
	{ID: "Claude Opus 5.5 (High)", Name: "🏆 Claude Opus 5.5 (High)"},
}

// buildModelKeyboard constructs inline keyboard for model selection
func buildModelKeyboard() (string, *InlineKeyboardMarkup) {
	current := getActiveModel()
	var sb strings.Builder
	sb.WriteString("🧠 <b>Active AI Model Selection:</b>\n\n")
	sb.WriteString(fmt.Sprintf("Current Model: <code>%s</code>\n\n", current))
	sb.WriteString("Select a model to switch:")

	var rows [][]InlineKeyboardButton
	for _, m := range AvailableModels {
		prefix := "⚪ "
		if strings.EqualFold(m.ID, current) || strings.EqualFold(m.Name, current) {
			prefix = "🟢 "
		}
		rows = append(rows, []InlineKeyboardButton{
			{
				Text:         prefix + m.Name,
				CallbackData: "model:" + m.ID,
			},
		})
	}
	return sb.String(), &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// setActiveModel updates ~/.gemini/antigravity-cli/settings.json
func setActiveModel(modelName string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "settings.json")

	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	if settings == nil {
		settings = make(map[string]interface{})
	}

	settings["model"] = modelName
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, updated, 0644)
}

// getDefaultReplyKeyboard returns persistent bottom keyboard
func getDefaultReplyKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]KeyboardButton{
			{
				{Text: "📊 Status"},
				{Text: "📁 Dir"},
				{Text: "🧠 Model"},
			},
			{
				{Text: "🔄 Continue"},
				{Text: "💬 Resume"},
				{Text: "✨ New"},
			},
			{
				{Text: "🌿 Git"},
				{Text: "🛑 Cancel"},
			},
		},
		ResizeKeyboard:        true,
		IsPersistent:          true,
		InputFieldPlaceholder: "Ask Antigravity or tap a command...",
	}
}

// buildActionKeyboard returns quick action inline keyboard for completed turns
func buildActionKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🔄 Continue", CallbackData: "action:continue"},
				{Text: "📁 Dir", CallbackData: "dir:roots"},
				{Text: "✨ New Session", CallbackData: "resume:new"},
			},
		},
	}
}

// getActiveModel reads the configured model from settings.json or environment
func getActiveModel() string {
	homeDir, err := os.UserHomeDir()
	if err == nil {
		settingsPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "settings.json")
		data, err := os.ReadFile(settingsPath)
		if err == nil {
			var settings struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(data, &settings); err == nil && settings.Model != "" {
				return settings.Model
			}
		}
	}

	envModel := os.Getenv("ANTIGRAVITY_MODEL")
	if envModel != "" {
		return envModel
	}

	return "Gemini 3.8 Flash (Medium)"
}

// shortenHomeDir converts /data/data/com.termux/files/home/... to ~/... for mobile display
func shortenHomeDir(path string) string {
	homeDir, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, homeDir) {
		return "~" + strings.TrimPrefix(path, homeDir)
	}
	return path
}

// getRecentConversations fetches recent conversations from conversation_summaries.db or conversations directory
func getRecentConversations(limit int) ([]ConversationSummary, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "conversation_summaries.db")

	sqliteBin := findSqliteBinary()
	query := fmt.Sprintf("SELECT conversation_id, title, last_modified_time FROM conversation_summaries ORDER BY last_modified_time DESC LIMIT %d;", limit)
	cmd := exec.Command(sqliteBin, "-separator", "|||", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("getRecentConversations: sqlite failed (%s on %s): %v. Trying filesystem fallback...", sqliteBin, dbPath, err)
		return getConversationsFromFS(homeDir, limit)
	}

	var results []ConversationSummary
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|||")
		if len(parts) >= 2 {
			id := strings.TrimSpace(parts[0])
			title := strings.TrimSpace(parts[1])
			title = strings.ReplaceAll(title, "<BOT_REPLY>", "")
			title = strings.ReplaceAll(title, "</BOT_REPLY>", "")
			title = strings.TrimSpace(title)
			if title == "" {
				title = "Untitled Conversation"
			}
			timeStr := ""
			if len(parts) >= 3 {
				tRaw := strings.TrimSpace(parts[2])
				if parsedT, err := time.Parse(time.RFC3339Nano, tRaw); err == nil {
					timeStr = parsedT.Format("02.01 15:04")
				} else if len(tRaw) >= 16 {
					timeStr = tRaw[:16]
				}
			}
			results = append(results, ConversationSummary{
				ID:           id,
				Title:        title,
				LastModified: timeStr,
			})
		}
	}

	if len(results) == 0 {
		return getConversationsFromFS(homeDir, limit)
	}

	return results, nil
}

// getConversationsFromFS lists recent conversations from ~/.gemini/antigravity-cli/conversations/
func getConversationsFromFS(homeDir string, limit int) ([]ConversationSummary, error) {
	convDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "conversations")
	entries, err := os.ReadDir(convDir)
	if err != nil {
		return nil, err
	}

	type fileItem struct {
		id      string
		modTime time.Time
	}
	var items []fileItem

	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".db") && !strings.Contains(name, "-wal") && !strings.Contains(name, "-shm") {
			id := strings.TrimSuffix(name, ".db")
			info, err := e.Info()
			if err == nil {
				items = append(items, fileItem{id: id, modTime: info.ModTime()})
			}
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].modTime.After(items[j].modTime)
	})

	var results []ConversationSummary
	for i, it := range items {
		if i >= limit {
			break
		}
		results = append(results, ConversationSummary{
			ID:           it.id,
			Title:        "Session " + it.id[:8],
			LastModified: it.modTime.Format("02.01 15:04"),
		})
	}

	return results, nil
}

// cleanUserPrompt strips <USER_REQUEST> tags and system rules from transcript content
func cleanUserPrompt(raw string) string {
	if idx := strings.Index(raw, "<USER_REQUEST>"); idx != -1 {
		raw = raw[idx+len("<USER_REQUEST>"):]
	}
	if idx := strings.Index(raw, "</USER_REQUEST>"); idx != -1 {
		raw = raw[:idx]
	}
	if idx := strings.Index(raw, "(System rule:"); idx != -1 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

// cleanBotReply extracts text inside <BOT_REPLY> tags if present
func cleanBotReply(raw string) string {
	if idx := strings.LastIndex(raw, "<BOT_REPLY>"); idx != -1 {
		raw = raw[idx+len("<BOT_REPLY>"):]
		if endIdx := strings.Index(raw, "</BOT_REPLY>"); endIdx != -1 {
			raw = raw[:endIdx]
		}
	}
	return strings.TrimSpace(raw)
}

// getConversationPreview reads transcript_full.jsonl (or transcript.jsonl) and returns the last user message and last bot reply
func getConversationPreview(convID string) (string, string) {
	homeDir, err := os.UserHomeDir()
	if err != nil || convID == "" {
		return "", ""
	}
	logsDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", convID, ".system_generated", "logs")
	transcriptPath := filepath.Join(logsDir, "transcript_full.jsonl")
	f, err := os.Open(transcriptPath)
	if err != nil {
		transcriptPath = filepath.Join(logsDir, "transcript.jsonl")
		f, err = os.Open(transcriptPath)
		if err != nil {
			return "", ""
		}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 512*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var lastUser, lastBot string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var step struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			continue
		}
		if step.Type == "USER_INPUT" && step.Content != "" {
			cleaned := cleanUserPrompt(step.Content)
			if cleaned != "" {
				lastUser = cleaned
			}
		} else if step.Type == "PLANNER_RESPONSE" && strings.TrimSpace(step.Content) != "" {
			cleaned := cleanBotReply(step.Content)
			if cleaned != "" {
				lastBot = cleaned
			}
		}
	}
	return lastUser, lastBot
}

// formatResumeConfirmation builds the detailed message shown when switching to a conversation
func formatResumeConfirmation(convID string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ <b>Conversation Resumed:</b>\n🆔 <code>%s</code>\n", convID))

	lastUser, lastBot := getConversationPreview(convID)
	if lastUser != "" {
		userPreview := lastUser
		runes := []rune(userPreview)
		if len(runes) > 1000 {
			userPreview = string(runes[:997]) + "..."
		}
		sb.WriteString(fmt.Sprintf("\n👤 <b>Your Last Message:</b>\n<blockquote>%s</blockquote>\n", escapeTelegram(userPreview)))
	}
	if lastBot != "" {
		botPreview := lastBot
		runes := []rune(botPreview)
		if len(runes) > 2500 {
			botPreview = string(runes[:2497]) + "..."
		}
		sb.WriteString(fmt.Sprintf("\n🤖 <b>Last Reply:</b>\n<blockquote>%s</blockquote>\n", escapeTelegram(botPreview)))
	}

	sb.WriteString("\n<i>Your subsequent messages will continue this conversation.</i>")
	return sb.String()
}

// buildResumeKeyboard constructs the text and inline buttons for /resume
func buildResumeKeyboard() (string, *InlineKeyboardMarkup, error) {
	convs, err := getRecentConversations(6)
	if err != nil {
		log.Printf("buildResumeKeyboard error: %v", err)
	}
	if len(convs) == 0 {
		text := "💬 <b>Saved Sessions:</b>\n\nNo saved session history found."
		kb := &InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{
				{
					{Text: "🔄 Start New Session", CallbackData: "resume:new"},
				},
			},
		}
		return text, kb, nil
	}

	var sb strings.Builder
	sb.WriteString("💬 <b>Saved Sessions (Select to resume):</b>\n\n")

	var rows [][]InlineKeyboardButton
	for i, c := range convs {
		num := i + 1
		sb.WriteString(fmt.Sprintf("<b>%d.</b> %s\n", num, escapeTelegram(c.Title)))
		if c.LastModified != "" {
			sb.WriteString(fmt.Sprintf("   🕒 <i>%s</i> | <code>%s</code>\n", c.LastModified, c.ID[:8]))
		} else {
			sb.WriteString(fmt.Sprintf("   🆔 <code>%s</code>\n", c.ID[:8]))
		}
		lastUser, _ := getConversationPreview(c.ID)
		if lastUser != "" {
			snippet := strings.ReplaceAll(lastUser, "\n", " ")
			runes := []rune(snippet)
			if len(runes) > 180 {
				snippet = string(runes[:177]) + "..."
			}
			sb.WriteString(fmt.Sprintf("   💬 <i>\"%s\"</i>\n", escapeTelegram(snippet)))
		}
		sb.WriteString("\n")

		buttonTitle := fmt.Sprintf("%d. %s", num, c.Title)
		if len([]rune(buttonTitle)) > 26 {
			buttonTitle = string([]rune(buttonTitle)[:24]) + ".."
		}

		rows = append(rows, []InlineKeyboardButton{
			{
				Text:         buttonTitle,
				CallbackData: fmt.Sprintf("resume:%s", c.ID),
			},
		})
	}

	rows = append(rows, []InlineKeyboardButton{
		{Text: "🔄 Start New Session", CallbackData: "resume:new"},
	})

	return sb.String(), &InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

// buildDirKeyboard constructs interactive directory selection
func buildDirKeyboard(currentDir string, allowed []string) (string, *InlineKeyboardMarkup) {
	cleanCurrent := filepath.Clean(currentDir)
	displayDir := shortenHomeDir(cleanCurrent)

	var sb strings.Builder
	sb.WriteString("📂 <b>Working Directory Selection:</b>\n\n")
	sb.WriteString(fmt.Sprintf("Current: <code>%s</code>\n\n", displayDir))
	sb.WriteString("Select a folder to navigate into:")

	var rows [][]InlineKeyboardButton

	parentDir := filepath.Dir(cleanCurrent)
	parentAllowed := false
	for _, ad := range allowed {
		if isSubDir(ad, parentDir) {
			parentAllowed = true
			break
		}
	}
	if parentAllowed && parentDir != cleanCurrent {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "📁 .. (Parent Directory)", CallbackData: fmt.Sprintf("dir:cd:%s", parentDir)},
		})
	}

	entries, err := os.ReadDir(cleanCurrent)
	if err == nil {
		var subdirs []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				subdirs = append(subdirs, e.Name())
			}
		}
		sort.Strings(subdirs)

		var currentRow []InlineKeyboardButton
		for i, dirName := range subdirs {
			if i >= 10 {
				break
			}
			fullSub := filepath.Join(cleanCurrent, dirName)
			btnText := "📁 " + dirName
			if len([]rune(btnText)) > 18 {
				btnText = string([]rune(btnText)[:16]) + ".."
			}
			currentRow = append(currentRow, InlineKeyboardButton{
				Text:         btnText,
				CallbackData: fmt.Sprintf("dir:cd:%s", fullSub),
			})
			if len(currentRow) == 2 {
				rows = append(rows, currentRow)
				currentRow = nil
			}
		}
		if len(currentRow) > 0 {
			rows = append(rows, currentRow)
		}
	}

	rows = append(rows, []InlineKeyboardButton{
		{Text: "🌐 Root Folders", CallbackData: "dir:roots"},
		{Text: "🔄 Refresh", CallbackData: fmt.Sprintf("dir:cd:%s", cleanCurrent)},
	})

	return sb.String(), &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// buildRootsKeyboard shows the top-level allowed directories
func buildRootsKeyboard(allowed []string) (string, *InlineKeyboardMarkup) {
	text := "🌐 <b>Allowed Root Folders (ALLOWED_DIRS):</b>\n\nSelect a root working directory:"
	var rows [][]InlineKeyboardButton

	for _, ad := range allowed {
		cleanAd := filepath.Clean(ad)
		btnText := "🏠 " + shortenHomeDir(cleanAd)
		rows = append(rows, []InlineKeyboardButton{
			{
				Text:         btnText,
				CallbackData: fmt.Sprintf("dir:cd:%s", cleanAd),
			},
		})
	}

	return text, &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// buildGitDashboard runs fast local git commands in dir and returns an interactive status card
func buildGitDashboard(dir string, action string) (string, *InlineKeyboardMarkup) {
	cleanDir := filepath.Clean(dir)
	displayDir := shortenHomeDir(cleanDir)

	kb := &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🔄 Status", CallbackData: "git:status"},
				{Text: "📊 Diff Stat", CallbackData: "git:diff"},
			},
			{
				{Text: "📜 Recent Commits", CallbackData: "git:log"},
				{Text: "📥 Git Pull", CallbackData: "git:pull"},
			},
		},
	}

	checkCmd := exec.Command("git", "-C", cleanDir, "rev-parse", "--is-inside-work-tree")
	if out, err := checkCmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "true") {
		return fmt.Sprintf("🌿 <b>Git Dashboard</b>\n📂 <code>%s</code>\n\nℹ️ <i>This directory is not a Git repository.</i>", escapeTelegram(displayDir)), kb
	}

	branchOut, _ := exec.Command("git", "-C", cleanDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		branch = "detached/none"
	}

	lastCommitOut, _ := exec.Command("git", "-C", cleanDir, "log", "-1", "--pretty=format:%h — %s (%cr)").Output()
	lastCommit := strings.TrimSpace(string(lastCommitOut))
	if lastCommit == "" {
		lastCommit = "No commits yet"
	}

	var sb strings.Builder
	sb.WriteString("🌿 <b>Git Repository Dashboard</b>\n")
	sb.WriteString(fmt.Sprintf("📂 <b>Dir:</b> <code>%s</code>\n", escapeTelegram(displayDir)))
	sb.WriteString(fmt.Sprintf("🔀 <b>Branch:</b> <code>%s</code>\n", escapeTelegram(branch)))
	sb.WriteString(fmt.Sprintf("📝 <b>Latest:</b> <code>%s</code>\n\n", escapeTelegram(lastCommit)))

	switch action {
	case "diff":
		diffOut, _ := exec.Command("git", "-C", cleanDir, "diff", "--stat").CombinedOutput()
		diffStr := strings.TrimSpace(string(diffOut))
		if diffStr == "" {
			diffStr = "No unstaged line changes."
		}
		if len([]rune(diffStr)) > 2500 {
			diffStr = string([]rune(diffStr)[:2500]) + "\n..."
		}
		sb.WriteString("📊 <b>Diff Summary (Unstaged):</b>\n")
		sb.WriteString(fmt.Sprintf("<pre>%s</pre>", escapeTelegram(diffStr)))

	case "log":
		logOut, _ := exec.Command("git", "-C", cleanDir, "log", "-n", "6", "--pretty=format:• %h — %s (%cr)").CombinedOutput()
		logStr := strings.TrimSpace(string(logOut))
		if logStr == "" {
			logStr = "No commit history found."
		}
		sb.WriteString("📜 <b>Recent Commits:</b>\n")
		sb.WriteString(fmt.Sprintf("<pre>%s</pre>", escapeTelegram(logStr)))

	case "pull":
		pullOut, err := exec.Command("git", "-C", cleanDir, "pull", "--ff-only").CombinedOutput()
		pullStr := strings.TrimSpace(string(pullOut))
		if err != nil {
			sb.WriteString(fmt.Sprintf("❌ <b>Git Pull Failed:</b>\n<pre>%s</pre>", escapeTelegram(pullStr)))
		} else {
			sb.WriteString(fmt.Sprintf("📥 <b>Git Pull Output:</b>\n<pre>%s</pre>", escapeTelegram(pullStr)))
		}

	default: // "status"
		statusOut, _ := exec.Command("git", "-C", cleanDir, "status", "-s").CombinedOutput()
		statusStr := strings.TrimSpace(string(statusOut))
		if statusStr == "" {
			sb.WriteString("✨ <b>Working Tree:</b> Clean (no modified or untracked files)")
		} else {
			sb.WriteString("📋 <b>Working Tree Changes:</b>\n")
			sb.WriteString(fmt.Sprintf("<pre>%s</pre>", escapeTelegram(statusStr)))
		}
	}

	return sb.String(), kb
}

// cleanArgStr extracts a clean string value from transcript tool_calls args
func cleanArgStr(args map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k]; ok && v != nil {
			s := fmt.Sprintf("%v", v)
			s = strings.TrimSpace(strings.Trim(s, `"`))
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// formatTranscriptToolCall produces a human-readable HTML summary of a tool call
func formatTranscriptToolCall(name string, args map[string]interface{}) string {
	switch name {
	case "read_file", "view_file":
		p := cleanArgStr(args, "Path", "AbsolutePath", "TargetFile", "file_path")
		if p != "" {
			return fmt.Sprintf("📖 Reading <code>%s</code>", escapeTelegram(filepath.Base(p)))
		}
		return "📖 Reading file"

	case "edit_file", "replace_file_content", "multi_replace_file_content":
		p := cleanArgStr(args, "Path", "TargetFile", "file_path")
		if p != "" {
			return fmt.Sprintf("✏️ Editing <code>%s</code>", escapeTelegram(filepath.Base(p)))
		}
		return "✏️ Editing file"

	case "write_file", "create_file":
		p := cleanArgStr(args, "Path", "TargetFile", "file_path")
		if p != "" {
			return fmt.Sprintf("📝 Writing <code>%s</code>", escapeTelegram(filepath.Base(p)))
		}
		return "📝 Writing file"

	case "run_command":
		summary := cleanArgStr(args, "toolSummary", "toolAction")
		cmdLine := cleanArgStr(args, "CommandLine", "command_line")
		if summary != "" && cmdLine != "" {
			return fmt.Sprintf("💻 <i>%s</i> (<code>%s</code>)", escapeTelegram(truncateStr(summary, 35)), escapeTelegram(truncateStr(cmdLine, 35)))
		} else if summary != "" {
			return fmt.Sprintf("💻 <i>%s</i>", escapeTelegram(truncateStr(summary, 45)))
		} else if cmdLine != "" {
			return fmt.Sprintf("💻 Running <code>%s</code>", escapeTelegram(truncateStr(cmdLine, 40)))
		}
		return "💻 Running command"

	case "search_web":
		q := cleanArgStr(args, "query")
		if q != "" {
			return fmt.Sprintf("🌐 Searching web: <i>%s</i>", escapeTelegram(truncateStr(q, 35)))
		}
		return "🌐 Searching web"

	case "manage_task":
		return "⏳ Checking background task"

	case "grep_search", "find_by_name", "find_file", "search_directory":
		q := cleanArgStr(args, "Query", "Pattern", "query")
		if q != "" {
			return fmt.Sprintf("🔍 Searching: <code>%s</code>", escapeTelegram(truncateStr(q, 35)))
		}
		return "🔍 Searching files"

	default:
		return fmt.Sprintf("🔧 <code>%s</code>", escapeTelegram(name))
	}
}

// extractThinkingSnippet returns a concise 1-line summary of the model's current thought
func extractThinkingSnippet(thinking string) string {
	thinking = strings.TrimSpace(thinking)
	if thinking == "" {
		return ""
	}
	lines := strings.Split(thinking, "\n")
	var lastNonEmpty string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			lastNonEmpty = trimmed
		}
	}
	if lastNonEmpty == "" {
		return ""
	}
	return truncateStr(lastNonEmpty, 90)
}

// getTranscriptPathForConv returns the path to transcript.jsonl for a conversation ID
func getTranscriptPathForConv(convID string) string {
	if convID == "" {
		return ""
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", convID, ".system_generated", "logs", "transcript.jsonl")
}

// findNewestTranscriptPath finds the most recently modified transcript.jsonl in brain/
func findNewestTranscriptPath(since time.Time) (string, string) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	brainDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain")
	entries, err := os.ReadDir(brainDir)
	if err != nil {
		return "", ""
	}
	var bestConvID, bestPath string
	var bestTime time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(brainDir, e.Name(), ".system_generated", "logs", "transcript.jsonl")
		info, err := os.Stat(p)
		if err == nil && info.ModTime().After(since.Add(-2*time.Second)) {
			if bestPath == "" || info.ModTime().After(bestTime) {
				bestTime = info.ModTime()
				bestConvID = e.Name()
				bestPath = p
			}
		}
	}
	return bestConvID, bestPath
}


