package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	dirTokenMu  sync.RWMutex
	dirTokenMap = make(map[string]string)

	askTokenMu  sync.RWMutex
	askTokenMap = make(map[string]string)

	sessionFileMu sync.Mutex
	botStartTime  = time.Now()
)

func registerAskOption(optionText string) string {
	clean := strings.TrimSpace(optionText)
	h := sha1.Sum([]byte(clean))
	token := fmt.Sprintf("%x", h[:6])
	askTokenMu.Lock()
	askTokenMap[token] = clean
	askTokenMu.Unlock()
	return "ask:opt:" + token
}

func resolveAskOption(token string) string {
	askTokenMu.RLock()
	val, ok := askTokenMap[token]
	askTokenMu.RUnlock()
	if ok {
		return val
	}
	return ""
}

// extractAskQuestionOptions extracts multiple-choice options from an ask_question tool call
func extractAskQuestionOptions(args map[string]interface{}) []string {
	rawQuestions, ok := args["questions"]
	if !ok || rawQuestions == nil {
		return nil
	}
	qSlice, ok := rawQuestions.([]interface{})
	if !ok {
		return nil
	}
	var options []string
	for _, qItem := range qSlice {
		qMap, ok := qItem.(map[string]interface{})
		if !ok {
			continue
		}
		if rawOpts, ok := qMap["options"].([]interface{}); ok {
			for _, o := range rawOpts {
				s := strings.TrimSpace(fmt.Sprintf("%v", o))
				if s != "" {
					options = append(options, s)
				}
			}
		}
	}
	return options
}


// PersistedSession represents the on-disk state for a user across restarts
type PersistedSession struct {
	WorkingDir     string    `json:"working_dir"`
	ConversationID string    `json:"conversation_id"`
	Continue       bool      `json:"continue"`
	LastActive     time.Time `json:"last_active"`
}

func getSessionsFilePath() string {
	exePath, err := os.Executable()
	var baseDir string
	if err == nil {
		baseDir = filepath.Dir(exePath)
	} else {
		baseDir, _ = os.Getwd()
	}
	return filepath.Join(baseDir, ".sessions.json")
}

// saveSessions persists all user sessions atomically to .sessions.json
func saveSessions() {
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()

	mu.RLock()
	dump := make(map[string]PersistedSession, len(sessions))
	for uid, st := range sessions {
		dump[strconv.FormatInt(uid, 10)] = PersistedSession{
			WorkingDir:     st.WorkingDir,
			ConversationID: st.ConversationID,
			Continue:       st.Continue,
			LastActive:     st.LastActive,
		}
	}
	mu.RUnlock()

	data, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		log.Printf("saveSessions marshal error: %v", err)
		return
	}

	filePath := getSessionsFilePath()
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		log.Printf("saveSessions write error: %v", err)
		return
	}
	_ = os.Rename(tmpPath, filePath)
}

// loadSessions restores user sessions from .sessions.json on startup
func loadSessions() {
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()

	filePath := getSessionsFilePath()
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	var dump map[string]PersistedSession
	if err := json.Unmarshal(data, &dump); err != nil {
		log.Printf("loadSessions unmarshal error: %v", err)
		return
	}

	mu.Lock()
	defer mu.Unlock()
	for uidStr, ps := range dump {
		uid, err := strconv.ParseInt(uidStr, 10, 64)
		if err != nil {
			continue
		}
		workDir := filepath.Clean(ps.WorkingDir)
		validDir := false
		if info, err := os.Stat(workDir); err == nil && info.IsDir() {
			for _, ad := range allowedDirs {
				if isSubDir(ad, workDir) {
					validDir = true
					break
				}
			}
		}
		if !validDir {
			workDir = defaultDir
		}

		convID := ps.ConversationID
		cont := ps.Continue
		if !ps.LastActive.IsZero() && time.Since(ps.LastActive) > 6*time.Hour {
			convID = ""
			cont = false
		}

		sessions[uid] = &SessionState{
			WorkingDir:     workDir,
			ConversationID: convID,
			Continue:       cont,
			IsBusy:         false,
			LastActive:     ps.LastActive,
		}
	}
	log.Printf("Restored %d user session(s) from disk", len(sessions))
}

