package main

import (
	"bytes"
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
	"unicode/utf8"

	"github.com/joho/godotenv"
)

// SessionState stores user configuration and memory
type SessionState struct {
	WorkingDir string
	Continue   bool
	IsBusy     bool
	LastActive time.Time
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
		// Fallback to current directory if not specified
		defaultDir, _ = os.Getwd()
		allowedDirs = append(allowedDirs, defaultDir)
	}

	log.Printf("Sandbox root: %v", allowedDirs)

	bot := NewTelegramBot(token)

	// Set Bot Commands
	commands := []map[string]string{
		{"command": "new", "description": "Bắt đầu một hội thoại mới với Antigravity (dọn dẹp phiên hiện tại)"},
		{"command": "dir", "description": "Đổi thư mục làm việc (Sandbox) - VD: /dir /path/to/dir"},
		{"command": "goal", "description": "Thực hiện nhiệm vụ triệt để không ngắt quãng (chạy ẩn)"},
		{"command": "grill_me", "description": "Hỏi ngược lại để làm rõ yêu cầu trước khi làm"},
		{"command": "browser", "description": "Ép bot sử dụng trình duyệt để duyệt web"},
		{"command": "schedule", "description": "Lên lịch hoặc hẹn giờ thực hiện tác vụ"},
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

	log.Println("Bot is now running. Press CTRL-C to exit.")

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
			WorkingDir: defaultDir,
			Continue:   false,
			IsBusy:     false,
			LastActive: time.Now(),
		}
		sessions[userID] = state
	}
	return state
}

func isAuthorized(userID int64, chatID int64) bool {
	// Kiểm tra Chat ID nếu có cấu hình
	if len(allowedChatIDs) > 0 && chatID != userID { // ChatID != UserID có nghĩa là group
		if !allowedChatIDs[chatID] {
			return false
		}
	}

	// Nếu không cấu hình ALLOWED list nào cho User, coi như public.
	if len(allowedUserIDs) == 0 {
		return true
	}

	if allowedUserIDs[userID] {
		return true
	}

	return false
}

// Hàm kiểm tra xem thư mục con có thực sự nằm trong thư mục cha không (phòng chống path traversal)
func isSubDir(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

func handleMessage(bot *TelegramBot, m *Message) {
	if m.From == nil || m.From.IsBot {
		return
	}

	log.Printf("📥 Received message from User %d in Chat %d: %q", m.From.ID, m.Chat.ID, m.Text)

	if !isAuthorized(m.From.ID, m.Chat.ID) {
		log.Printf("⛔ Unauthorized message ignored from User %d.", m.From.ID)
		return
	}

	content := strings.TrimSpace(m.Text)
	if content == "" {
		return
	}

	if botUsername != "" {
		mention := "@" + botUsername
		isGroup := m.Chat.Type == "group" || m.Chat.Type == "supergroup"
		
		if isGroup && !strings.HasPrefix(content, "/") && !strings.Contains(content, mention) {
			return // Bỏ qua tin nhắn không tag bot trong nhóm
		}
		
		// Xóa tag ra khỏi nội dung để không làm rối lệnh hoặc LLM
		content = strings.Replace(content, mention, "", -1)
		content = strings.TrimSpace(content)
		if content == "" {
			return
		}
	} else if (m.Chat.Type == "group" || m.Chat.Type == "supergroup") && !strings.HasPrefix(content, "/") {
		return // Fallback nếu không lấy được username: chỉ nhận lệnh /
	}

	// Xử lý các lệnh Slash
	if strings.HasPrefix(content, "/") {
		parts := strings.SplitN(content, " ", 2)
		cmd := strings.ToLower(parts[0])
		args := ""
		if len(parts) > 1 {
			args = strings.TrimSpace(parts[1])
		}

		state := getSession(m.From.ID)

		switch cmd {
		case "/goal", "/grill_me", "/browser":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "❌ Bạn cần cung cấp tham số cho lệnh này.")
				return
			}
			// Chuyển /grill_me thành /grill-me cho agy
			internalCmd := cmd
			if cmd == "/grill_me" {
				internalCmd = "/grill-me"
			}
			
			fullPrompt := fmt.Sprintf("%s %s", internalCmd, args)
			bot.SendMessage(m.Chat.ID, fmt.Sprintf("⏳ **[%s]** Đã nhận lệnh, đang xử lý ngầm (có thể mất vài phút)...", cmd))
			go executeAgy(bot, m.From.ID, m.Chat.ID, fullPrompt, true)
			return

		case "/schedule":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "❌ Bạn cần cung cấp thời gian và nhiệm vụ (VD: /schedule in 5m say hello).")
				return
			}
			fullPrompt := fmt.Sprintf("/schedule %s", args)
			bot.SendMessage(m.Chat.ID, "⏳ **[/schedule]** Đang thiết lập lịch trình...")
			go executeAgy(bot, m.From.ID, m.Chat.ID, fullPrompt, true)
			return

		case "/new":
			mu.Lock()
			state.Continue = false
			mu.Unlock()
			bot.SendMessage(m.Chat.ID, "🔄 Hệ thống đã sẵn sàng khởi tạo hội thoại mới ở tin nhắn tiếp theo!")
			return

		case "/dir":
			if args == "" {
				bot.SendMessage(m.Chat.ID, "❌ Bạn cần cung cấp đường dẫn thư mục.")
				return
			}
			reqDir := args
			mu.Lock()
			currentDir := state.WorkingDir
			mu.Unlock()

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

			var responseContent string
			if !isAllowed {
				responseContent = "⛔ Thư mục này không nằm trong danh sách Sandbox được phép (ALLOWED_DIRS)."
			} else {
				info, err := os.Stat(targetDir)
				if os.IsNotExist(err) {
					err = os.MkdirAll(targetDir, 0755)
					if err != nil {
						responseContent = fmt.Sprintf("❌ Không thể tạo thư mục mới `%s`:\n%v", targetDir, err)
					} else {
						responseContent = fmt.Sprintf("📂 Đã tạo mới và chuyển tới thư mục: `%s`", targetDir)
						mu.Lock()
						state.WorkingDir = targetDir
						mu.Unlock()
					}
				} else if err != nil {
					responseContent = fmt.Sprintf("❌ Lỗi khi đọc đường dẫn: %v", err)
				} else if !info.IsDir() {
					responseContent = fmt.Sprintf("❌ `%s` là một file, không phải là thư mục!", targetDir)
				} else {
					responseContent = fmt.Sprintf("📂 Đã đổi thư mục làm việc sang: `%s`", targetDir)
					mu.Lock()
					state.WorkingDir = targetDir
					mu.Unlock()
				}
			}
			bot.SendMessage(m.Chat.ID, responseContent)
			return
		}
	}

	// Normal text message
	bot.SendChatAction(m.Chat.ID, "typing")
	executeAgy(bot, m.From.ID, m.Chat.ID, content, false)
}

