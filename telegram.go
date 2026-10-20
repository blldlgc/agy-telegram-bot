package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type TelegramBot struct {
	Token  string
	client *http.Client
	offset int64
}

func NewTelegramBot(token string) *TelegramBot {
	return &TelegramBot{
		Token: token,
		client: &http.Client{
			Timeout: 60 * time.Second, // Long polling timeout
		},
	}
}

// Structs for Telegram API
type UpdateResponse struct {
	Ok          bool     `json:"ok"`
	Result      []Update `json:"result"`
	Description string   `json:"description,omitempty"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size,omitempty"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type Audio struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Duration int    `json:"duration,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type Video struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Duration int    `json:"duration,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type Message struct {
	MessageID      int64       `json:"message_id"`
	From           *User       `json:"from"`
	Chat           *Chat       `json:"chat"`
	Text           string      `json:"text"`
	Caption        string      `json:"caption,omitempty"`
	Document       *Document   `json:"document,omitempty"`
	Photo          []PhotoSize `json:"photo,omitempty"`
	Voice          *Voice      `json:"voice,omitempty"`
	Audio          *Audio      `json:"audio,omitempty"`
	Video          *Video      `json:"video,omitempty"`
	ReplyToMessage *Message    `json:"reply_to_message,omitempty"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type KeyboardButton struct {
	Text string `json:"text"`
}

type ReplyKeyboardMarkup struct {
	Keyboard              [][]KeyboardButton `json:"keyboard"`
	ResizeKeyboard        bool               `json:"resize_keyboard"`
	IsPersistent          bool               `json:"is_persistent"`
	OneTimeKeyboard       bool               `json:"one_time_keyboard,omitempty"`
	InputFieldPlaceholder string             `json:"input_field_placeholder,omitempty"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data"`
}

type APIResponse struct {
	Ok          bool            `json:"ok"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after,omitempty"`
	} `json:"parameters,omitempty"`
}