func makePathToken(fullPath string) string {
	clean := filepath.Clean(fullPath)
	h := sha1.Sum([]byte(clean))
	token := fmt.Sprintf("%x", h[:6])
	dirTokenMu.Lock()
	dirTokenMap[token] = clean
	dirTokenMu.Unlock()
	return token
}

func registerDirToken(fullPath string) string {
	return "dir:cd:" + makePathToken(fullPath)
}

func resolveDirToken(tokenOrPath string) string {
	dirTokenMu.RLock()
	p, ok := dirTokenMap[tokenOrPath]
	dirTokenMu.RUnlock()
	if ok {
		return p
	}
	return tokenOrPath
}

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
	sb.WriteString(fmt.Sprintf("Current Model: <code>%s</code>\n\n", escapeTelegram(current)))
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
				{Text: "🖥️ Server"},
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
				{Text: "📁 Dir", CallbackData: "dir:open"},
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
		short := "~" + strings.TrimPrefix(path, homeDir)
		if short == "~" {
			return "~/"
		}
		return short
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
					timeStr = parsedT.Local().Format("02.01 15:04")
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
		shortID := it.id
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		results = append(results, ConversationSummary{
			ID:           it.id,
			Title:        "Session " + shortID,
			LastModified: it.modTime.Local().Format("02.01 15:04"),
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
	sb.WriteString(fmt.Sprintf("✅ <b>Conversation Resumed:</b>\n🆔 <code>%s</code>\n", escapeTelegram(convID)))

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
		sb.WriteString(fmt.Sprintf("\n🤖 <b>Last Reply:</b>\n<blockquote expandable>%s</blockquote>\n", escapeTelegram(botPreview)))
	}

	sb.WriteString("\n<i>Your subsequent messages will continue this conversation.</i>")
	return sb.String()
}

