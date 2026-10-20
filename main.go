package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

// QueuedMessage represents a user message waiting for the active task to finish
type QueuedMessage struct {
	ChatID       int64
	Content      string
	IsBackground bool
}

// SessionState stores user configuration, conversation ID and process handles
type SessionState struct {
	WorkingDir       string
	ConversationID   string
	Continue         bool
	IsBusy           bool
	TaskStartTime    time.Time
	ActiveStep       string
	ActiveThought    string
	CancelFunc       context.CancelFunc
	Cmd              *exec.Cmd
	LastActive       time.Time
	Queue            []QueuedMessage
	PendingFiles     []string
	TurnsCompleted   int
	LastTaskDuration string
	LastUserPrompt   string
}

var (
	sessions = make(map[int64]*SessionState)
	mu       sync.RWMutex

	allowedDirs []string
	defaultDir  string

	allowedUserIDs = make(map[int64]bool)
	allowedChatIDs = make(map[int64]bool)

	botUsername string
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found, reading from environment variables")
	}

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable not set")
	}

	// Parse ALLOWED_USER_IDS
	users := os.Getenv("ALLOWED_USER_IDS")
	if users != "" {
		for _, u := range strings.Split(users, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				id, err := strconv.ParseInt(u, 10, 64)
				if err == nil {
					allowedUserIDs[id] = true
				}
			}
		}
	}

	// Parse ALLOWED_CHAT_IDS
	chats := os.Getenv("ALLOWED_CHAT_IDS")
	if chats != "" {
		for _, c := range strings.Split(chats, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				id, err := strconv.ParseInt(c, 10, 64)
				if err == nil {
					allowedChatIDs[id] = true
				}
			}
		}
	}

	// Parse ALLOWED_DIRS for Sandbox
	dirs := os.Getenv("ALLOWED_DIRS")
	if dirs != "" {
		for _, d := range strings.Split(dirs, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				absD, err := filepath.Abs(d)
				if err == nil {
					allowedDirs = append(allowedDirs, absD)
				}
			}
		}
	}

	if len(allowedDirs) > 0 {
		defaultDir = allowedDirs[0]
	} else {
		defaultDir, _ = os.Getwd()
		allowedDirs = append(allowedDirs, defaultDir)
	}

	log.Printf("Sandbox root dirs: %v", allowedDirs)
	loadSessions()
	if pruned := pruneOldUploads(7 * 24 * time.Hour); pruned > 0 {
		log.Printf("Pruned %d old upload file(s) (>7 days)", pruned)
	}
	_, _ = buildDirKeyboard(defaultDir, allowedDirs)
	_, _ = buildRootsKeyboard(allowedDirs)

	bot := NewTelegramBot(token)
	_ = bot.DeleteWebhook()

	// Graceful shutdown on SIGINT / SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, saving sessions and confirming updates...", sig)
		saveSessions()
		bot.ConfirmUpdates()
		os.Exit(0)
	}()

	// Set Bot Commands
	commands := []map[string]string{
		{"command": "status", "description": "Interactive bot status & control dashboard"},
		{"command": "quick", "description": "1-tap engineering workflows (test, audit, build)"},
		{"command": "dir", "description": "Browse & switch working directory (Sandbox)"},
		{"command": "files", "description": "View code in-chat or download directory files"},
		{"command": "find", "description": "Instant local file & code content search"},
		{"command": "sh", "description": "Run a fast local shell command in working dir"},
		{"command": "export", "description": "Download current workspace as a clean ZIP archive"},
		{"command": "model", "description": "Select and switch active AI model"},
		{"command": "git", "description": "Interactive Git status, diff, log, branches & review"},
		{"command": "server", "description": "System health, RAM, CPU, disk & battery dashboard"},
		{"command": "resume", "description": "Browse and resume previous conversations"},
		{"command": "new", "description": "Start a fresh new conversation"},
		{"command": "tasks", "description": "View active task progress and message queue"},
		{"command": "review", "description": "Run AI code review on current Git changes"},
		{"command": "compact", "description": "Compact conversation context to save memory"},
		{"command": "getfile", "description": "Download a file from server to Telegram"},
		{"command": "cancel", "description": "Cancel currently running task"},
		{"command": "restart", "description": "Restart the bot service"},
		{"command": "goal", "description": "Execute an autonomous deep goal"},
		{"command": "schedule", "description": "Schedule a delayed or recurring task"},
		{"command": "help", "description": "Show commands and help guide"},
	}
	if err := bot.SetMyCommands(commands); err != nil {
		log.Printf("Warning: Failed to set bot commands: %v", err)
	}

	me, err := bot.GetMe()
	if err != nil {
		log.Printf("Warning: Could not get bot info (GetMe): %v", err)
	} else {
		botUsername = me.Username
		log.Printf("🤖 Bot Username: @%s", botUsername)
	}

	log.Println("Bot is running. Press CTRL-C to exit.")

	// Long Polling Loop
	for {
		updates, err := bot.GetUpdates()
		if err != nil {
			log.Printf("Error getting updates: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range updates {
			if update.Message != nil {
				go handleMessage(bot, update.Message)
			} else if update.CallbackQuery != nil {
				go handleCallbackQuery(bot, update.CallbackQuery)
			}
		}
	}
}

func getSession(userID int64) *SessionState {
	mu.Lock()
	defer mu.Unlock()
	state, exists := sessions[userID]
	if !exists {
		state = &SessionState{
			WorkingDir:     defaultDir,
			ConversationID: "",
			Continue:       false,
			IsBusy:         false,
			LastActive:     time.Now(),
		}
		sessions[userID] = state
	}
	return state
}

// killActiveProcess terminates the running agy process and its entire child process group
func killActiveProcess(state *SessionState) bool {
	mu.Lock()
	busy := state.IsBusy
	cancel := state.CancelFunc
	cmdProc := state.Cmd
	mu.Unlock()

	if !busy || cancel == nil {
		return false
	}

	cancel()
	if cmdProc != nil && cmdProc.Process != nil {
		pid := cmdProc.Process.Pid
		if pid > 0 {
			// Kill the entire process group (-pid) so child subshells/tools don't hold pipes open
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = cmdProc.Process.Kill()
	}
	return true
}

func isAuthorized(userID int64, chatID int64) bool {
	if len(allowedChatIDs) > 0 && chatID != userID {
		if !allowedChatIDs[chatID] {
			return false
		}
	}

	if len(allowedUserIDs) == 0 {
		return true
	}

	return allowedUserIDs[userID]
}

// resolveRealPath evaluates symlinks (or parent directory symlinks if the leaf does not exist yet)
func resolveRealPath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	parent := filepath.Dir(clean)
	if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(filepath.Clean(resolvedParent), filepath.Base(clean))
	}
	return clean
}

func isSubDir(parent, child string) bool {
	realParent := resolveRealPath(parent)
	realChild := resolveRealPath(child)
	if realParent == realChild {
		return true
	}
	return strings.HasPrefix(realChild, realParent+string(filepath.Separator))
}

func isPathAllowed(targetPath string) bool {
	cleanTarget := filepath.Clean(targetPath)
	uploadsDir := getUploadsDir()
	if isSubDir(uploadsDir, cleanTarget) {
		return true
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		brainDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain")
		if isSubDir(brainDir, cleanTarget) {
			return true
		}
	}
	for _, ad := range allowedDirs {
		if isSubDir(ad, cleanTarget) {
			return true
		}
	}
	return false
}

// buildStatusDashboard constructs an interactive status overview card
func buildStatusDashboard(state *SessionState) (string, *InlineKeyboardMarkup) {
	mu.RLock()
	busy := state.IsBusy
	workDir := state.WorkingDir
	convID := state.ConversationID
	qCount := len(state.Queue)
	activeStep := state.ActiveStep
	taskStart := state.TaskStartTime
	turns := state.TurnsCompleted
	lastDur := state.LastTaskDuration
	mu.RUnlock()

	model := getActiveModel()
	statusStr := "🟢 Idle &amp; Ready"
	if busy {
		dur := ""
		if !taskStart.IsZero() {
			dur = fmt.Sprintf(" (%s)", formatDurationShort(time.Since(taskStart)))
		}
		if qCount > 0 {
			statusStr = fmt.Sprintf("🟡 Running Task%s · 📥 %d queued", dur, qCount)
		} else {
			statusStr = fmt.Sprintf("🟡 Running Task%s", dur)
		}
		if activeStep != "" {
			statusStr += "\n  └ ⚡ " + activeStep
		}
	}

	convStr := "<i>New Session</i>"
	if convID != "" {
		convStr = fmt.Sprintf("<code>%s</code>", escapeTelegram(convID))
	}

	statsStr := fmt.Sprintf("<code>%d turn(s)</code>", turns)
	if lastDur != "" {
		statsStr += fmt.Sprintf(" (last: <i>%s</i>)", escapeTelegram(lastDur))
	}

	// Check git branch if inside a repo
	gitInfo := ""
	if branchOut, err := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		if br := strings.TrimSpace(string(branchOut)); br != "" {
			gitInfo = fmt.Sprintf(" (🌿 <code>%s</code>)", escapeTelegram(br))
		}
	}

	uptime := formatDurationShort(time.Since(botStartTime))

	statusMsg := fmt.Sprintf(`🤖 <b>Antigravity Status Dashboard</b>

• <b>Model:</b> <i>%s</i>
• <b>Directory:</b> <code>%s</code>%s
• <b>Session:</b> %s
• <b>Activity:</b> %s
• <b>State:</b> %s
• <b>Bot Uptime:</b> <code>%s</code>`,
		escapeTelegram(model),
		escapeTelegram(shortenHomeDir(workDir)),
		gitInfo,
		convStr,
		statsStr,
		statusStr,
		uptime,
	)

	kb := &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🧠 Switch Model", CallbackData: "model:open"},
				{Text: "📂 Browse Dir", CallbackData: "dir:open"},
			},
			{
				{Text: "⚡ Quick Workflows", CallbackData: "quick:open"},
				{Text: "🌿 Git Dashboard", CallbackData: "git:status"},
			},
			{
				{Text: "💬 Sessions", CallbackData: "resume:page:0"},
				{Text: "🖥️ Server Health", CallbackData: "server:refresh"},
			},
			{
				{Text: "🧹 Compact Context", CallbackData: "action:compact"},
				{Text: "🔄 Refresh Status", CallbackData: "status:open"},
			},
		},
	}

	return statusMsg, kb
}