func executeAgy(bot *TelegramBot, userID int64, chatID int64, content string, isBackground bool) {
	state := getSession(userID)

	cmdPath := os.Getenv("ANTIGRAVITY_CMD")
	if cmdPath == "" {
		cmdPath = "agy" // default binary
	}

	mu.Lock()
	// Tự động reset nếu quá 2 tiếng không tương tác (chỉ áp dụng cho normal chat)
	if !isBackground && time.Since(state.LastActive) > 2*time.Hour {
		state.Continue = false
	}
	if !isBackground {
		state.LastActive = time.Now()
	}

	if !isBackground && state.IsBusy {
		mu.Unlock()
		bot.SendMessage(chatID, "⏳ **Hệ thống đang bận:** Bot đang xử lý một yêu cầu trước đó của bạn. Vui lòng chờ lệnh hoàn tất trước khi gửi yêu cầu mới.")
		return
	}

	if !isBackground {
		state.IsBusy = true
	}

	continueFlag := state.Continue

	if isBackground {
		state.Continue = false
	}
	mu.Unlock()

	if !isBackground {
		defer func() {
			mu.Lock()
			state.IsBusy = false
			mu.Unlock()
		}()
	}

	// Chuẩn bị tham số cho agy
	args := []string{
		"--sandbox",
		"--add-dir", state.WorkingDir,
		"--dangerously-skip-permissions",
		"--print-timeout", "720h", // Mở rộng thời gian chờ lên tới 30 ngày cho các lệnh /goal, /schedule
	}

	if continueFlag {
		args = append(args, "-c")
	}

	// Thêm nội dung prompt vào cuối cùng cấu hình ẩn
	sysPrompt := "\n\n(Lưu ý từ hệ thống: Tuyệt đối không sử dụng định dạng bảng (table). Hãy trình bày dưới dạng danh sách (bullet list) có in đậm/in nghiêng để dễ đọc trên điện thoại. Hãy giữ nội dung ngắn gọn và súc tích nhất có thể. QUAN TRỌNG: Bạn BẮT BUỘC phải bọc toàn bộ nội dung câu trả lời cuối cùng dành cho người dùng vào giữa cặp thẻ <BOT_REPLY> và </BOT_REPLY>. Bất kỳ suy nghĩ hoặc phần log nào của bạn phải nằm ngoài thẻ này để hệ thống có thể lọc bỏ.)"
	args = append(args, "-p", content+sysPrompt)

	log.Printf("Executing for %d in %s: %s", userID, state.WorkingDir, cmdPath)

	cmd := exec.Command(cmdPath, args...)
	cmd.Dir = state.WorkingDir

	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	// Bắt đầu vòng lặp gửi "typing..." liên tục để user biết bot vẫn đang sống
	done := make(chan bool)
	if !isBackground {
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(4 * time.Second):
					bot.SendChatAction(chatID, "typing")
				}
			}
		}()
	}

	err := cmd.Run()
	
	if !isBackground {
		done <- true // Dừng vòng lặp typing
	}
	
	result := out.String()
	errStr := errOut.String()

	if errStr != "" {
		result += "\n[STDERR]\n" + errStr
	}

	// Lọc kết quả để lấy nội dung trong thẻ <BOT_REPLY> (nếu có)
	re := regexp.MustCompile(`(?s)<BOT_REPLY>(.*?)</BOT_REPLY>`)
	matches := re.FindAllStringSubmatch(result, -1)
	if len(matches) > 0 {
		result = matches[len(matches)-1][1]
	} else if result != "" {
		// Fallback: Xóa các dòng điển hình của thoughts nếu model quên in thẻ
		lines := strings.Split(result, "\n")
		var filtered []string
		for _, line := range lines {
			lowerLine := strings.ToLower(strings.TrimSpace(line))
			if strings.HasPrefix(lowerLine, "i will ") ||
				strings.HasPrefix(lowerLine, "i am going to ") ||
				strings.HasPrefix(lowerLine, "i need to ") ||
				strings.HasPrefix(lowerLine, "let me ") {
				continue
			}
			filtered = append(filtered, line)
		}
		result = strings.Join(filtered, "\n")
	}

	result = strings.TrimSpace(result)
	if result == "" {
		if err != nil {
			result = fmt.Sprintf("Lệnh hoàn tất với lỗi: %v\n\n[Raw Output]:\n%s", err, out.String())
		} else {
			result = "Lệnh hoàn tất."
		}
	}
	
	if err == nil && !isBackground {
		mu.Lock()
		state.Continue = true
		mu.Unlock()
	}

	sendLongMessage(bot, chatID, result)
}