// buildResumeKeyboard constructs the paginated text and inline buttons for /resume
func buildResumeKeyboard(activeConvID string, page ...int) (string, *InlineKeyboardMarkup, error) {
	curPage := 0
	if len(page) > 0 && page[0] >= 0 {
		curPage = page[0]
	}
	const perPage = 5
	convs, err := getRecentConversations(30)
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

	totalPages := (len(convs) + perPage - 1) / perPage
	if curPage >= totalPages {
		curPage = totalPages - 1
	}
	startIdx := curPage * perPage
	endIdx := startIdx + perPage
	if endIdx > len(convs) {
		endIdx = len(convs)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("💬 <b>Saved Sessions</b> <i>(Page %d/%d)</i>:\n\n", curPage+1, totalPages))

	var rows [][]InlineKeyboardButton
	for i := startIdx; i < endIdx; i++ {
		c := convs[i]
		num := i + 1
		isActive := activeConvID != "" && c.ID == activeConvID
		activeBadge := ""
		btnPrefix := ""
		if isActive {
			activeBadge = " 🟢 <b>[Active]</b>"
			btnPrefix = "🟢 "
		}

		sb.WriteString(fmt.Sprintf("<b>%d.</b> %s%s\n", num, escapeTelegram(c.Title), activeBadge))
		shortID := c.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		if c.LastModified != "" {
			sb.WriteString(fmt.Sprintf("   🕒 <i>%s</i> | <code>%s</code>\n", c.LastModified, shortID))
		} else {
			sb.WriteString(fmt.Sprintf("   🆔 <code>%s</code>\n", shortID))
		}
		lastUser, _ := getConversationPreview(c.ID)
		if lastUser != "" {
			snippet := strings.ReplaceAll(lastUser, "\n", " ")
			runes := []rune(snippet)
			if len(runes) > 160 {
				snippet = string(runes[:157]) + "..."
			}
			sb.WriteString(fmt.Sprintf("   💬 <i>\"%s\"</i>\n", escapeTelegram(snippet)))
		}
		sb.WriteString("\n")

		buttonTitle := fmt.Sprintf("%s%d. %s", btnPrefix, num, c.Title)
		if len([]rune(buttonTitle)) > 28 {
			buttonTitle = string([]rune(buttonTitle)[:26]) + ".."
		}

		rows = append(rows, []InlineKeyboardButton{
			{
				Text:         buttonTitle,
				CallbackData: fmt.Sprintf("resume:%s", c.ID),
			},
		})
	}

	if totalPages > 1 {
		var navRow []InlineKeyboardButton
		if curPage > 0 {
			navRow = append(navRow, InlineKeyboardButton{
				Text:         "⬅️ Newer",
				CallbackData: fmt.Sprintf("resume:page:%d", curPage-1),
			})
		}
		if curPage+1 < totalPages {
			navRow = append(navRow, InlineKeyboardButton{
				Text:         "Older ➡️",
				CallbackData: fmt.Sprintf("resume:page:%d", curPage+1),
			})
		}
		if len(navRow) > 0 {
			rows = append(rows, navRow)
		}
	}

	rows = append(rows, []InlineKeyboardButton{
		{Text: "✨ Start New Session", CallbackData: "resume:new"},
	})

	return sb.String(), &InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

// buildDirKeyboard constructs interactive directory selection with pagination and file browser access
func buildDirKeyboard(currentDir string, allowed []string, page ...int) (string, *InlineKeyboardMarkup) {
	curPage := 0
	if len(page) > 0 && page[0] >= 0 {
		curPage = page[0]
	}

	cleanCurrent := filepath.Clean(currentDir)
	displayDir := shortenHomeDir(cleanCurrent)
	curToken := makePathToken(cleanCurrent)

	entries, err := os.ReadDir(cleanCurrent)
	var subdirs []string
	fileCount := 0
	if err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				subdirs = append(subdirs, e.Name())
			} else {
				fileCount++
			}
		}
		sort.Strings(subdirs)
	}

	const perPage = 8
	totalPages := (len(subdirs) + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if curPage >= totalPages {
		curPage = totalPages - 1
	}

	var sb strings.Builder
	sb.WriteString("📂 <b>Working Directory Explorer:</b>\n\n")
	sb.WriteString(fmt.Sprintf("📍 <b>Current:</b> <code>%s</code>\n", escapeTelegram(displayDir)))
	sb.WriteString(fmt.Sprintf("📊 <i>%d folder(s), %d file(s)</i>\n\n", len(subdirs), fileCount))
	sb.WriteString("Select a folder to navigate or browse files:")

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
			{Text: "⬆️ .. (Parent Directory)", CallbackData: registerDirToken(parentDir)},
		})
	}

	startIdx := curPage * perPage
	endIdx := startIdx + perPage
	if endIdx > len(subdirs) {
		endIdx = len(subdirs)
	}

	var currentRow []InlineKeyboardButton
	for i := startIdx; i < endIdx; i++ {
		dirName := subdirs[i]
		fullSub := filepath.Join(cleanCurrent, dirName)
		btnText := "📁 " + dirName
		if len([]rune(btnText)) > 20 {
			btnText = string([]rune(btnText)[:18]) + ".."
		}
		currentRow = append(currentRow, InlineKeyboardButton{
			Text:         btnText,
			CallbackData: registerDirToken(fullSub),
		})
		if len(currentRow) == 2 {
			rows = append(rows, currentRow)
			currentRow = nil
		}
	}
	if len(currentRow) > 0 {
		rows = append(rows, currentRow)
	}

	// Pagination row if > perPage folders
	if totalPages > 1 {
		var navRow []InlineKeyboardButton
		if curPage > 0 {
			navRow = append(navRow, InlineKeyboardButton{
				Text:         "⬅️ Prev",
				CallbackData: fmt.Sprintf("dir:page:%s:%d", curToken, curPage-1),
			})
		}
		navRow = append(navRow, InlineKeyboardButton{
			Text:         fmt.Sprintf("📖 %d/%d", curPage+1, totalPages),
			CallbackData: fmt.Sprintf("dir:page:%s:%d", curToken, curPage),
		})
		if curPage+1 < totalPages {
			navRow = append(navRow, InlineKeyboardButton{
				Text:         "Next ➡️",
				CallbackData: fmt.Sprintf("dir:page:%s:%d", curToken, curPage+1),
			})
		}
		rows = append(rows, navRow)
	}

	// Quick file browser & git buttons
	rows = append(rows, []InlineKeyboardButton{
		{Text: fmt.Sprintf("📄 Files (%d)", fileCount), CallbackData: fmt.Sprintf("file:list:%s:0", curToken)},
		{Text: "🌿 Git Status", CallbackData: "git:status"},
	})

	rows = append(rows, []InlineKeyboardButton{
		{Text: "🌐 Root Folders", CallbackData: "dir:roots"},
		{Text: "🔄 Refresh", CallbackData: fmt.Sprintf("dir:page:%s:%d", curToken, curPage)},
	})

	return sb.String(), &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// buildFilesKeyboard lists files in currentDir with 1-tap download buttons