func handleCallbackQuery(bot *TelegramBot, cq *CallbackQuery) {
	if cq.From == nil {
		return
	}

	userID := cq.From.ID
	chatID := int64(0)
	msgID := int64(0)
	if cq.Message != nil && cq.Message.Chat != nil {
		chatID = cq.Message.Chat.ID
		msgID = cq.Message.MessageID
	}

	if !isAuthorized(userID, chatID) {
		_ = bot.AnswerCallbackQuery(cq.ID, "⛔ Unauthorized access.")
		return
	}

	state := getSession(userID)
	data := cq.Data

	switch {
	case data == "status:open":
		text, markup := buildStatusDashboard(state)
		_ = bot.AnswerCallbackQuery(cq.ID, "📊 Status updated")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case data == "server:refresh":
		mu.RLock()
		curDir := state.WorkingDir
		mu.RUnlock()
		text, markup := buildSystemHealthDashboard(curDir)
		_ = bot.AnswerCallbackQuery(cq.ID, "🖥️ System health refreshed")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case data == "server:clean_uploads":
		removed, freed := cleanAllUploads()
		mu.Lock()
		state.PendingFiles = nil
		curDir := state.WorkingDir
		mu.Unlock()
		_ = bot.AnswerCallbackQuery(cq.ID, fmt.Sprintf("🧹 Removed %d file(s) (%s)", removed, formatByteSize(freed)))
		if chatID != 0 && msgID != 0 {
			text, markup := buildSystemHealthDashboard(curDir)
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case data == "quick:open":
		mu.RLock()
		curDir := state.WorkingDir
		mu.RUnlock()
		text, markup := buildQuickActionsKeyboard(curDir)
		_ = bot.AnswerCallbackQuery(cq.ID, "⚡ Quick workflows")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case strings.HasPrefix(data, "quick:"):
		act := strings.TrimPrefix(data, "quick:")
		var prompt string
		switch act {
		case "tests":
			prompt = "Run the full test suite in the current workspace. If any tests fail, diagnose the root cause, fix the code, and re-run the tests until all pass."
		case "audit":
			prompt = "Perform a comprehensive security, concurrency, and bug audit of the codebase in the current working directory. Identify any high-impact issues and fix them."
		case "build":
			prompt = "Run the build and linter/vet checks in the current workspace. Fix any compilation errors or warnings."
		case "refactor":
			prompt = "Inspect the codebase in the current directory for code quality, dead code, or architectural bottlenecks, and apply clean, safe refactoring improvements."
		case "docs":
			prompt = "Inspect the codebase and update README.md and documentation files so they accurately reflect the current architecture, features, and commands."
		case "arch":
			prompt = "Analyze the current workspace and provide a concise, mobile-friendly overview of its architecture, key files, and how components interact."
		default:
			_ = bot.AnswerCallbackQuery(cq.ID, "")
			return
		}
		_ = bot.AnswerCallbackQuery(cq.ID, "⚡ Starting "+act+" workflow...")
		go executeAgy(bot, userID, chatID, prompt, false)

	case data == "model:open":
		text, markup := buildModelKeyboard()
		_ = bot.AnswerCallbackQuery(cq.ID, "🧠 Model selection")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case data == "resume:new":
		mu.Lock()
		state.ConversationID = ""
		state.Continue = false
		mu.Unlock()
		saveSessions()
		_ = bot.AnswerCallbackQuery(cq.ID, "🔄 Started new conversation.")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, "🔄 <b>New Session:</b> Conversation history cleared. Your next message will start a fresh session.", nil, "HTML")
		}

	case strings.HasPrefix(data, "resume:page:"):
		pageStr := strings.TrimPrefix(data, "resume:page:")
		page, _ := strconv.Atoi(pageStr)
		mu.RLock()
		activeConv := state.ConversationID
		mu.RUnlock()
		text, markup, err := buildResumeKeyboard(activeConv, page)
		if err != nil {
			_ = bot.AnswerCallbackQuery(cq.ID, "❌ Error loading sessions")
			return
		}
		_ = bot.AnswerCallbackQuery(cq.ID, fmt.Sprintf("📖 Page %d", page+1))
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case strings.HasPrefix(data, "resume:"):
		convID := strings.TrimPrefix(data, "resume:")
		mu.Lock()
		state.ConversationID = convID
		state.Continue = true
		state.LastActive = time.Now()
		mu.Unlock()
		saveSessions()
		_ = bot.AnswerCallbackQuery(cq.ID, "✅ Switched to conversation.")
		if chatID != 0 && msgID != 0 {
			resp := formatResumeConfirmation(convID)
			bot.EditMessageText(chatID, msgID, resp, nil, "HTML")
		}

	case data == "dir:open":
		mu.RLock()
		curDir := state.WorkingDir
		mu.RUnlock()
		text, markup := buildDirKeyboard(curDir, allowedDirs, 0)
		_ = bot.AnswerCallbackQuery(cq.ID, "📂 Directory menu")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case data == "dir:roots":
		text, markup := buildRootsKeyboard(allowedDirs)
		_ = bot.AnswerCallbackQuery(cq.ID, "🌐 Root folders")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case strings.HasPrefix(data, "dir:page:"):
		rest := strings.TrimPrefix(data, "dir:page:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			targetDir := filepath.Clean(resolveDirToken(parts[0]))
			page, _ := strconv.Atoi(parts[1])
			if !isPathAllowed(targetDir) {
				_ = bot.AnswerCallbackQuery(cq.ID, "⛔ Outside sandbox paths.")
				return
			}
			_ = bot.AnswerCallbackQuery(cq.ID, fmt.Sprintf("📂 Page %d", page+1))
			if chatID != 0 && msgID != 0 {
				text, markup := buildDirKeyboard(targetDir, allowedDirs, page)
				bot.EditMessageText(chatID, msgID, text, markup, "HTML")
			}
		}

	case strings.HasPrefix(data, "dir:zip:"):
		token := strings.TrimPrefix(data, "dir:zip:")
		targetDir := filepath.Clean(resolveDirToken(token))
		if !isPathAllowed(targetDir) {
			_ = bot.AnswerCallbackQuery(cq.ID, "⛔ Outside sandbox paths.")
			return
		}
		_ = bot.AnswerCallbackQuery(cq.ID, "📦 Creating ZIP archive...")
		zipPath, count, zipSize, err := exportDirToZip(targetDir)
		if err != nil {
			bot.SendMessage(chatID, fmt.Sprintf("❌ Failed to create ZIP archive: %v", err))
			return
		}
		defer os.Remove(zipPath)
		caption := fmt.Sprintf("📦 <b>Workspace Archive:</b> <code>%s</code>\n📄 %d file(s) · %s",
			escapeTelegram(filepath.Base(targetDir)), count, formatByteSize(zipSize))
		if _, err := bot.SendDocument(chatID, zipPath, caption); err != nil {
			bot.SendMessage(chatID, fmt.Sprintf("❌ Failed to send ZIP archive: %v", err))
		}

	case strings.HasPrefix(data, "dir:cd:"):
		rawTarget := strings.TrimPrefix(data, "dir:cd:")
		targetDir := filepath.Clean(resolveDirToken(rawTarget))

		isAllowed := false
		for _, ad := range allowedDirs {
			if isSubDir(ad, targetDir) {
				isAllowed = true
				break
			}
		}

		if !isAllowed {
			_ = bot.AnswerCallbackQuery(cq.ID, "⛔ Directory is outside allowed sandbox paths.")
			return
		}

		mu.Lock()
		state.WorkingDir = targetDir
		state.LastActive = time.Now()
		mu.Unlock()
		saveSessions()

		_ = bot.AnswerCallbackQuery(cq.ID, fmt.Sprintf("📂 %s", filepath.Base(targetDir)))
		if chatID != 0 && msgID != 0 {
			text, markup := buildDirKeyboard(targetDir, allowedDirs, 0)
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case strings.HasPrefix(data, "file:list:"):
		rest := strings.TrimPrefix(data, "file:list:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			targetDir := filepath.Clean(resolveDirToken(parts[0]))
			page, _ := strconv.Atoi(parts[1])
			if !isPathAllowed(targetDir) {
				_ = bot.AnswerCallbackQuery(cq.ID, "⛔ Outside sandbox paths.")
				return
			}
			_ = bot.AnswerCallbackQuery(cq.ID, "📄 Files list")
			if chatID != 0 && msgID != 0 {
				text, markup := buildFilesKeyboard(targetDir, page)
				bot.EditMessageText(chatID, msgID, text, markup, "HTML")
			}
		}

	case strings.HasPrefix(data, "file:view:"):
		rest := strings.TrimPrefix(data, "file:view:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			targetPath := filepath.Clean(resolveDirToken(parts[0]))
			page, _ := strconv.Atoi(parts[1])
			if !isPathAllowed(targetPath) {
				_ = bot.AnswerCallbackQuery(cq.ID, "⛔ File outside sandbox.")
				return
			}
			_ = bot.AnswerCallbackQuery(cq.ID, "👁️ "+filepath.Base(targetPath))
			if chatID != 0 && msgID != 0 {
				text, markup := buildFilePreviewKeyboard(targetPath, page)
				bot.EditMessageText(chatID, msgID, text, markup, "HTML")
			}
		}

	case strings.HasPrefix(data, "file:dl:"):
		token := strings.TrimPrefix(data, "file:dl:")
		targetPath := filepath.Clean(resolveDirToken(token))
		if !isPathAllowed(targetPath) {
			_ = bot.AnswerCallbackQuery(cq.ID, "⛔ File is outside sandbox paths.")
			return
		}
		info, err := os.Stat(targetPath)
		if err != nil || info.IsDir() {
			_ = bot.AnswerCallbackQuery(cq.ID, "❌ File not found.")
			return
		}
		_ = bot.AnswerCallbackQuery(cq.ID, "📤 Sending "+filepath.Base(targetPath)+"...")
		if chatID != 0 {
			caption := fmt.Sprintf("📄 <code>%s</code> (%s)", escapeTelegram(filepath.Base(targetPath)), formatByteSize(info.Size()))
			if _, err := bot.SendFileAuto(chatID, targetPath, caption); err != nil {
				bot.SendMessage(chatID, fmt.Sprintf("❌ Failed to send file: %v", err))
			}
		}

	case strings.HasPrefix(data, "file:act:"):
		rest := strings.TrimPrefix(data, "file:act:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			act := parts[0]
			targetPath := filepath.Clean(resolveDirToken(parts[1]))
			if !isPathAllowed(targetPath) {
				_ = bot.AnswerCallbackQuery(cq.ID, "⛔ File outside sandbox.")
				return
			}
			// Remove from PendingFiles if present
			mu.Lock()
			var remaining []string
			for _, pf := range state.PendingFiles {
				if pf != targetPath {
					remaining = append(remaining, pf)
				}
			}
			state.PendingFiles = remaining
			mu.Unlock()

			var prompt string
			switch act {
			case "analyze":
				prompt = fmt.Sprintf("Please read and thoroughly analyze the uploaded file at '%s'. Explain what it does, its structure, and any notable issues.", targetPath)
			case "fix":
				prompt = fmt.Sprintf("Please inspect the uploaded file at '%s', identify any bugs, errors, or code quality issues, and provide or apply the fix.", targetPath)
			case "sum":
				prompt = fmt.Sprintf("Please read the uploaded file at '%s' and provide a concise, clear summary of its key contents.", targetPath)
			default:
				prompt = fmt.Sprintf("Please inspect the uploaded file at '%s'.", targetPath)
			}
			_ = bot.AnswerCallbackQuery(cq.ID, "⚡ Processing "+filepath.Base(targetPath)+"...")
			go executeAgy(bot, userID, chatID, prompt, false)
		}

	case strings.HasPrefix(data, "ask:opt:"):
		token := strings.TrimPrefix(data, "ask:opt:")
		optText := resolveAskOption(token)
		if optText == "" {
			_ = bot.AnswerCallbackQuery(cq.ID, "ℹ️ Option expired, please type your reply.")
			return
		}
		_ = bot.AnswerCallbackQuery(cq.ID, "✅ Selected: "+truncateStr(optText, 30))
		go executeAgy(bot, userID, chatID, optText, false)

	case strings.HasPrefix(data, "model:"):
		modelID := strings.TrimPrefix(data, "model:")
		err := setActiveModel(modelID)
		if err != nil {
			_ = bot.AnswerCallbackQuery(cq.ID, "❌ Failed to change model.")
		} else {
			_ = bot.AnswerCallbackQuery(cq.ID, fmt.Sprintf("✅ Model: %s", modelID))
			if chatID != 0 && msgID != 0 {
				resp := fmt.Sprintf("🧠 <b>Active AI Model Changed:</b>\n<code>%s</code>\n\nSubsequent requests will run with this model.", escapeTelegram(modelID))
				bot.EditMessageText(chatID, msgID, resp, nil, "HTML")
			}
		}

	case data == "action:continue":
		_ = bot.AnswerCallbackQuery(cq.ID, "Continuing...")
		go executeAgy(bot, userID, chatID, "Please continue where you left off.", false)

	case data == "action:retry":
		mu.RLock()
		lastPrompt := state.LastUserPrompt
		mu.RUnlock()
		if lastPrompt == "" {
			_ = bot.AnswerCallbackQuery(cq.ID, "ℹ️ No previous prompt to retry.")
			return
		}
		_ = bot.AnswerCallbackQuery(cq.ID, "🔄 Retrying prompt...")
		go executeAgy(bot, userID, chatID, lastPrompt, false)

	case data == "action:compact":
		_ = bot.AnswerCallbackQuery(cq.ID, "🧹 Compacting conversation context...")
		go executeAgy(bot, userID, chatID, "/compact", false)

	case data == "action:cancel":
		if killActiveProcess(state) {
			_ = bot.AnswerCallbackQuery(cq.ID, "🛑 Task cancelled.")
			if chatID != 0 && msgID != 0 {
				bot.EditMessageText(chatID, msgID, "🛑 <b>Task cancelled by user.</b>", nil, "HTML")
			}
		} else {
			_ = bot.AnswerCallbackQuery(cq.ID, "ℹ️ No active task running.")
		}

	case data == "git:review":
		_ = bot.AnswerCallbackQuery(cq.ID, "🔍 Starting AI Code Review...")
		reviewPrompt := "Run `git status` and `git diff` (both staged and unstaged) in the current repository and perform a thorough code review: highlight potential bugs, edge cases, security issues, and concrete improvements."
		go executeAgy(bot, userID, chatID, reviewPrompt, false)

	case strings.HasPrefix(data, "git:"):
		action := strings.TrimPrefix(data, "git:")
		mu.RLock()
		curDir := state.WorkingDir
		mu.RUnlock()
		text, markup := buildGitDashboard(curDir, action)
		_ = bot.AnswerCallbackQuery(cq.ID, "🌿 Git "+action)
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case data == "queue:run_now":
		if killActiveProcess(state) {
			_ = bot.AnswerCallbackQuery(cq.ID, "⚡ Interrupting current task...")
			if chatID != 0 && msgID != 0 {
				bot.EditMessageText(chatID, msgID, "⚡ <b>Interrupted active task. Starting queued message now...</b>", nil, "HTML")
			}
		} else {
			_ = bot.AnswerCallbackQuery(cq.ID, "ℹ️ Task already started or finished.")
		}

	case data == "queue:clear":
		mu.Lock()
		count := len(state.Queue)
		state.Queue = nil
		mu.Unlock()
		_ = bot.AnswerCallbackQuery(cq.ID, "🗑️ Queue cleared.")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, fmt.Sprintf("🗑️ <b>Queue cleared</b> (%d pending message(s) removed).", count), nil, "HTML")
		}

	default:
		_ = bot.AnswerCallbackQuery(cq.ID, "")
	}
}