func sendLongMessage(bot *TelegramBot, chatID int64, text string) {
	// Format văn bản Markdown sang HTML cho Telegram
	text = formatTelegramHTML(text)

	// Telegram limits messages to 4096 chars. We use 4000 to be safe.
	const maxLen = 4000
	if utf8.RuneCountInString(text) <= maxLen {
		if err := bot.SendMessage(chatID, text, "HTML"); err != nil {
			log.Printf("Error sending message: %v", err)
			// Nếu lỗi do HTML parse (ví dụ unclosed tag), gửi lại dạng text thường
			bot.SendMessage(chatID, text)
		}
		return
	}

	// Split by chunks
	runes := []rune(text)
	for i := 0; i < len(runes); i += maxLen {
		end := i + maxLen
		if end > len(runes) {
			end = len(runes)
		}
		err := bot.SendMessage(chatID, string(runes[i:end]), "HTML")
		if err != nil {
			log.Printf("Error sending message chunk: %v", err)
			bot.SendMessage(chatID, string(runes[i:end]))
		}
	}
}

// formatTelegramHTML converts standard markdown to Telegram-compatible HTML
func formatTelegramHTML(text string) string {
	// 1. Convert < and > outside of tags (basic escaping to prevent telegram api error)
	// We do a simple replace first. Note: If agy returns actual HTML, this will escape it.
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")

	// 2. Convert code blocks ```...``` to <pre>...</pre>
	rePre := regexp.MustCompile("(?s)```[a-zA-Z]*\n(.*?)\n?```")
	text = rePre.ReplaceAllString(text, "<pre>$1</pre>")

	// 3. Convert inline code `...` to <code>...</code>
	reCode := regexp.MustCompile("`([^`]+)`")
	text = reCode.ReplaceAllString(text, "<code>$1</code>")

	// 4. Convert bold **...** to <b>...</b>
	reBold := regexp.MustCompile(`\*\*(.+?)\*\*`)
	text = reBold.ReplaceAllString(text, "<b>$1</b>")

	// 5. Convert italic *...* to <i>...</i>
	// Careful not to match bullet points (which have a space after *)
	reItalic := regexp.MustCompile(`(?m)(^|[^\*])\*([^\*\s][^\*]*[^\*\s]|[^\*\s])\*([^\*]|$)`)
	text = reItalic.ReplaceAllString(text, "$1<i>$2</i>$3")

	return text
}