func buildFilesKeyboard(currentDir string, page int) (string, *InlineKeyboardMarkup) {
	cleanCurrent := filepath.Clean(currentDir)
	displayDir := shortenHomeDir(cleanCurrent)
	curToken := makePathToken(cleanCurrent)

	entries, err := os.ReadDir(cleanCurrent)
	type fileEntry struct {
		name string
		size int64
	}
	var files []fileEntry
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			info, err := e.Info()
			var sz int64
			if err == nil {
				sz = info.Size()
			}
			files = append(files, fileEntry{name: e.Name(), size: sz})
		}
		sort.Slice(files, func(i, j int) bool {
			return files[i].name < files[j].name
		})
	}

	const perPage = 8
	totalPages := (len(files) + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	var sb strings.Builder
	sb.WriteString("📄 <b>Directory Files (Tap to Download):</b>\n\n")
	sb.WriteString(fmt.Sprintf("📍 <b>Dir:</b> <code>%s</code>\n", escapeTelegram(displayDir)))

	var rows [][]InlineKeyboardButton
	if len(files) == 0 {
		sb.WriteString("\n<i>No visible files in this directory.</i>")
	} else {
		sb.WriteString(fmt.Sprintf("📦 <i>Showing page %d/%d (%d total files)</i>\n\n", page+1, totalPages, len(files)))
		startIdx := page * perPage
		endIdx := startIdx + perPage
		if endIdx > len(files) {
			endIdx = len(files)
		}
		for i := startIdx; i < endIdx; i++ {
			f := files[i]
			fullPath := filepath.Join(cleanCurrent, f.name)
			fToken := makePathToken(fullPath)
			sizeStr := formatByteSize(f.size)
			btnText := fmt.Sprintf("📥 %s (%s)", f.name, sizeStr)
			if len([]rune(btnText)) > 34 {
				btnText = fmt.Sprintf("📥 %s (%s)", truncateStr(f.name, 20), sizeStr)
			}
			rows = append(rows, []InlineKeyboardButton{
				{
					Text:         btnText,
					CallbackData: "file:dl:" + fToken,
				},
			})
		}
	}

	if totalPages > 1 {
		var navRow []InlineKeyboardButton
		if page > 0 {
			navRow = append(navRow, InlineKeyboardButton{
				Text:         "⬅️ Prev",
				CallbackData: fmt.Sprintf("file:list:%s:%d", curToken, page-1),
			})
		}
		if page+1 < totalPages {
			navRow = append(navRow, InlineKeyboardButton{
				Text:         "Next ➡️",
				CallbackData: fmt.Sprintf("file:list:%s:%d", curToken, page+1),
			})
		}
		if len(navRow) > 0 {
			rows = append(rows, navRow)
		}
	}

	rows = append(rows, []InlineKeyboardButton{
		{Text: "📂 Back to Folders", CallbackData: registerDirToken(cleanCurrent)},
		{Text: "🔄 Refresh", CallbackData: fmt.Sprintf("file:list:%s:%d", curToken, page)},
	})

	return sb.String(), &InlineKeyboardMarkup{InlineKeyboard: rows}
}