func handleMessage(bot *TelegramBot, m *Message) {
	if m.From == nil || m.From.IsBot {
		return
	}

	if !isAuthorized(m.From.ID, m.Chat.ID) {
		log.Printf("⛔ Unauthorized message ignored from User %d.", m.From.ID)
		return
	}

	// Handle Document / File upload from Telegram
	if m.Document != nil {
		handleIncomingDocument(bot, m)
		return
	}

	// Handle Photo / Screenshot upload from Telegram
	if len(m.Photo) > 0 {
		handleIncomingPhoto(bot, m)
		return
	}

	// Handle Voice / Audio / Video upload from Telegram
	if m.Voice != nil || m.Audio != nil || m.Video != nil {
		handleIncomingMedia(bot, m)
		return
	}

	log.Printf("📥 Message from User %d in Chat %d: %q", m.From.ID, m.Chat.ID, m.Text)

	content := strings.TrimSpace(m.Text)
	if content == "" {
		return
	}

	// Map persistent reply keyboard buttons to slash commands
	switch content {
	case "📊 Status":
		content = "/status"
	case "📁 Dir":
		content = "/dir"
	case "🧠 Model":
		content = "/model"
	case "🔄 Continue":
		content = "/continue"
	case "💬 Resume":
		content = "/resume"
	case "✨ New":
		content = "/new"
	case "🌿 Git":
		content = "/git"
	case "⚡ Quick":
		content = "/quick"
	case "🖥️ Server":
		content = "/server"
	case "🛑 Cancel":
		content = "/cancel"
	}

	if botUsername != "" {
		mention := "@" + botUsername
		isGroup := m.Chat.Type == "group" || m.Chat.Type == "supergroup"

		if isGroup && !strings.HasPrefix(content, "/") && !strings.Contains(content, mention) {
			return
		}

		content = strings.Replace(content, mention, "", -1)
		content = strings.TrimSpace(content)
		if content == "" {
			return
		}
	} else if (m.Chat.Type == "group" || m.Chat.Type == "supergroup") && !strings.HasPrefix(content, "/") {
		return
	}

	// Slash Commands
	if strings.HasPrefix(content, "/") {
		parts := strings.SplitN(content, " ", 2)
		cmd := strings.ToLower(parts[0])
		if atIdx := strings.Index(cmd, "@"); atIdx != -1 {
			cmd = cmd[:atIdx]
		}
		args := ""
		if len(parts) > 1 {
			args = strings.TrimSpace(parts[1])
		}

		state := getSession(m.From.ID)

		switch cmd {
		case "/start", "/help":
			helpText := `👋 <b>Google Antigravity Telegram Gateway</b>

<b>Navigation &amp; Control:</b>
• <b>/status</b> — Interactive status &amp; quick-action dashboard
• <b>/quick</b> — 1-tap workflows (test, audit, build, refactor)
• <b>/dir [path]</b> — Browse &amp; switch working directory
• <b>/files</b> — View code in-chat or 1-tap download files
• <b>/find &lt;query&gt;</b> — Instant workspace filename &amp; text search
• <b>/sh &lt;cmd&gt;</b> — Fast local shell command (0 LLM wait)
• <b>/export</b> — Download current workspace as a clean ZIP archive
• <b>/model [name]</b> — Switch active AI model (` + "<code>Gemini</code> / <code>Claude</code>" + `)
• <b>/git</b> — Interactive Git status, diff, log, branches &amp; AI review
• <b>/server</b> — Real-time RAM, CPU, disk &amp; battery health

<b>Sessions &amp; Tasks:</b>
• <b>/resume [query]</b> — Browse or search saved conversations
• <b>/new</b> — Start a fresh conversation session
• <b>/continue</b> — Continue where the agent left off
• <b>/compact</b> — Compress conversation context
• <b>/tasks</b> — Inspect active task &amp; queued prompts
• <b>/cancel</b> — Immediately stop the running task
• <b>/restart</b> — Gracefully save state &amp; restart bot

<b>Agent Power Tools:</b>
• <b>/review</b> — Run AI code review on uncommitted changes
• <b>/getfile &lt;path&gt;</b> — Download any file from sandbox
• <b>/goal &lt;task&gt;</b> — Run an autonomous deep background goal
• <b>/schedule &lt;time&gt; &lt;task&gt;</b> — Schedule a timer or cron task

💡 <i>Tip: You can also send code files, photos/screenshots, voice messages, or swipe-to-reply to any message!</i>`
			bot.SendMessageWithMarkup(m.Chat.ID, helpText, getDefaultReplyKeyboard(), "HTML")
			return

		case "/restart":
			saveSessions()
			bot.SendMessage(m.Chat.ID, "🔄 <b>Saving session state &amp; restarting bot...</b> (Active in 1s)", "HTML")
			bot.ConfirmUpdates()
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
			return

		case "/continue":
			go executeAgy(bot, m.From.ID, m.Chat.ID, "Please continue where you left off.", false)
			return

		case "/compact":
			go executeAgy(bot, m.From.ID, m.Chat.ID, "/compact", false)
			return

		case "/review":
			reviewPrompt := "Run `git status` and `git diff` (both staged and unstaged) in the current repository and perform a thorough code review: highlight potential bugs, edge cases, security issues, and concrete improvements."
			if args != "" {
				reviewPrompt += "\nAdditional focus: " + args
			}
			go executeAgy(bot, m.From.ID, m.Chat.ID, reviewPrompt, false)
			return

		case "/model":
			if args != "" {
				resolvedModel := resolveModelArg(args)
				if err := setActiveModel(resolvedModel); err != nil {
					bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to change model: %v", err))
				} else {
					bot.SendMessage(m.Chat.ID, fmt.Sprintf("🧠 <b>Active AI Model Changed:</b>\n<code>%s</code>", escapeTelegram(resolvedModel)), "HTML")
				}
				return
			}
			text, markup := buildModelKeyboard()
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/find", "/search":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "🔎 Please specify a filename or text pattern (e.g. <code>/find handleMessage</code>).", "HTML")
				return
			}
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			text, markup := buildFindResultsKeyboard(curDir, args)
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/sh", "/cmd", "/exec":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "💻 Please specify a shell command to run (e.g. <code>/sh go test ./...</code>).", "HTML")
				return
			}
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			outHTML, kb, fixPrompt := runQuickShellCommand(curDir, args)
			if fixPrompt != "" {
				mu.Lock()
				state.LastUserPrompt = fixPrompt
				mu.Unlock()
			}
			bot.SendMessageWithMarkup(m.Chat.ID, outHTML, kb, "HTML")
			return

		case "/export", "/zip":
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			if !isPathAllowed(curDir) {
				bot.SendMessage(m.Chat.ID, "⛔ Current directory is outside sandbox paths.")
				return
			}
			zipPath, count, zipSize, err := exportDirToZip(curDir)
			if err != nil {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to create ZIP archive: %v", err))
				return
			}
			defer os.Remove(zipPath)
			caption := fmt.Sprintf("📦 <b>Workspace Archive:</b> <code>%s</code>\n📄 %d file(s) · %s",
				escapeTelegram(filepath.Base(curDir)), count, formatByteSize(zipSize))
			if _, err := bot.SendDocument(m.Chat.ID, zipPath, caption); err != nil {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to send ZIP archive: %v", err))
			}
			return

		case "/quick", "/workflows":
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			text, markup := buildQuickActionsKeyboard(curDir)
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/server", "/health", "/system":
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			text, markup := buildSystemHealthDashboard(curDir)
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/files", "/ls":
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			text, markup := buildFilesKeyboard(curDir, 0)
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/git":
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			action := "status"
			if args == "diff" || args == "log" || args == "pull" {
				action = args
			} else if args == "review" {
				reviewPrompt := "Run `git status` and `git diff` (both staged and unstaged) in the current repository and perform a thorough code review: highlight potential bugs, edge cases, security issues, and concrete improvements."
				go executeAgy(bot, m.From.ID, m.Chat.ID, reviewPrompt, false)
				return
			}
			text, markup := buildGitDashboard(curDir, action)
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/getfile", "/file", "/download":
			if args == "" {
				mu.RLock()
				curDir := state.WorkingDir
				mu.RUnlock()
				text, markup := buildFilesKeyboard(curDir, 0)
				bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
				return
			}
			mu.RLock()
			currentDir := state.WorkingDir
			mu.RUnlock()

			var targetPath string
			if filepath.IsAbs(args) {
				targetPath = filepath.Clean(args)
			} else {
				targetPath = filepath.Clean(filepath.Join(currentDir, args))
			}

			if !isPathAllowed(targetPath) {
				bot.SendMessage(m.Chat.ID, "⛔ File is outside allowed sandbox paths.")
				return
			}

			info, err := os.Stat(targetPath)
			if err != nil || info.IsDir() {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ File not found: <code>%s</code>", escapeTelegram(filepath.Base(targetPath))), "HTML")
				return
			}

			caption := fmt.Sprintf("📄 <code>%s</code> (%s)", escapeTelegram(filepath.Base(targetPath)), formatByteSize(info.Size()))
			_, err = bot.SendFileAuto(m.Chat.ID, targetPath, caption)
			if err != nil {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to send file: %v", err))
			}
			return

		case "/status":
			text, markup := buildStatusDashboard(state)
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/tasks", "/ps", "/queue":
			mu.RLock()
			busy := state.IsBusy
			taskStart := state.TaskStartTime
			activeStep := state.ActiveStep
			activeThought := state.ActiveThought
			qCopy := append([]QueuedMessage(nil), state.Queue...)
			mu.RUnlock()

			if !busy && len(qCopy) == 0 {
				bot.SendMessage(m.Chat.ID, "🟢 <b>No active tasks or queued messages.</b>\nThe agent is idle and ready.", "HTML")
				return
			}

			var sb strings.Builder
			sb.WriteString("⚙️ <b>Active Task &amp; Queue Monitor</b>\n\n")
			if busy {
				dur := formatDurationShort(time.Since(taskStart))
				sb.WriteString(fmt.Sprintf("🟡 <b>Running Task:</b> active for <code>%s</code>\n", dur))
				if activeStep != "" {
					sb.WriteString(fmt.Sprintf("⚡ <b>Step:</b> %s\n", activeStep))
				}
				if activeThought != "" {
					sb.WriteString(fmt.Sprintf("💭 <b>Thought:</b> <i>%s</i>\n", escapeTelegram(activeThought)))
				}
			}
			if len(qCopy) > 0 {
				sb.WriteString(fmt.Sprintf("\n📥 <b>Queued Messages (%d):</b>\n", len(qCopy)))
				for i, qm := range qCopy {
					sb.WriteString(fmt.Sprintf("%d. <i>%s</i>\n", i+1, escapeTelegram(truncateStr(qm.Content, 80))))
				}
			}

			var rows [][]InlineKeyboardButton
			if busy {
				rows = append(rows, []InlineKeyboardButton{
					{Text: "🛑 Cancel Active Task", CallbackData: "action:cancel"},
				})
			}
			if len(qCopy) > 0 {
				rows = append(rows, []InlineKeyboardButton{
					{Text: "⚡ Interrupt & Run Next", CallbackData: "queue:run_now"},
					{Text: "🗑️ Clear Queue", CallbackData: "queue:clear"},
				})
			}
			bot.SendMessageWithMarkup(m.Chat.ID, sb.String(), &InlineKeyboardMarkup{InlineKeyboard: rows}, "HTML")
			return

		case "/cancel", "/stop":
			if !killActiveProcess(state) {
				bot.SendMessage(m.Chat.ID, "ℹ️ No task is currently running.")
				return
			}
			bot.SendMessage(m.Chat.ID, "🛑 <b>Task cancelled.</b>", "HTML")
			return

		case "/resume":
			mu.RLock()
			activeConv := state.ConversationID
			mu.RUnlock()
			if args != "" {
				exactID, searchMsg, searchMarkup := searchConversations(args, activeConv)
				if exactID != "" {
					mu.Lock()
					state.ConversationID = exactID
					state.Continue = true
					state.LastActive = time.Now()
					mu.Unlock()
					saveSessions()
					bot.SendMessage(m.Chat.ID, formatResumeConfirmation(exactID), "HTML")
					return
				}
				if searchMarkup != nil {
					bot.SendMessageWithMarkup(m.Chat.ID, searchMsg, searchMarkup, "HTML")
				} else {
					bot.SendMessage(m.Chat.ID, searchMsg, "HTML")
				}
				return
			}
			text, markup, err := buildResumeKeyboard(activeConv, 0)
			if err != nil {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to read conversation history: %v", err))
				return
			}
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/new":
			mu.Lock()
			state.ConversationID = ""
			state.Continue = false
			mu.Unlock()
			saveSessions()
			bot.SendMessage(m.Chat.ID, "🔄 Session reset. Your next message will start a fresh conversation!")
			return

		case "/dir":
			if args == "" {
				mu.RLock()
				curDir := state.WorkingDir
				mu.RUnlock()
				text, markup := buildDirKeyboard(curDir, allowedDirs, 0)
				bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
				return
			}

			reqDir := args
			mu.RLock()
			currentDir := state.WorkingDir
			mu.RUnlock()

			var targetDir string
			if filepath.IsAbs(reqDir) {
				targetDir = filepath.Clean(reqDir)
			} else {
				targetDir = filepath.Clean(filepath.Join(currentDir, reqDir))
			}

			isAllowed := false
			for _, ad := range allowedDirs {
				if isSubDir(ad, targetDir) {
					isAllowed = true
					break
				}
			}

			if !isAllowed {
				bot.SendMessage(m.Chat.ID, "⛔ Directory is outside allowed sandbox paths (ALLOWED_DIRS).")
				return
			}

			info, err := os.Stat(targetDir)
			if os.IsNotExist(err) {
				err = os.MkdirAll(targetDir, 0755)
				if err != nil {
					bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to create directory: %v", err))
					return
				}
			} else if err != nil {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Directory error: %v", err))
				return
			} else if !info.IsDir() {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ `%s` is not a directory!", targetDir))
				return
			}

			mu.Lock()
			state.WorkingDir = targetDir
			state.LastActive = time.Now()
			mu.Unlock()
			saveSessions()
			bot.SendMessage(m.Chat.ID, fmt.Sprintf("📂 Working directory changed to: <code>%s</code>", escapeTelegram(shortenHomeDir(targetDir))), "HTML")
			return

		case "/goal", "/grill_me", "/grill-me", "/browser", "/plan", "/boost":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "❌ Please specify a prompt or task for this command.")
				return
			}
			internalCmd := cmd
			if cmd == "/grill_me" {
				internalCmd = "/grill-me"
			}
			fullPrompt := fmt.Sprintf("%s %s", internalCmd, args)
			go executeAgy(bot, m.From.ID, m.Chat.ID, fullPrompt, true)
			return

		case "/schedule":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "❌ Please specify time and task (e.g., /schedule in 5m remind me).")
				return
			}
			fullPrompt := fmt.Sprintf("/schedule %s", args)
			go executeAgy(bot, m.From.ID, m.Chat.ID, fullPrompt, true)
			return
		}
	}

	// Enrich prompt with ReplyToMessage context if user swiped-to-reply on Telegram
	if m.ReplyToMessage != nil {
		quoted := strings.TrimSpace(m.ReplyToMessage.Text)
		if quoted == "" {
			quoted = strings.TrimSpace(m.ReplyToMessage.Caption)
		}
		if quoted != "" {
			// Strip footer line if replying to a bot message
			if idx := strings.Index(quoted, "┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈"); idx != -1 {
				quoted = strings.TrimSpace(quoted[:idx])
			}
			runes := []rune(quoted)
			if len(runes) > 1500 {
				quoted = string(runes[:1497]) + "..."
			}
			content = fmt.Sprintf("[Replying to earlier message:\n\"\"\"\n%s\n\"\"\"]\n\n%s", quoted, content)
		}
	}

	// Attach any pending uploaded files (uploaded without caption right before this message)
	state := getSession(m.From.ID)
	mu.Lock()
	var attachedFiles []string
	if len(state.PendingFiles) > 0 {
		attachedFiles = append(attachedFiles, state.PendingFiles...)
		state.PendingFiles = nil
	}
	mu.Unlock()
	if len(attachedFiles) > 0 {
		content = fmt.Sprintf("[User uploaded file(s): %s]\n\n%s", strings.Join(attachedFiles, ", "), content)
	}

	// Normal text message
	executeAgy(bot, m.From.ID, m.Chat.ID, content, false)
}

