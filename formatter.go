package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	reBold1        = regexp.MustCompile(`\*\*([^\n]+?)\*\*`)
	reBold2        = regexp.MustCompile(`__([^\n]+?)__`)
	reItalic1      = regexp.MustCompile(`(?m)(^|[^\*])\*([^\*\s][^\*\n]*[^\*\s]|[^\*\s])\*([^\*]|$)`)
	reItalic2      = regexp.MustCompile(`(?m)(^|[^_])_([^_ \s][^_\n]*[^_ \s]|[^_ \s])_([^_]|$)`)
	reStrike       = regexp.MustCompile(`~~([^\n]+?)~~`)
	reLink         = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s\)]+)\)`)
	reHeader       = regexp.MustCompile(`(?m)^#{1,6}\s*(.+)$`)
	reTaskDone     = regexp.MustCompile(`(?im)^\s*[\*\-]\s+\[x\]\s+(.+)$`)
	reTaskTodo     = regexp.MustCompile(`(?m)^\s*[\*\-]\s+\[\s\]\s+(.+)$`)
	reBullet       = regexp.MustCompile(`(?m)^[\*\-]\s+(.+)$`)
	reHorizRule    = regexp.MustCompile(`(?m)^\s*(?:-\s*-\s*-+|\*\s*\*\s*\*+|_\s*_\s*_+)\s*$`)
	reHTMLTag      = regexp.MustCompile(`<[^>]+>`)
	rePreCodeTag   = regexp.MustCompile(`<pre><code[^>]*>`)
	reTableSep     = regexp.MustCompile(`^\s*\|?(\s*:?-{2,}:?\s*\|)+\s*:?-{2,}:?\s*\|?\s*$`)
)

func escapeTelegram(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// parseTableCells splits a Markdown table row into trimmed cell strings
func parseTableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	raw := strings.Split(line, "|")
	cells := make([]string, 0, len(raw))
	for _, c := range raw {
		cells = append(cells, strings.TrimSpace(c))
	}
	return cells
}

// isTableRow checks if a line looks like a Markdown pipe-table row
func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return false
	}
	return strings.Count(trimmed, "|") >= 2
}