func formatByteSize(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	kb := float64(b) / 1024.0
	if kb < 1024 {
		return fmt.Sprintf("%.1f KB", kb)
	}
	mb := kb / 1024.0
	return fmt.Sprintf("%.1f MB", mb)
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
				CallbackData: registerDirToken(cleanAd),
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
			{
				{Text: "🔍 AI Code Review", CallbackData: "git:review"},
				{Text: "📂 Dir Menu", CallbackData: "dir:open"},
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

	// Check ahead/behind count relative to upstream if configured
	syncBadge := ""
	abOut, err := exec.Command("git", "-C", cleanDir, "rev-list", "--left-right", "--count", "HEAD...@{u}").Output()
	if err == nil {
		fields := strings.Fields(strings.TrimSpace(string(abOut)))
		if len(fields) == 2 {
			ahead, _ := strconv.Atoi(fields[0])
			behind, _ := strconv.Atoi(fields[1])
			if ahead > 0 && behind > 0 {
				syncBadge = fmt.Sprintf(" (⬆️%d ⬇️%d)", ahead, behind)
			} else if ahead > 0 {
				syncBadge = fmt.Sprintf(" (⬆️%d ahead)", ahead)
			} else if behind > 0 {
				syncBadge = fmt.Sprintf(" (⬇️%d behind)", behind)
			} else {
				syncBadge = " (✓ up to date)"
			}
		}
	}

	lastCommitOut, _ := exec.Command("git", "-C", cleanDir, "log", "-1", "--pretty=format:%h — %s (%cr)").Output()
	lastCommit := strings.TrimSpace(string(lastCommitOut))
	if lastCommit == "" {
		lastCommit = "No commits yet"
	}

	var sb strings.Builder
	sb.WriteString("🌿 <b>Git Repository Dashboard</b>\n")
	sb.WriteString(fmt.Sprintf("📂 <b>Dir:</b> <code>%s</code>\n", escapeTelegram(displayDir)))
	sb.WriteString(fmt.Sprintf("🔀 <b>Branch:</b> <code>%s</code><i>%s</i>\n", escapeTelegram(branch), escapeTelegram(syncBadge)))
	sb.WriteString(fmt.Sprintf("📝 <b>Latest:</b> <code>%s</code>\n\n", escapeTelegram(lastCommit)))

	switch action {
	case "diff":
		unstagedOut, _ := exec.Command("git", "-C", cleanDir, "diff", "--stat").CombinedOutput()
		stagedOut, _ := exec.Command("git", "-C", cleanDir, "diff", "--cached", "--stat").CombinedOutput()
		unstagedStr := strings.TrimSpace(string(unstagedOut))
		stagedStr := strings.TrimSpace(string(stagedOut))

		if unstagedStr == "" && stagedStr == "" {
			sb.WriteString("📊 <b>Diff Summary:</b>\n<i>No staged or unstaged changes.</i>")
		} else {
			if stagedStr != "" {
				if len([]rune(stagedStr)) > 1200 {
					stagedStr = string([]rune(stagedStr)[:1200]) + "\n..."
				}
				sb.WriteString("🟢 <b>Staged Changes:</b>\n")
				sb.WriteString(fmt.Sprintf("<pre>%s</pre>\n", escapeTelegram(stagedStr)))
			}
			if unstagedStr != "" {
				if len([]rune(unstagedStr)) > 1200 {
					unstagedStr = string([]rune(unstagedStr)[:1200]) + "\n..."
				}
				sb.WriteString("🟡 <b>Unstaged Changes:</b>\n")
				sb.WriteString(fmt.Sprintf("<pre>%s</pre>", escapeTelegram(unstagedStr)))
			}
		}

	case "log":
		logOut, _ := exec.Command("git", "-C", cleanDir, "log", "-n", "8", "--pretty=format:• %h — %s (%cr)").CombinedOutput()
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

func renderProgressBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int((percent / 100.0) * float64(width))
	if filled > width {
		filled = width
	}
	return strings.Repeat("▓", filled) + strings.Repeat("░", width-filled)
}

// buildSystemHealthDashboard collects real-time Linux/Termux system and bot metrics
func buildSystemHealthDashboard(workDir string) (string, *InlineKeyboardMarkup) {
	var sb strings.Builder
	sb.WriteString("🖥️ <b>System &amp; Server Health Dashboard</b>\n\n")

	// 1. Bot Uptime & Go Runtime
	botUp := formatDurationShort(time.Since(botStartTime))
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	botRAMMB := float64(memStats.Alloc) / (1024 * 1024)
	sb.WriteString(fmt.Sprintf("🤖 <b>Bot Uptime:</b> <code>%s</code> (%.1f MB RAM, %d goroutines)\n", botUp, botRAMMB, runtime.NumGoroutine()))

	// 2. System Uptime
	if upBytes, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(upBytes))
		if len(fields) > 0 {
			if secs, err := strconv.ParseFloat(fields[0], 64); err == nil {
				sysUp := formatDurationShort(time.Duration(secs) * time.Second)
				sb.WriteString(fmt.Sprintf("⏱️ <b>System Uptime:</b> <code>%s</code> (%s/%s)\n", sysUp, runtime.GOOS, runtime.GOARCH))
			}
		}
	}

	// 3. CPU Load Average
	if loadBytes, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(loadBytes))
		if len(fields) >= 3 {
			sb.WriteString(fmt.Sprintf("⚡ <b>CPU Load (1/5/15m):</b> <code>%s / %s / %s</code> (%d cores)\n", fields[0], fields[1], fields[2], runtime.NumCPU()))
		}
	}

	// 4. Memory Usage (/proc/meminfo)
	if memBytes, err := os.ReadFile("/proc/meminfo"); err == nil {
		var totalKB, availKB int64
		for _, line := range strings.Split(string(memBytes), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				_, _ = fmt.Sscanf(line, "MemTotal: %d kB", &totalKB)
			} else if strings.HasPrefix(line, "MemAvailable:") {
				_, _ = fmt.Sscanf(line, "MemAvailable: %d kB", &availKB)
			}
		}
		if totalKB > 0 {
			usedKB := totalKB - availKB
			pct := (float64(usedKB) / float64(totalKB)) * 100.0
			bar := renderProgressBar(pct, 10)
			sb.WriteString(fmt.Sprintf("🧠 <b>System RAM:</b> <code>%s</code> %.0f%% (%.1f / %.1f GB)\n",
				bar, pct, float64(usedKB)/(1024*1024), float64(totalKB)/(1024*1024)))
		}
	}

	// 5. Disk Usage (syscall.Statfs on workDir)
	var stat syscall.Statfs_t
	if err := syscall.Statfs(workDir, &stat); err == nil && stat.Blocks > 0 {
		totalBytes := stat.Blocks * uint64(stat.Bsize)
		freeBytes := stat.Bavail * uint64(stat.Bsize)
		usedBytes := totalBytes - freeBytes
		pct := (float64(usedBytes) / float64(totalBytes)) * 100.0
		bar := renderProgressBar(pct, 10)
		sb.WriteString(fmt.Sprintf("💾 <b>Disk Space:</b> <code>%s</code> %.0f%% (%.1f / %.1f GB)\n",
			bar, pct, float64(usedBytes)/(1024*1024*1024), float64(totalBytes)/(1024*1024*1024)))
	}

	// 6. Battery Status (sysfs or termux-battery-status)
	if batStr := readBatteryStatus(); batStr != "" {
		sb.WriteString(fmt.Sprintf("🔋 <b>Battery:</b> %s\n", batStr))
	}

	kb := &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🔄 Refresh Health", CallbackData: "server:refresh"},
				{Text: "📊 Bot Status", CallbackData: "status:open"},
			},
			{
				{Text: "📂 Working Dir", CallbackData: "dir:open"},
				{Text: "🌿 Git Dashboard", CallbackData: "git:status"},
			},
		},
	}

	return sb.String(), kb
}