// getUploadsDir returns the dedicated fixed directory under the bot folder for incoming files/photos/media
func getUploadsDir() string {
	exePath, err := os.Executable()
	var baseDir string
	if err == nil {
		baseDir = filepath.Dir(exePath)
	} else {
		baseDir, _ = os.Getwd()
	}
	uploadsDir := filepath.Join(baseDir, "uploads")
	_ = os.MkdirAll(uploadsDir, 0755)
	return uploadsDir
}

func handleIncomingDocument(bot *TelegramBot, m *Message) {
	if m.From == nil || m.Document == nil {
		return
	}
	if !isAuthorized(m.From.ID, m.Chat.ID) {
		return
	}
	uploadsDir := getUploadsDir()

	fileInfo, err := bot.GetFile(m.Document.FileID)
	if err != nil {
		bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to get file details: %v", err))
		return
	}

	saveName := filepath.Base(m.Document.FileName)
	if saveName == "" || saveName == "." {
		saveName = fmt.Sprintf("file_%s", time.Now().Format("20060102_150405"))
	}
	destPath := filepath.Join(uploadsDir, saveName)
	// Prevent overwriting an existing uploaded file with the same name
	if _, err := os.Stat(destPath); err == nil {
		ext := filepath.Ext(saveName)
		base := strings.TrimSuffix(saveName, ext)
		saveName = fmt.Sprintf("%s_%s%s", base, time.Now().Format("150405"), ext)
		destPath = filepath.Join(uploadsDir, saveName)
	}

	if err := bot.DownloadFile(fileInfo.FilePath, destPath); err != nil {
		bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to download file: %v", err))
		return
	}

	ackText := fmt.Sprintf("📥 <b>File saved:</b> <code>%s</code> (%s)\n📂 Location: <code>%s</code>",
		escapeTelegram(saveName), formatByteSize(m.Document.FileSize), escapeTelegram(shortenHomeDir(destPath)))

	caption := strings.TrimSpace(m.Caption)
	if caption != "" {
		bot.SendMessage(m.Chat.ID, ackText, "HTML")
		prompt := fmt.Sprintf("I uploaded a file at '%s'. %s", destPath, caption)
		executeAgy(bot, m.From.ID, m.Chat.ID, prompt, false)
	} else {
		state := getSession(m.From.ID)
		mu.Lock()
		state.PendingFiles = append(state.PendingFiles, destPath)
		mu.Unlock()

		fToken := makePathToken(destPath)
		fileMarkup := &InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{
				{
					{Text: "🔍 Analyze File", CallbackData: "file:act:analyze:" + fToken},
					{Text: "🛠️ Fix / Improve", CallbackData: "file:act:fix:" + fToken},
				},
				{
					{Text: "📝 Summarize", CallbackData: "file:act:sum:" + fToken},
				},
			},
		}
		bot.SendMessageWithMarkup(m.Chat.ID, ackText+"\n\n<i>Tap an action below or type your instructions in the next message:</i>", fileMarkup, "HTML")
	}
}

