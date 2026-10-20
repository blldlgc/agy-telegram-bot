package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	WorkingDir     string
	ConversationID string
	Continue       bool
	IsBusy         bool
	CancelFunc     context.CancelFunc
	Cmd            *exec.Cmd
	LastActive     time.Time
	Queue          []QueuedMessage
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
	_, _ = buildDirKeyboard(defaultDir, allowedDirs)
	_, _ = buildRootsKeyboard(allowedDirs)

	bot := NewTelegramBot(token)
	_ = bot.DeleteWebhook()

	// Set Bot Commands
	commands := []map[string]string{
		{"command": "new", "description": "Start a fresh new conversation"},
		{"command": "resume", "description": "List and resume previous conversations"},
		{"command": "dir", "description": "Select or change working directory (Sandbox)"},
		{"command": "model", "description": "Select and switch active AI model"},
		{"command": "git", "description": "Interactive Git status, diff, log, and pull dashboard"},
		{"command": "status", "description": "Show current model, directory, and status"},
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

func isSubDir(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
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
	case data == "resume:new":
		mu.Lock()
		state.ConversationID = ""
		state.Continue = false
		mu.Unlock()
		_ = bot.AnswerCallbackQuery(cq.ID, "🔄 Started new conversation.")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, "🔄 <b>New Session:</b> Conversation history cleared. Your next message will start a fresh session.", nil, "HTML")
		}

	case strings.HasPrefix(data, "resume:"):
		convID := strings.TrimPrefix(data, "resume:")
		mu.Lock()
		state.ConversationID = convID
		state.Continue = true
		mu.Unlock()
		_ = bot.AnswerCallbackQuery(cq.ID, "✅ Switched to conversation.")
		if chatID != 0 && msgID != 0 {
			resp := formatResumeConfirmation(convID)
			bot.EditMessageText(chatID, msgID, resp, nil, "HTML")
		}

	case data == "dir:open":
		mu.RLock()
		curDir := state.WorkingDir
		mu.RUnlock()
		text, markup := buildDirKeyboard(curDir, allowedDirs)
		_ = bot.AnswerCallbackQuery(cq.ID, "📂 Directory menu")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case data == "dir:roots":
		text, markup := buildRootsKeyboard(allowedDirs)
		_ = bot.AnswerCallbackQuery(cq.ID, "Root folders")
		if chatID != 0 && msgID != 0 {
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
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
		mu.Unlock()

		_ = bot.AnswerCallbackQuery(cq.ID, fmt.Sprintf("📂 %s", filepath.Base(targetDir)))
		if chatID != 0 && msgID != 0 {
			text, markup := buildDirKeyboard(targetDir, allowedDirs)
			bot.EditMessageText(chatID, msgID, text, markup, "HTML")
		}

	case strings.HasPrefix(data, "model:"):
		modelID := strings.TrimPrefix(data, "model:")
		err := setActiveModel(modelID)
		if err != nil {
			_ = bot.AnswerCallbackQuery(cq.ID, "❌ Failed to change model.")
		} else {
			_ = bot.AnswerCallbackQuery(cq.ID, fmt.Sprintf("✅ Model: %s", modelID))
			if chatID != 0 && msgID != 0 {
				resp := fmt.Sprintf("🧠 <b>Active AI Model Changed:</b>\n<code>%s</code>\n\nSubsequent requests will run with this model.", modelID)
				bot.EditMessageText(chatID, msgID, resp, nil, "HTML")
			}
		}

	case data == "action:continue":
		_ = bot.AnswerCallbackQuery(cq.ID, "Continuing...")
		go executeAgy(bot, userID, chatID, "Please continue where you left off.", false)

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
		mu.Lock()
		busy := state.IsBusy
		cancel := state.CancelFunc
		cmdProc := state.Cmd
		mu.Unlock()
		if busy && cancel != nil {
			cancel()
			if cmdProc != nil && cmdProc.Process != nil {
				_ = cmdProc.Process.Kill()
			}
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
		args := ""
		if len(parts) > 1 {
			args = strings.TrimSpace(parts[1])
		}

		state := getSession(m.From.ID)

		switch cmd {
		case "/start", "/help":
			helpText := `👋 <b>Google Antigravity Telegram Assistant</b>

Available commands:
• <b>/model</b> — Select & switch active AI model
• <b>/new</b> — Start a fresh new session
• <b>/resume</b> — List and switch to previous conversations
• <b>/dir</b> — Browse and change working directory
• <b>/git</b> — Interactive Git status, diff, log & pull dashboard
• <b>/status</b> — View active model, directory, and session info
• <b>/getfile &lt;path&gt;</b> — Download a file from server
• <b>/cancel</b> — Immediately stop the running task
• <b>/restart</b> — Restart the bot service
• <b>/goal &lt;task&gt;</b> — Execute an autonomous deep goal
• <b>/schedule &lt;time&gt; &lt;task&gt;</b> — Schedule a delayed or recurring task

You can also send files/photos or coding questions directly!`
			bot.SendMessageWithMarkup(m.Chat.ID, helpText, getDefaultReplyKeyboard(), "HTML")
			return

		case "/restart":
			bot.SendMessage(m.Chat.ID, "🔄 <b>Restarting bot...</b> (New instance will be active in 1 second)", "HTML")
			bot.ConfirmUpdates()
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
			return

		case "/continue":
			go executeAgy(bot, m.From.ID, m.Chat.ID, "Please continue where you left off.", false)
			return

		case "/model":
			text, markup := buildModelKeyboard()
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/git":
			mu.RLock()
			curDir := state.WorkingDir
			mu.RUnlock()
			action := "status"
			if args == "diff" || args == "log" || args == "pull" {
				action = args
			}
			text, markup := buildGitDashboard(curDir, action)
			bot.SendMessageWithMarkup(m.Chat.ID, text, markup, "HTML")
			return

		case "/getfile", "/file", "/download":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "❌ Please specify a file path (e.g. <code>/getfile main.go</code>).", "HTML")
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

			isAllowed := false
			for _, ad := range allowedDirs {
				if isSubDir(ad, targetPath) {
					isAllowed = true
					break
				}
			}
			if !isAllowed {
				bot.SendMessage(m.Chat.ID, "⛔ File is outside allowed sandbox paths.")
				return
			}

			info, err := os.Stat(targetPath)
			if err != nil || info.IsDir() {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ File not found: <code>%s</code>", escapeTelegram(filepath.Base(targetPath))), "HTML")
				return
			}

			caption := fmt.Sprintf("📄 <code>%s</code> (%.1f KB)", filepath.Base(targetPath), float64(info.Size())/1024.0)
			_, err = bot.SendDocument(m.Chat.ID, targetPath, caption)
			if err != nil {
				bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to send document: %v", err))
			}
			return

		case "/status":
			mu.RLock()
			busy := state.IsBusy
			workDir := state.WorkingDir
			convID := state.ConversationID
			qCount := len(state.Queue)
			mu.RUnlock()

			model := getActiveModel()
			statusStr := "🟢 Idle"
			if busy {
				if qCount > 0 {
					statusStr = fmt.Sprintf("🟡 Running Task (%d queued)", qCount)
				} else {
					statusStr = "🟡 Running Task"
				}
			}
			convStr := "<i>New Session</i>"
			if convID != "" {
				convStr = fmt.Sprintf("<code>%s</code>", convID)
			}

			statusMsg := fmt.Sprintf(`🤖 <b>Antigravity Status:</b>

• <b>Model:</b> <i>%s</i>
• <b>Working Directory:</b> <code>%s</code>
• <b>Conversation:</b> %s
• <b>Status:</b> %s`, model, shortenHomeDir(workDir), convStr, statusStr)

			bot.SendMessage(m.Chat.ID, statusMsg, "HTML")
			return

		case "/cancel", "/stop":
			mu.Lock()
			busy := state.IsBusy
			cancel := state.CancelFunc
			cmdProc := state.Cmd
			mu.Unlock()

			if !busy || cancel == nil {
				bot.SendMessage(m.Chat.ID, "ℹ️ No task is currently running.")
				return
			}

			cancel()
			if cmdProc != nil && cmdProc.Process != nil {
				_ = cmdProc.Process.Kill()
			}
			bot.SendMessage(m.Chat.ID, "🛑 <b>Task cancelled.</b>", "HTML")
			return

		case "/resume":
			if args != "" {
				mu.Lock()
				state.ConversationID = args
				state.Continue = true
				mu.Unlock()
				bot.SendMessage(m.Chat.ID, formatResumeConfirmation(args), "HTML")
				return
			}
			text, markup, err := buildResumeKeyboard()
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
			bot.SendMessage(m.Chat.ID, "🔄 Session reset. Your next message will start a fresh conversation!")
			return

		case "/dir":
			if args == "" {
				mu.RLock()
				curDir := state.WorkingDir
				mu.RUnlock()
				text, markup := buildDirKeyboard(curDir, allowedDirs)
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
			mu.Unlock()
			bot.SendMessage(m.Chat.ID, fmt.Sprintf("📂 Working directory changed to: <code>%s</code>", shortenHomeDir(targetDir)), "HTML")
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

	// Normal text message
	executeAgy(bot, m.From.ID, m.Chat.ID, content, false)
}

// getUploadsDir returns the dedicated fixed directory under the bot folder for incoming files/photos
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

	if err := bot.DownloadFile(fileInfo.FilePath, destPath); err != nil {
		bot.SendMessage(m.Chat.ID, fmt.Sprintf("❌ Failed to download file: %v", err))
		return
	}

	ackText := fmt.Sprintf("📥 <b>File saved:</b> <code>%s</code> (%.1f KB)\n📂 Location: <code>%s</code>", saveName, float64(m.Document.FileSize)/1024.0, shortenHomeDir(destPath))

	caption := strings.TrimSpace(m.Caption)
	if caption != "" {
		bot.SendMessage(m.Chat.ID, ackText, "HTML")
		prompt := fmt.Sprintf("I uploaded a file at '%s'. %s", destPath, caption)
		executeAgy(bot, m.From.ID, m.Chat.ID, prompt, false)
	} else {
		bot.SendMessage(m.Chat.ID, ackText, "HTML")
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

	ackText := fmt.Sprintf("🖼️ <b>Photo saved:</b> <code>%s</code> (%.1f KB)\n📂 Location: <code>%s</code>", saveName, float64(bestPhoto.FileSize)/1024.0, shortenHomeDir(destPath))
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
	if !isBackground && time.Since(state.LastActive) > 2*time.Hour {
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
	}

	convID := state.ConversationID
	contFlag := state.Continue
	workDir := state.WorkingDir

	ctx, cancel := context.WithCancel(context.Background())
	if !isBackground {
		state.CancelFunc = cancel
	}
	mu.Unlock()

	defer func() {
		if !isBackground {
			mu.Lock()
			state.IsBusy = false
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

	// Send initial status message WITHOUT ReplyKeyboardMarkup so Telegram allows EditMessageText!
	initText := "⏳ <i>Antigravity is starting...</i>"
	if isBackground {
		initText = "⏳ <b>[Background]</b> Task initiated..."
	}
	statusMsg, err := bot.SendMessageWithMarkup(chatID, initText, nil, "HTML")
	statusMsgID := int64(0)
	if err == nil && statusMsg != nil {
		statusMsgID = statusMsg.MessageID
	}

	// Prepare arguments
	uploadsDir := getUploadsDir()
	args := []string{
		"--sandbox",
		"--add-dir", workDir,
		"--add-dir", uploadsDir,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
	}

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

	renderLiveSteps := func(preparing bool) string {
		var sb strings.Builder
		sb.WriteString("⚙️ <b>Live Progress:</b>\n\n")
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
		statusMu.Unlock()
		if _, err := bot.EditMessageText(chatID, statusMsgID, newStatus, nil, "HTML"); err != nil && !strings.Contains(err.Error(), "message is not modified") {
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

				if updated {
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
			bot.EditMessageText(chatID, statusMsgID, "🛑 <b>Task cancelled.</b>", nil, "HTML")
		}
		return
	}

	// Update conversation ID
	if capturedConvID != "" {
		mu.Lock()
		state.ConversationID = capturedConvID
		state.Continue = true
		mu.Unlock()
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

	// Update live status message to completed (preserving step history)
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

	// Format with new rock-solid Telegram HTML converter
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
	}

	// Automatically send any files requested via <SEND_FILE>
	homeDir, _ := os.UserHomeDir()
	brainDir := ""
	if homeDir != "" {
		brainDir = filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain")
	}

	for _, rawPath := range filesToSend {
		var targetPath string
		if filepath.IsAbs(rawPath) {
			targetPath = filepath.Clean(rawPath)
		} else {
			targetPath = filepath.Clean(filepath.Join(workDir, rawPath))
		}

		isAllowed := isSubDir(uploadsDir, targetPath) || (brainDir != "" && isSubDir(brainDir, targetPath))
		if !isAllowed {
			for _, ad := range allowedDirs {
				if isSubDir(ad, targetPath) {
					isAllowed = true
					break
				}
			}
		}
		if !isAllowed {
			log.Printf("Auto-send blocked for path outside sandbox: %s", targetPath)
			continue
		}

		info, err := os.Stat(targetPath)
		if err != nil || info.IsDir() {
			log.Printf("Auto-send file not found or is dir: %s", targetPath)
			continue
		}

		caption := fmt.Sprintf("📎 <code>%s</code> (%.1f KB)", filepath.Base(targetPath), float64(info.Size())/1024.0)
		if _, err := bot.SendDocument(chatID, targetPath, caption); err != nil {
			log.Printf("Failed to auto-send document %s: %v", targetPath, err)
		}
	}
}