// convertMarkdownTables converts Markdown pipe tables into mobile-friendly bullet lists
func convertMarkdownTables(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	i := 0
	for i < len(lines) {
		if i+1 < len(lines) && isTableRow(lines[i]) && reTableSep.MatchString(lines[i+1]) {
			headers := parseTableCells(lines[i])
			i += 2 // skip header and separator
			for i < len(lines) && isTableRow(lines[i]) {
				if reTableSep.MatchString(lines[i]) {
					i++
					continue
				}
				cells := parseTableCells(lines[i])
				if len(cells) == 0 {
					i++
					continue
				}
				if len(cells) == 1 {
					out = append(out, fmt.Sprintf("• **%s**", cells[0]))
				} else if len(cells) == 2 {
					out = append(out, fmt.Sprintf("• **%s:** %s", cells[0], cells[1]))
				} else {
					var parts []string
					for colIdx := 1; colIdx < len(cells); colIdx++ {
						if cells[colIdx] == "" {
							continue
						}
						if colIdx < len(headers) && headers[colIdx] != "" {
							parts = append(parts, fmt.Sprintf("*%s:* %s", headers[colIdx], cells[colIdx]))
						} else {
							parts = append(parts, cells[colIdx])
						}
					}
					if len(parts) > 0 {
						out = append(out, fmt.Sprintf("• **%s** — %s", cells[0], strings.Join(parts, " · ")))
					} else {
						out = append(out, fmt.Sprintf("• **%s**", cells[0]))
					}
				}
				i++
			}
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return strings.Join(out, "\n")
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

	// 2.5 Convert any Markdown tables into mobile-friendly bullet points
	text = convertMarkdownTables(text)

	// 3. Escape normal text (<, >, &)
	text = escapeTelegram(text)

	// 4. Transform Markdown Elements
	// Horizontal rules (---, ***, ___) -> sleek mobile divider (before bold/italic!)
	text = reHorizRule.ReplaceAllString(text, "┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈")

	// Headers -> Bold
	text = reHeader.ReplaceAllString(text, "<b>$1</b>")

	// Task list checkboxes (- [x] / - [ ]) -> ✅ / ⬜
	text = reTaskDone.ReplaceAllString(text, "✅ $1")
	text = reTaskTodo.ReplaceAllString(text, "⬜ $1")

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
			joined := strings.Join(quoteBuf, "\n")
			if len(quoteBuf) > 6 || utf8.RuneCountInString(joined) > 450 {
				resultLines = append(resultLines, "<blockquote expandable>"+joined+"</blockquote>")
			} else {
				resultLines = append(resultLines, "<blockquote>"+joined+"</blockquote>")
			}
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

// splitLongLines ensures no single line exceeds maxLineLen runes without breaking HTML entities
func splitLongLines(lines []string, maxLineLen int) []string {
	if maxLineLen < 80 {
		maxLineLen = 80
	}
	var result []string
	for _, line := range lines {
		if utf8.RuneCountInString(line) <= maxLineLen {
			result = append(result, line)
			continue
		}
		runes := []rune(line)
		for len(runes) > maxLineLen {
			cut := maxLineLen
			// Avoid cutting inside an HTML entity (&...;) or tag (<...>)
			for back := 0; back < 20 && cut-back > 10; back++ {
				ch := runes[cut-back-1]
				if ch == ' ' || ch == '\t' {
					cut = cut - back
					break
				}
			}
			result = append(result, string(runes[:cut]))
			runes = runes[cut:]
		}
		if len(runes) > 0 {
			result = append(result, string(runes))
		}
	}
	return result
}

// SplitTelegramHTML splits long HTML text into chunks <= maxLen runes while preserving tag integrity
func SplitTelegramHTML(text string, maxLen int) []string {
	if utf8.RuneCountInString(text) <= maxLen {
		return []string{text}
	}

	rawLines := strings.Split(text, "\n")
	lines := splitLongLines(rawLines, maxLen-60)

	var chunks []string
	var current strings.Builder
	currentLen := 0
	inPre := false
	preOpenTag := "<pre>"
	preCloseTag := "</pre>"
	inBlockquote := false
	blockquoteOpenTag := "<blockquote>"

	for _, line := range lines {
		lineLen := utf8.RuneCountInString(line) + 1 // +1 for newline

		// Check if adding this line exceeds maxLen BEFORE updating tag state for this line
		if currentLen+lineLen > maxLen && currentLen > 0 {
			if inPre {
				current.WriteString("\n" + preCloseTag)
			}
			if inBlockquote {
				current.WriteString("\n</blockquote>")
			}
			chunks = append(chunks, current.String())
			current.Reset()
			currentLen = 0

			// Re-open active block tags in the new chunk
			if inBlockquote {
				current.WriteString(blockquoteOpenTag + "\n")
				currentLen += utf8.RuneCountInString(blockquoteOpenTag) + 1
			}
			if inPre {
				current.WriteString(preOpenTag + "\n")
				currentLen += utf8.RuneCountInString(preOpenTag) + 1
			}
		}

		if current.Len() > 0 && currentLen > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
		currentLen += lineLen

		// Update tag state AFTER appending the line
		if m := rePreCodeTag.FindString(line); m != "" {
			inPre = true
			preOpenTag = m
			preCloseTag = "</code></pre>"
		} else if strings.Contains(line, "<pre>") || strings.Contains(line, "<pre ") {
			inPre = true
			preOpenTag = "<pre>"
			preCloseTag = "</pre>"
		}
		if strings.Contains(line, "</pre>") {
			inPre = false
			preOpenTag = "<pre>"
			preCloseTag = "</pre>"
		}
		if strings.Contains(line, "<blockquote expandable>") {
			inBlockquote = true
			blockquoteOpenTag = "<blockquote expandable>"
		} else if strings.Contains(line, "<blockquote>") {
			inBlockquote = true
			blockquoteOpenTag = "<blockquote>"
		}
		if strings.Contains(line, "</blockquote>") {
			inBlockquote = false
			blockquoteOpenTag = "<blockquote>"
		}
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
