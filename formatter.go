package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	reBold1   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reBold2   = regexp.MustCompile(`__([^_]+)__`)
	reItalic1 = regexp.MustCompile(`(?m)(^|[^\*])\*([^\*\s][^\*]*[^\*\s]|[^\*\s])\*([^\*]|$)`)
	reItalic2 = regexp.MustCompile(`(?m)(^|[^_])_([^_ \s][^_]*[^_ \s]|[^_ \s])_([^_]|$)`)
	reStrike  = regexp.MustCompile(`~~([^~]+)~~`)
	reLink    = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s\)]+)\)`)
	reHeader  = regexp.MustCompile(`(?m)^#{1,6}\s*(.+)$`)
	reBullet  = regexp.MustCompile(`(?m)^[\*\-]\s+(.+)$`)
	reHTMLTag = regexp.MustCompile(`<[^>]+>`)
)

func escapeTelegram(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// FormatTelegramHTML converts Markdown to Telegram-safe HTML
func FormatTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	type placeholder struct {
		token string
		value string
	}
	var placeholders []placeholder
	counter := 0

	// 1. Extract and protect code blocks ```lang\ncode\n```
	reCodeBlock := regexp.MustCompile("(?s)```([a-zA-Z0-9_-]*)\r?\n?(.*?)```")
	text = reCodeBlock.ReplaceAllStringFunc(text, func(m string) string {
		sub := reCodeBlock.FindStringSubmatch(m)
		lang := ""
		body := ""
		if len(sub) > 1 {
			lang = strings.TrimSpace(sub[1])
		}
		if len(sub) > 2 {
			body = sub[2]
		}
		escapedBody := escapeTelegram(body)
		token := fmt.Sprintf("\uFFF0CB%d\uFFF0", counter)
		counter++
		var tag string
		if lang != "" {
			tag = fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", escapeTelegram(lang), escapedBody)
		} else {
			tag = fmt.Sprintf("<pre>%s</pre>", escapedBody)
		}
		placeholders = append(placeholders, placeholder{token: token, value: tag})
		return token
	})

	// 2. Extract and protect inline code `code`
	reInlineCode := regexp.MustCompile("`([^`\n]+)`")
	text = reInlineCode.ReplaceAllStringFunc(text, func(m string) string {
		sub := reInlineCode.FindStringSubmatch(m)
		body := sub[1]
		escapedBody := escapeTelegram(body)
		token := fmt.Sprintf("\uFFF0IC%d\uFFF0", counter)
		counter++
		tag := fmt.Sprintf("<code>%s</code>", escapedBody)
		placeholders = append(placeholders, placeholder{token: token, value: tag})
		return token
	})

	// 3. Escape normal text (<, >, &)
	text = escapeTelegram(text)

	// 4. Transform Markdown Elements
	// Headers -> Bold
	text = reHeader.ReplaceAllString(text, "<b>$1</b>")

	// Bullet points
	text = reBullet.ReplaceAllString(text, "• $1")

	// Bold
	text = reBold1.ReplaceAllString(text, "<b>$1</b>")
	text = reBold2.ReplaceAllString(text, "<b>$1</b>")

	// Strikethrough
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	// Italic
	text = reItalic1.ReplaceAllString(text, "$1<i>$2</i>$3")
	text = reItalic2.ReplaceAllString(text, "$1<i>$2</i>$3")

	// Links [title](url)
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	// Blockquotes (> text)
	lines := strings.Split(text, "\n")
	var resultLines []string
	inQuote := false
	var quoteBuf []string

	flushQuote := func() {
		if inQuote {
			resultLines = append(resultLines, "<blockquote>"+strings.Join(quoteBuf, "\n")+"</blockquote>")
			quoteBuf = nil
			inQuote = false
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "&gt; ") || strings.HasPrefix(trimmed, "&gt;") {
			inQuote = true
			content := strings.TrimPrefix(trimmed, "&gt; ")
			content = strings.TrimPrefix(content, "&gt;")
			quoteBuf = append(quoteBuf, content)
		} else {
			flushQuote()
			resultLines = append(resultLines, line)
		}
	}
	flushQuote()
	text = strings.Join(resultLines, "\n")

	// 5. Restore placeholders (code blocks and inline code)
	for _, p := range placeholders {
		text = strings.Replace(text, p.token, p.value, 1)
	}

	return text
}

// SplitTelegramHTML splits long HTML text into chunks <= maxLen runes while preserving tag integrity
func SplitTelegramHTML(text string, maxLen int) []string {
	if utf8.RuneCountInString(text) <= maxLen {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var current strings.Builder
	currentLen := 0
	inPre := false
	inBlockquote := false

	for _, line := range lines {
		lineLen := utf8.RuneCountInString(line) + 1 // +1 for newline

		// Check if line contains start/end tags
		if strings.Contains(line, "<pre>") || strings.Contains(line, "<pre ") {
			inPre = true
		}
		if strings.Contains(line, "</pre>") {
			inPre = false
		}
		if strings.Contains(line, "<blockquote>") {
			inBlockquote = true
		}
		if strings.Contains(line, "</blockquote>") {
			inBlockquote = false
		}

		if currentLen+lineLen > maxLen && currentLen > 0 {
			// Need to finish current chunk
			if inPre {
				current.WriteString("\n</pre>")
			}
			if inBlockquote {
				current.WriteString("\n</blockquote>")
			}
			chunks = append(chunks, current.String())
			current.Reset()
			currentLen = 0

			// Re-open tags in new chunk
			if inPre {
				current.WriteString("<pre>\n")
				currentLen += 6
			}
			if inBlockquote {
				current.WriteString("<blockquote>\n")
				currentLen += 13
			}
		}

		if current.Len() > 0 && currentLen > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
		currentLen += lineLen
	}

	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}

// StripHTML removes HTML tags for plain text fallback
func StripHTML(s string) string {
	s = reHTMLTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	return s
}