func handleIncomingPhoto(bot *TelegramBot, m *Message) {
	if m.From == nil || len(m.Photo) == 0 {
		return
	}
	if !isAuthorized(m.From.ID, m.Chat.ID) {
		return
	}
	uploadsDir := getUploadsDir()

	bestPhoto := m.Photo[len(m.Photo)-1]
	fileInfo, err := bot.GetFile(bestPhoto.FileID)
	if err != nil {
		bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to get photo details: %v", err))
		return
	}

	ext := filepath.Ext(fileInfo.FilePath)
	if ext == "" {
		ext = ".jpg"
	}
	saveName := fmt.Sprintf("photo_%s%s", time.Now().Format("20060102_150405"), ext)
	destPath := filepath.Join(uploadsDir, saveName)

	if err := bot.DownloadFile(fileInfo.FilePath, destPath); err != nil {
		bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to download photo: %v", err))
		return
	}

	ackText := fmt.Sprintf("🖼️ <b>Photo saved:</b> <code>%s</code> (%s)\n📂 Location: <code>%s</code>",
		escapeTelegram(saveName), formatByteSize(bestPhoto.FileSize), escapeTelegram(shortenHomeDir(destPath)))
	bot.SendMessage(m.Chat.ID, ackText, "HTML")

	caption := strings.TrimSpace(m.Caption)
	var prompt string
	if caption != "" {
		prompt = fmt.Sprintf("User uploaded an image/screenshot at '%s'. Please inspect this image file (using read_file) and fulfill the following request: %s", destPath, caption)
	} else {
		prompt = fmt.Sprintf("User uploaded an image/screenshot at '%s'. Please inspect this image file (using read_file), analyze its contents, and if you see an error or issue, explain the solution.", destPath)
	}
	executeAgy(bot, m.From.ID, m.Chat.ID, prompt, false)
}