func readBatteryStatus() string {
	// Try Linux/Android sysfs first (instant, zero overhead)
	capBytes, err := os.ReadFile("/sys/class/power_supply/battery/capacity")
	if err == nil {
		capStr := strings.TrimSpace(string(capBytes))
		statBytes, _ := os.ReadFile("/sys/class/power_supply/battery/status")
		statStr := strings.TrimSpace(string(statBytes))
		if statStr != "" {
			return fmt.Sprintf("<code>%s%%</code> (<i>%s</i>)", escapeTelegram(capStr), escapeTelegram(statStr))
		}
		return fmt.Sprintf("<code>%s%%</code>", escapeTelegram(capStr))
	}

	// Try termux-battery-status with a strict 1.2s timeout
	if path, err := exec.LookPath("termux-battery-status"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
		defer cancel()
		out, err := exec.CommandContext(ctx, path).Output()
		if err == nil {
			var bat struct {
				Percentage  int     `json:"percentage"`
				Status      string  `json:"status"`
				Temperature float64 `json:"temperature"`
			}
			if json.Unmarshal(out, &bat) == nil && bat.Percentage > 0 {
				return fmt.Sprintf("<code>%d%%</code> (<i>%s</i>, %.1f°C)", bat.Percentage, escapeTelegram(bat.Status), bat.Temperature)
			}
		}
	}
	return ""
}