// SetMyCommands sets the bot menu commands
func (b *TelegramBot) SetMyCommands(commands []map[string]string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/setMyCommands", b.Token)

	payload := map[string]interface{}{
		"commands": commands,
	}
	body, _ := json.Marshal(payload)

	resp, err := b.client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// DeleteWebhook deletes any existing webhook so getUpdates works
func (b *TelegramBot) DeleteWebhook() error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/deleteWebhook", b.Token)
	resp, err := b.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// GetMe fetches the bot's own info
func (b *TelegramBot) GetMe() (*User, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", b.Token)

	resp, err := b.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp struct {
		Ok     bool  `json:"ok"`
		Result *User `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}
	if !apiResp.Ok {
		return nil, fmt.Errorf("telegram API returned ok=false on getMe")
	}
	return apiResp.Result, nil
}

// ConfirmUpdates acknowledges processed updates on Telegram servers before shutdown/restart
func (b *TelegramBot) ConfirmUpdates() {
	curOffset := atomic.LoadInt64(&b.offset)
	if curOffset == 0 {
		return
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", b.Token)
	payload := map[string]interface{}{
		"offset":  curOffset,
		"timeout": 0,
		"limit":   1,
	}
	body, _ := json.Marshal(payload)
	resp, err := b.client.Post(url, "application/json", bytes.NewBuffer(body))
	if err == nil {
		resp.Body.Close()
	}
}

// GetUpdates fetches new messages via long polling
func (b *TelegramBot) GetUpdates() ([]Update, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", b.Token)

	curOffset := atomic.LoadInt64(&b.offset)
	payload := map[string]interface{}{
		"offset":  curOffset,
		"timeout": 30, // 30 seconds long polling
	}
	body, _ := json.Marshal(payload)

	resp, err := b.client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp UpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.Ok {
		return nil, fmt.Errorf("telegram API returned ok=false: %s", apiResp.Description)
	}

	// Update offset to highest update_id + 1 atomically
	for _, u := range apiResp.Result {
		if u.UpdateID >= curOffset {
			curOffset = u.UpdateID + 1
			atomic.StoreInt64(&b.offset, curOffset)
		}
	}

	return apiResp.Result, nil
}

// postMessageJSON posts a JSON request to Telegram and handles 429 rate-limit retries automatically
func (b *TelegramBot) postMessageJSON(endpoint string, payload map[string]interface{}) (*Message, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.Token, endpoint)

	for attempt := 0; attempt < 2; attempt++ {
		body, _ := json.Marshal(payload)
		resp, err := b.client.Post(url, "application/json", bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var apiResp struct {
			Ok          bool     `json:"ok"`
			ErrorCode   int      `json:"error_code"`
			Result      *Message `json:"result"`
			Description string   `json:"description"`
			Parameters  *struct {
				RetryAfter int `json:"retry_after"`
			} `json:"parameters"`
		}
		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			return nil, err
		}
		if apiResp.Ok {
			return apiResp.Result, nil
		}

		// Handle HTTP 429 rate limits gracefully
		if apiResp.ErrorCode == 429 && apiResp.Parameters != nil && apiResp.Parameters.RetryAfter > 0 && apiResp.Parameters.RetryAfter <= 15 && attempt == 0 {
			waitDur := time.Duration(apiResp.Parameters.RetryAfter) * time.Second
			log.Printf("⏳ Telegram rate limit on %s, retrying after %v...", endpoint, waitDur)
			time.Sleep(waitDur)
			continue
		}

		if !strings.Contains(apiResp.Description, "message is not modified") {
			log.Printf("⚠️ Telegram %s failed: %s", endpoint, apiResp.Description)
		}
		return nil, fmt.Errorf("telegram %s failed: %s", endpoint, apiResp.Description)
	}

	return nil, fmt.Errorf("telegram %s failed after retry", endpoint)
}

// SendMessage sends a text message to a specific chat with persistent reply keyboard
func (b *TelegramBot) SendMessage(chatID int64, text string, parseMode ...string) (*Message, error) {
	return b.SendMessageWithMarkup(chatID, text, getDefaultReplyKeyboard(), parseMode...)
}

// SendMessageWithMarkup sends a text message with optional reply markup (inline keyboard)
func (b *TelegramBot) SendMessageWithMarkup(chatID int64, text string, markup interface{}, parseMode ...string) (*Message, error) {
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if len(parseMode) > 0 && parseMode[0] != "" {
		payload["parse_mode"] = parseMode[0]
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return b.postMessageJSON("sendMessage", payload)
}

// EditMessageText edits an existing message text
func (b *TelegramBot) EditMessageText(chatID int64, messageID int64, text string, markup interface{}, parseMode ...string) (*Message, error) {
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if len(parseMode) > 0 && parseMode[0] != "" {
		payload["parse_mode"] = parseMode[0]
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return b.postMessageJSON("editMessageText", payload)
}

// DeleteMessage deletes a message
func (b *TelegramBot) DeleteMessage(chatID int64, messageID int64) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/deleteMessage", b.Token)

	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	body, _ := json.Marshal(payload)

	resp, err := b.client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// AnswerCallbackQuery answers an inline keyboard callback query
func (b *TelegramBot) AnswerCallbackQuery(callbackQueryID string, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", b.Token)

	payload := map[string]interface{}{
		"callback_query_id": callbackQueryID,
	}
	if text != "" {
		payload["text"] = text
	}
	body, _ := json.Marshal(payload)

	resp, err := b.client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// SendChatAction sends a typing or upload indicator
func (b *TelegramBot) SendChatAction(chatID int64, action string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendChatAction", b.Token)

	payload := map[string]interface{}{
		"chat_id": chatID,
		"action":  action,
	}
	body, _ := json.Marshal(payload)

	resp, err := b.client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram SendChatAction API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

type TelegramFile struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
}

// GetFile requests file path information from Telegram API
func (b *TelegramBot) GetFile(fileID string) (*TelegramFile, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", b.Token, fileID)
	resp, err := b.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp struct {
		Ok          bool          `json:"ok"`
		Result      *TelegramFile `json:"result"`
		Description string        `json:"description,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}
	if !apiResp.Ok || apiResp.Result == nil {
		return nil, fmt.Errorf("getFile error: %s", apiResp.Description)
	}
	return apiResp.Result, nil
}

// DownloadFile downloads a file from Telegram servers to local disk
func (b *TelegramBot) DownloadFile(filePath string, destPath string) error {
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.Token, filePath)
	resp, err := b.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download error, HTTP status: %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// sendMultipartFile uploads a local file to a Telegram endpoint (sendDocument or sendPhoto)
func (b *TelegramBot) sendMultipartFile(chatID int64, endpoint string, fieldName string, filePath string, caption string) (*Message, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return nil, err
	}
	if caption != "" {
		_ = writer.WriteField("caption", caption)
		_ = writer.WriteField("parse_mode", "HTML")
	}

	part, err := writer.CreateFormFile(fieldName, filepath.Base(filePath))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.Token, endpoint)
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp struct {
		Ok          bool     `json:"ok"`
		Result      *Message `json:"result"`
		Description string   `json:"description,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}
	if !apiResp.Ok {
		return nil, fmt.Errorf("%s error: %s", endpoint, apiResp.Description)
	}
	return apiResp.Result, nil
}

// SendDocument uploads and sends a local file as a Telegram document
func (b *TelegramBot) SendDocument(chatID int64, filePath string, caption string) (*Message, error) {
	return b.sendMultipartFile(chatID, "sendDocument", "document", filePath, caption)
}

// SendPhoto uploads and sends a local image file as an inline Telegram photo, falling back to SendDocument
func (b *TelegramBot) SendPhoto(chatID int64, filePath string, caption string) (*Message, error) {
	msg, err := b.sendMultipartFile(chatID, "sendPhoto", "photo", filePath, caption)
	if err != nil {
		return b.SendDocument(chatID, filePath, caption)
	}
	return msg, nil
}

// SendFileAuto sends images (<10MB) as inline photos and all other files as documents
func (b *TelegramBot) SendFileAuto(chatID int64, filePath string, caption string) (*Message, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if info.Size() > 0 && info.Size() < 10*1024*1024 {
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp":
			return b.SendPhoto(chatID, filePath, caption)
		}
	}
	return b.SendDocument(chatID, filePath, caption)
}