func handleIncomingMedia(bot *TelegramBot, m *Message) {
	if m.From == nil {
		return
	}
	if !isAuthorized(m.From.ID, m.Chat.ID) {
		return
	}
	uploadsDir := getUploadsDir()

	var fileID, defaultExt, prefix, mediaLabel string
	var fileSize int64

	if m.Voice != nil {
		fileID = m.Voice.FileID
		fileSize = m.Voice.FileSize
		defaultExt = ".ogg"
		prefix = "voice"
		mediaLabel = "🎙️ Voice message"
	} else if m.Audio != nil {
		fileID = m.Audio.FileID
		fileSize = m.Audio.FileSize
		defaultExt = filepath.Ext(m.Audio.FileName)
		if defaultExt == "" {
			defaultExt = ".mp3"
		}
		prefix = "audio"
		mediaLabel = "🎵 Audio file"
	} else if m.Video != nil {
		fileID = m.Video.FileID
		fileSize = m.Video.FileSize
		defaultExt = filepath.Ext(m.Video.FileName)
		if defaultExt == "" {
			defaultExt = ".mp4"
		}
		prefix = "video"
		mediaLabel = "🎬 Video"
	} else {
		return
	}

	fileInfo, err := bot.GetFile(fileID)
	if err != nil {
		bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to get media details: %v", err))
		return
	}

	ext := filepath.Ext(fileInfo.FilePath)
	if ext == "" {
		ext = defaultExt
	}
	saveName := fmt.Sprintf("%s_%s%s", prefix, time.Now().Format("20060102_150405"), ext)
	destPath := filepath.Join(uploadsDir, saveName)

	if err := bot.DownloadFile(fileInfo.FilePath, destPath); err != nil {
		bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to download media: %v", err))
		return
	}

	ackText := fmt.Sprintf("%s <b>saved:</b> <code>%s</code> (%s)\n📂 Location: <code>%s</code>",
		mediaLabel, escapeTelegram(saveName), formatByteSize(fileSize), escapeTelegram(shortenHomeDir(destPath)))
	bot.SendMessage(m.Chat.ID, ackText, "HTML")

	caption := strings.TrimSpace(m.Caption)
	var prompt string
	if m.Voice != nil {
		if caption != "" {
			prompt = fmt.Sprintf("User sent a voice message saved at '%s' with note: %s. Please inspect/listen to this audio file (using read_file) and fulfill the spoken request.", destPath, caption)
		} else {
			prompt = fmt.Sprintf("User sent a voice message saved at '%s'. Please inspect/listen to this audio file (using read_file), understand what the user is asking, and fulfill their request directly.", destPath)
		}
	} else {
		if caption != "" {
			prompt = fmt.Sprintf("User uploaded a %s file at '%s'. Please inspect it (using read_file) and fulfill this request: %s", prefix, destPath, caption)
		} else {
			prompt = fmt.Sprintf("User uploaded a %s file at '%s'. Please inspect it (using read_file) and summarize or address its contents.", prefix, destPath)
		}
	}
	executeAgy(bot, m.From.ID, m.Chat.ID, prompt, false)
}

// EventPayload for parsing agy --output-format stream-json
type EventPayload struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id,omitempty"`
	StepUpdate     *struct {
		StepIndex int    `json:"step_index"`
		State     string `json:"state"`
		StepType  string `json:"step_type"`
		ToolName  string `json:"tool_name,omitempty"`
		ToolInfo  struct {
			Name       string                 `json:"name"`
			Parameters map[string]interface{} `json:"parameters"`
		} `json:"tool_info,omitempty"`
		TextDelta string `json:"text_delta,omitempty"`
	} `json:"step_update,omitempty"`
	Result *struct {
		ConversationID string `json:"conversation_id"`
		Status         string `json:"status"`
		Response       string `json:"response"`
	} `json:"result,omitempty"`
}

func truncateStr(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max-2]) + ".."
	}
	return s
}