func formatDurationShort(d time.Duration) string {
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
	}
	if mins > 0 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
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
		action := cleanArgStr(args, "Action", "action")
		if action != "" {
			return fmt.Sprintf("⏳ Task manager (<code>%s</code>)", escapeTelegram(action))
		}
		return "⏳ Checking background task"

	case "invoke_subagent":
		role := cleanArgStr(args, "Role", "TypeName")
		if role != "" {
			return fmt.Sprintf("🤖 Subagent: <i>%s</i>", escapeTelegram(truncateStr(role, 35)))
		}
		return "🤖 Launching subagent"

	case "define_subagent":
		subName := cleanArgStr(args, "Name")
		if subName != "" {
			return fmt.Sprintf("🛠️ Defining subagent <code>%s</code>", escapeTelegram(subName))
		}
		return "🛠️ Defining subagent"

	case "manage_subagents":
		return "👥 Managing subagents"

	case "send_message":
		return "💬 Messaging subagent"

	case "ask_question":
		return "❓ Preparing question for user"

	case "schedule":
		return "⏰ Scheduling timer/cron task"

	case "grep_search", "find_by_name", "find_file", "search_directory", "list_dir":
		q := cleanArgStr(args, "Query", "Pattern", "query", "DirectoryPath")
		if q != "" {
			return fmt.Sprintf("🔍 Searching: <code>%s</code>", escapeTelegram(truncateStr(filepath.Base(q), 35)))
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