func executeAgy(bot *TelegramBot, userID int64, chatID int64, content string, isBackground bool) {
	state := getSession(userID)

	cmdPath := os.Getenv("ANTIGRAVITY_CMD")
	if cmdPath == "" {
		cmdPath = "agy"
	}

	mu.Lock()
	if !isBackground && !state.LastActive.IsZero() && time.Since(state.LastActive) > 2*time.Hour {
		state.Continue = false
		state.ConversationID = ""
	}
	if !isBackground {
		state.LastActive = time.Now()
	}

	if !isBackground && state.IsBusy {
		state.Queue = append(state.Queue, QueuedMessage{
			ChatID:       chatID,
			Content:      content,
			IsBackground: false,
		})
		qLen := len(state.Queue)
		mu.Unlock()

		qMarkup := &InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{
				{
					{Text: "⚡ Interrupt & Run Now", CallbackData: "queue:run_now"},
					{Text: "🗑️ Clear Queue", CallbackData: "queue:clear"},
				},
			},
		}
		qMsg := fmt.Sprintf("📥 <b>Message Queued (#%d):</b>\nThe bot is currently processing a task. Your message will start automatically once the current task finishes.", qLen)
		bot.SendMessageWithMarkup(chatID, qMsg, qMarkup, "HTML")
		return
	}

	if !isBackground {
		state.IsBusy = true
		state.TaskStartTime = time.Now()
		state.ActiveStep = "Starting..."
		state.ActiveThought = ""
		state.LastUserPrompt = content
	}

	convID := state.ConversationID
	contFlag := state.Continue
	workDir := state.WorkingDir

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !isBackground {
		state.CancelFunc = cancel
	}
	mu.Unlock()

	defer func() {
		if !isBackground {
			mu.Lock()
			state.IsBusy = false
			state.ActiveStep = ""
			state.ActiveThought = ""
			state.CancelFunc = nil
			state.Cmd = nil
			var nextTask *QueuedMessage
			if len(state.Queue) > 0 {
				next := state.Queue[0]
				state.Queue = state.Queue[1:]
				nextTask = &next
			}
			mu.Unlock()
			if nextTask != nil {
				go executeAgy(bot, userID, nextTask.ChatID, nextTask.Content, nextTask.IsBackground)
			}
		}
	}()

	liveCancelMarkup := &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🛑 Cancel Task", CallbackData: "action:cancel"},
			},
		},
	}

	// Send initial status message WITHOUT ReplyKeyboardMarkup so Telegram allows EditMessageText!
	initText := "⏳ <i>Antigravity is starting...</i>"
	if isBackground {
		initText = "⏳ <b>[Background]</b> Task initiated..."
	}
	statusMsg, err := bot.SendMessageWithMarkup(chatID, initText, liveCancelMarkup, "HTML")
	statusMsgID := int64(0)
	if err == nil && statusMsg != nil {
		statusMsgID = statusMsg.MessageID
	}

	// Prepare arguments with deduplicated sandbox roots
	uploadsDir := getUploadsDir()
	args := []string{
		"--sandbox",
	}
	addedDirs := make(map[string]bool)
	addSandboxDir := func(d string) {
		clean := filepath.Clean(d)
		if clean != "" && !addedDirs[clean] {
			addedDirs[clean] = true
			args = append(args, "--add-dir", clean)
		}
	}
	addSandboxDir(workDir)
	addSandboxDir(uploadsDir)
	for _, ad := range allowedDirs {
		addSandboxDir(ad)
	}
	args = append(args,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
	)

	if isBackground {
		args = append(args, "--print-timeout", "720h")
	}

	if convID != "" {
		args = append(args, "--conversation", convID)
	} else if contFlag {
		args = append(args, "-c")
	}

	activeModel := getActiveModel()
	if activeModel != "" {
		args = append(args, "--model", activeModel)
	}

	sysPrompt := "\n\n(System rule: Avoid markdown tables. Use bold/italic bullet points for mobile readability. Be concise, clear, and direct. VERY IMPORTANT: Enclose your final user-facing response strictly between <BOT_REPLY> and </BOT_REPLY> tags. Keep internal thoughts or logs outside these tags. If you generate a file, PDF, report, archive, or image for the user, or if the user asks you to send a file, include <SEND_FILE>/path/to/file</SEND_FILE> inside <BOT_REPLY> so the bot automatically sends it as a Telegram attachment.)"
	args = append(args, "-p", content+sysPrompt)

	log.Printf("Executing for %d in %s: %s (conv: %s, model: %s)", userID, workDir, cmdPath, convID, activeModel)

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	cmd.Dir = workDir
	// Put child process in its own process group so /cancel kills the entire process tree
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	mu.Lock()
	state.Cmd = cmd
	mu.Unlock()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		bot.SendMessage(chatID, fmt.Sprintf("❌ Stdout error: %v", err))
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		bot.SendMessage(chatID, fmt.Sprintf("❌ Stderr error: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		bot.SendMessage(chatID, fmt.Sprintf("❌ Failed to start process: %v", err))
		return
	}

	startTime := time.Now()

	// Continuous typing heartbeat
	stopTyping := make(chan struct{})
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		_ = bot.SendChatAction(chatID, "typing")
		for {
			select {
			case <-stopTyping:
				return
			case <-ticker.C:
				_ = bot.SendChatAction(chatID, "typing")
			}
		}
	}()
	defer close(stopTyping)

	var finalResponse string
	var capturedConvID string
	var rawOutput strings.Builder
	var lastStatusUpdate time.Time
	var statusMu sync.Mutex
	var stepHistory []string
	var currentActiveStep string
	var currentThought string
	var lastSentStatus string
	var capturedAskOptions []string

	renderLiveSteps := func(preparing bool) string {
		var sb strings.Builder
		elapsedSec := int(time.Since(startTime).Seconds())
		if elapsedSec > 0 {
			sb.WriteString(fmt.Sprintf("⚙️ <b>Live Progress</b> <i>(%ds)</i>:\n\n", elapsedSec))
		} else {
			sb.WriteString("⚙️ <b>Live Progress:</b>\n\n")
		}
		startIdx := 0
		if len(stepHistory) > 6 {
			startIdx = len(stepHistory) - 6
			sb.WriteString(fmt.Sprintf("<i>... (%d earlier steps completed)</i>\n", startIdx))
		}
		for _, s := range stepHistory[startIdx:] {
			sb.WriteString("✅ " + s + "\n")
		}
		if preparing {
			sb.WriteString("\n✍️ <i>Preparing final response...</i>")
		} else if currentActiveStep != "" {
			sb.WriteString("\n⚡ <b>Active:</b> " + currentActiveStep)
		} else {
			sb.WriteString("🧠 <i>Thinking...</i>")
		}
		if currentThought != "" && !preparing {
			sb.WriteString(fmt.Sprintf("\n💭 <i>%s</i>", escapeTelegram(currentThought)))
		}
		return sb.String()
	}

	updateStatus := func(newStatus string) {
		statusMu.Lock()
		if statusMsgID == 0 || newStatus == lastSentStatus {
			statusMu.Unlock()
			return
		}
		if time.Since(lastStatusUpdate) < 900*time.Millisecond {
			statusMu.Unlock()
			return
		}
		lastStatusUpdate = time.Now()
		lastSentStatus = newStatus
		stepSnap := currentActiveStep
		thoughtSnap := currentThought
		statusMu.Unlock()

		if !isBackground {
			mu.Lock()
			state.ActiveStep = stepSnap
			state.ActiveThought = thoughtSnap
			mu.Unlock()
		}

		if _, err := bot.EditMessageText(chatID, statusMsgID, newStatus, liveCancelMarkup, "HTML"); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			log.Printf("updateStatus EditMessageText error: %v", err)
		}
	}

	// Real-time transcript.jsonl watcher (captures exact thoughts and tool calls as agy writes them)
	var initTranscriptPath string
	var transcriptOffset int64
	if convID != "" {
		initTranscriptPath = getTranscriptPathForConv(convID)
		if info, err := os.Stat(initTranscriptPath); err == nil {
			transcriptOffset = info.Size()
		}
	}

	stopWatcher := make(chan struct{})
	go func() {
		tPath := initTranscriptPath
		offset := transcriptOffset
		ticker := time.NewTicker(800 * time.Millisecond)
		defer ticker.Stop()

		switchedToThinking := false
		for {
			select {
			case <-stopWatcher:
				return
			case <-ticker.C:
				if tPath == "" {
					statusMu.Lock()
					cid := capturedConvID
					statusMu.Unlock()
					if cid != "" {
						tPath = getTranscriptPathForConv(cid)
					} else {
						foundID, foundPath := findNewestTranscriptPath(startTime)
						if foundPath != "" {
							tPath = foundPath
							statusMu.Lock()
							if capturedConvID == "" {
								capturedConvID = foundID
							}
							statusMu.Unlock()
						}
					}
				}

				updated := false
				if tPath != "" {
					if f, err := os.Open(tPath); err == nil {
						if info, err := f.Stat(); err == nil && info.Size() > offset {
							_, _ = f.Seek(offset, 0)
							sc := bufio.NewScanner(f)
							buf := make([]byte, 512*1024)
							sc.Buffer(buf, 5*1024*1024)
							for sc.Scan() {
								line := sc.Text()
								if !strings.HasPrefix(line, "{") {
									continue
								}
								var tStep struct {
									Type      string `json:"type"`
									Thinking  string `json:"thinking"`
									ToolCalls []struct {
										Name string                 `json:"name"`
										Args map[string]interface{} `json:"args"`
									} `json:"tool_calls"`
								}
								if json.Unmarshal([]byte(line), &tStep) == nil && tStep.Type == "PLANNER_RESPONSE" {
									statusMu.Lock()
									if tStep.Thinking != "" {
										if th := extractThinkingSnippet(tStep.Thinking); th != "" {
											currentThought = th
											updated = true
										}
									}
									for _, tc := range tStep.ToolCalls {
										if tc.Name == "" {
											continue
										}
										if tc.Name == "ask_question" {
											if opts := extractAskQuestionOptions(tc.Args); len(opts) > 0 {
												capturedAskOptions = opts
											}
										}
										label := formatTranscriptToolCall(tc.Name, tc.Args)
										if currentActiveStep != "" && currentActiveStep != label {
											stepHistory = append(stepHistory, currentActiveStep)
										}
										currentActiveStep = label
										updated = true
									}
									statusMu.Unlock()
								}
							}
							offset = info.Size()
						}
						_ = f.Close()
					}
				}

				statusMu.Lock()
				sinceLastUpdate := time.Since(lastStatusUpdate)
				statusMu.Unlock()

				if updated || (switchedToThinking && sinceLastUpdate >= 5*time.Second) {
					statusMu.Lock()
					progressText := renderLiveSteps(false)
					statusMu.Unlock()
					updateStatus(progressText)
				} else if !switchedToThinking && time.Since(startTime) > 1500*time.Millisecond {
					switchedToThinking = true
					statusMu.Lock()
					progressText := renderLiveSteps(false)
					statusMu.Unlock()
					updateStatus(progressText)
				}
			}
		}
	}()
	defer close(stopWatcher)

	var streamWg sync.WaitGroup
	streamWg.Add(2)

	// Read stdout stream
	go func() {
		defer streamWg.Done()
		scanner := bufio.NewScanner(stdout)
		// Increase buffer size for large NDJSON lines
		buf := make([]byte, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			rawOutput.WriteString(line + "\n")

			if !strings.HasPrefix(line, "{") {
				continue
			}

			var payload EventPayload
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				continue
			}

			if payload.ConversationID != "" {
				statusMu.Lock()
				capturedConvID = payload.ConversationID
				statusMu.Unlock()
			}

			if payload.Event == "init" {
				statusMu.Lock()
				progressText := renderLiveSteps(false)
				statusMu.Unlock()
				updateStatus(progressText)
			} else if payload.Event == "step_update" && payload.StepUpdate != nil {
				su := payload.StepUpdate
				if su.State == "ACTIVE" || su.State == "STATE_ACTIVE" {
					if su.StepType == "tool" || su.ToolName != "" || su.ToolInfo.Name != "" {
						toolName := su.ToolName
						if toolName == "" && su.ToolInfo.Name != "" {
							toolName = su.ToolInfo.Name
						}
						stepLabel := formatTranscriptToolCall(toolName, su.ToolInfo.Parameters)

						statusMu.Lock()
						if currentActiveStep != "" && currentActiveStep != stepLabel {
							stepHistory = append(stepHistory, currentActiveStep)
						}
						currentActiveStep = stepLabel
						progressText := renderLiveSteps(false)
						statusMu.Unlock()

						updateStatus(progressText)
					} else if su.StepType == "agent_response" || su.StepType == "planner_response" {
						statusMu.Lock()
						if currentActiveStep != "" {
							stepHistory = append(stepHistory, currentActiveStep)
							currentActiveStep = ""
						}
						progressText := renderLiveSteps(true)
						statusMu.Unlock()

						updateStatus(progressText)
					}
				}
			} else if payload.Event == "result" && payload.Result != nil {
				finalResponse = payload.Result.Response
				if payload.Result.ConversationID != "" {
					statusMu.Lock()
					capturedConvID = payload.Result.ConversationID
					statusMu.Unlock()
				}
			}
		}
	}()

	// Read stderr in background
	var stderrOutput strings.Builder
	go func() {
		defer streamWg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			stderrOutput.WriteString(scanner.Text() + "\n")
		}
	}()

	streamWg.Wait()
	err = cmd.Wait()

	// Handle cancellation
	if ctx.Err() != nil {
		if statusMsgID != 0 {
			retryKb := &InlineKeyboardMarkup{
				InlineKeyboard: [][]InlineKeyboardButton{
					{{Text: "🔄 Retry Last Prompt", CallbackData: "action:retry"}},
				},
			}
			bot.EditMessageText(chatID, statusMsgID, "🛑 <b>Task cancelled.</b>", retryKb, "HTML")
		}
		return
	}

	// Update and persist conversation ID
	statusMu.Lock()
	finalConvID := capturedConvID
	statusMu.Unlock()
	if finalConvID != "" {
		mu.Lock()
		state.ConversationID = finalConvID
		state.Continue = true
		state.LastActive = time.Now()
		mu.Unlock()
		saveSessions()
	}

	resultText := finalResponse
	if resultText == "" {
		resultText = rawOutput.String()
	}

	// Extract content from <BOT_REPLY> tag
	re := regexp.MustCompile(`(?s)<BOT_REPLY>(.*?)</BOT_REPLY>`)
	matches := re.FindAllStringSubmatch(resultText, -1)
	if len(matches) > 0 {
		resultText = matches[len(matches)-1][1]
	} else if resultText != "" {
		lines := strings.Split(resultText, "\n")
		var filtered []string
		for _, line := range lines {
			lower := strings.ToLower(strings.TrimSpace(line))
			if strings.HasPrefix(lower, "i will ") ||
				strings.HasPrefix(lower, "i am going to ") ||
				strings.HasPrefix(lower, "i need to ") ||
				strings.HasPrefix(lower, "let me ") {
				continue
			}
			filtered = append(filtered, line)
		}
		resultText = strings.Join(filtered, "\n")
	}

	// Extract any <SEND_FILE>...</SEND_FILE> tags for automatic file/PDF/image delivery
	reSendFile := regexp.MustCompile(`(?s)<SEND_FILE>\s*(.*?)\s*</SEND_FILE>`)
	fileMatches := reSendFile.FindAllStringSubmatch(resultText, -1)
	var filesToSend []string
	for _, fm := range fileMatches {
		if len(fm) > 1 && strings.TrimSpace(fm[1]) != "" {
			filesToSend = append(filesToSend, strings.TrimSpace(fm[1]))
		}
	}
	resultText = strings.TrimSpace(reSendFile.ReplaceAllString(resultText, ""))

	if resultText == "" {
		if err != nil {
			resultText = fmt.Sprintf("Command failed with error: %v\n\n%s", err, stderrOutput.String())
		} else {
			resultText = "Task completed."
		}
	}

	elapsed := time.Since(startTime)
	durStr := fmt.Sprintf("%.1fs", elapsed.Seconds())
	if elapsed > time.Minute {
		durStr = fmt.Sprintf("%dm %ds", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	}

	mu.Lock()
	state.TurnsCompleted++
	state.LastTaskDuration = durStr
	mu.Unlock()
	saveSessions()

	// Update live status message to completed (preserving step history and removing Cancel button)
	if statusMsgID != 0 {
		statusMu.Lock()
		if currentActiveStep != "" {
			stepHistory = append(stepHistory, currentActiveStep)
			currentActiveStep = ""
		}
		var doneSb strings.Builder
		if len(stepHistory) > 0 {
			startIdx := 0
			if len(stepHistory) > 6 {
				startIdx = len(stepHistory) - 6
				doneSb.WriteString(fmt.Sprintf("<i>... (%d earlier steps completed)</i>\n", startIdx))
			}
			for _, s := range stepHistory[startIdx:] {
				doneSb.WriteString("✅ " + s + "\n")
			}
			doneSb.WriteString("\n")
		}
		doneSb.WriteString(fmt.Sprintf("✅ <b>Task complete</b> <i>(took %s)</i>", durStr))
		statusMu.Unlock()
		if _, err := bot.EditMessageText(chatID, statusMsgID, doneSb.String(), nil, "HTML"); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			log.Printf("final status EditMessageText error: %v", err)
		}
	}

	// Format with rock-solid Telegram HTML converter
	htmlContent := FormatTelegramHTML(resultText)

	// Append dynamic HTML footer AFTER markdown-to-HTML conversion so tags are not escaped
	footer := fmt.Sprintf("\n\n┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈\n🤖 <i>%s</i> | 📂 <code>%s</code>", escapeTelegram(activeModel), escapeTelegram(shortenHomeDir(workDir)))
	htmlContent += footer

	chunks := SplitTelegramHTML(htmlContent, 3800)

	// Send final response as NEW message (triggers Telegram push notification & sound on phone!)
	if len(chunks) > 0 {
		replyMarkup := getDefaultReplyKeyboard()
		_, err := bot.SendMessageWithMarkup(chatID, chunks[0], replyMarkup, "HTML")
		if err != nil {
			log.Printf("SendMessage HTML parse error: %v, falling back to plain text", err)
			_, _ = bot.SendMessageWithMarkup(chatID, StripHTML(chunks[0]), replyMarkup)
		}

		// Send additional chunks if message was split
		for i := 1; i < len(chunks); i++ {
			_, err := bot.SendMessage(chatID, chunks[i], "HTML")
			if err != nil {
				log.Printf("SendMessage chunk HTML parse error: %v, falling back to plain text", err)
				_, _ = bot.SendMessage(chatID, StripHTML(chunks[i]))
			}
		}

		// If the agent invoked ask_question with choices, present them as 1-tap inline buttons
		statusMu.Lock()
		askOpts := append([]string(nil), capturedAskOptions...)
		statusMu.Unlock()
		if len(askOpts) > 0 {
			var optRows [][]InlineKeyboardButton
			for i, opt := range askOpts {
				if i >= 8 {
					break
				}
				btnLabel := opt
				if len([]rune(btnLabel)) > 36 {
					btnLabel = string([]rune(btnLabel)[:34]) + ".."
				}
				optRows = append(optRows, []InlineKeyboardButton{
					{
						Text:         "👉 " + btnLabel,
						CallbackData: registerAskOption(opt),
					},
				})
			}
			_, _ = bot.SendMessageWithMarkup(chatID, "❓ <b>Quick Reply Options:</b>\n<i>Tap an option below or type a custom response:</i>", &InlineKeyboardMarkup{InlineKeyboard: optRows}, "HTML")
		}
	}

	// Automatically send any files/images requested via <SEND_FILE>
	for _, rawPath := range filesToSend {
		var targetPath string
		if filepath.IsAbs(rawPath) {
			targetPath = filepath.Clean(rawPath)
		} else {
			targetPath = filepath.Clean(filepath.Join(workDir, rawPath))
		}

		if !isPathAllowed(targetPath) {
			log.Printf("Auto-send blocked for path outside sandbox: %s", targetPath)
			continue
		}

		info, err := os.Stat(targetPath)
		if err != nil || info.IsDir() {
			log.Printf("Auto-send file not found or is dir: %s", targetPath)
			continue
		}

		caption := fmt.Sprintf("📎 <code>%s</code> (%s)", escapeTelegram(filepath.Base(targetPath)), formatByteSize(info.Size()))
		if _, err := bot.SendFileAuto(chatID, targetPath, caption); err != nil {
			log.Printf("Failed to auto-send file %s: %v", targetPath, err)
		}
	}
}
