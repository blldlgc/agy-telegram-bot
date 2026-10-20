package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatTelegramHTML(t *testing.T) {
	input := `# Header
Here is **bold** and *italic* text.
Link: [Google](https://google.com)
List:
* Item 1
* Item 2

> This is a quote
> Second line quote

Code block:
` + "```go\nfunc main() {\n\tfmt.Println(\"A < B & C > D\")\n}\n```" + `
Inline code: ` + "`x < 10 && y > 20`"

	res := FormatTelegramHTML(input)

	if !strings.Contains(res, "<b>Header</b>") {
		t.Errorf("Header not converted to bold: %s", res)
	}
	if !strings.Contains(res, "<b>bold</b>") {
		t.Errorf("Bold not converted: %s", res)
	}
	if !strings.Contains(res, "<i>italic</i>") {
		t.Errorf("Italic not converted: %s", res)
	}
	if !strings.Contains(res, `<a href="https://google.com">Google</a>`) {
		t.Errorf("Link not converted: %s", res)
	}
	if !strings.Contains(res, "<blockquote>This is a quote\nSecond line quote</blockquote>") {
		t.Errorf("Blockquote not converted: %s", res)
	}
	if !strings.Contains(res, "&lt;pre&gt;") && !strings.Contains(res, "<pre>") {
		t.Errorf("Pre tag missing: %s", res)
	}
	if !strings.Contains(res, "fmt.Println(&#34;A &lt; B &amp; C &gt; D&#34;)") && !strings.Contains(res, "A &lt; B &amp; C &gt; D") {
		t.Errorf("Code content escaping failed: %s", res)
	}
	if !strings.Contains(res, "<code>x &lt; 10 &amp;&amp; y &gt; 20</code>") {
		t.Errorf("Inline code escaping failed: %s", res)
	}
}

func TestMarkdownTableConversion(t *testing.T) {
	input := `Here is a table:
| Feature | Status | Priority |
| --- | :---: | ---: |
| Auth | Done | High |
| Queue | Active | Medium |`

	res := FormatTelegramHTML(input)
	if !strings.Contains(res, "• <b>Auth</b>") || !strings.Contains(res, "<i>Status:</i> Done") {
		t.Errorf("Table not converted to mobile bullet list: %s", res)
	}
}

func TestCheckboxesAndHorizontalRulesAndBoldAsterisk(t *testing.T) {
	input := `**Pointer *p in Go**
---
- [x] Completed task
- [ ] Pending task`

	res := FormatTelegramHTML(input)
	if !strings.Contains(res, "<b>Pointer *p in Go</b>") {
		t.Errorf("Bold with inner asterisk failed: %s", res)
	}
	if !strings.Contains(res, "┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈") {
		t.Errorf("Horizontal rule not converted: %s", res)
	}
	if !strings.Contains(res, "✅ Completed task") || !strings.Contains(res, "⬜ Pending task") {
		t.Errorf("Task list checkboxes not converted: %s", res)
	}
}

func TestExpandableBlockquote(t *testing.T) {
	var lines []string
	for i := 0; i < 8; i++ {
		lines = append(lines, "> Quote line")
	}
	res := FormatTelegramHTML(strings.Join(lines, "\n"))
	if !strings.Contains(res, "<blockquote expandable>") {
		t.Errorf("Expected expandable blockquote for >6 lines, got: %s", res)
	}
}

func TestSplitTelegramHTML(t *testing.T) {
	longCode := "```go\n" + strings.Repeat("x = x + 1\n", 500) + "```"
	html := FormatTelegramHTML(longCode)
	chunks := SplitTelegramHTML(html, 500)

	if len(chunks) < 2 {
		t.Errorf("Expected multiple chunks, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		if strings.Contains(chunk, "<pre><code") && !strings.Contains(chunk, "</code></pre>") {
			t.Errorf("Chunk %d has unclosed <pre><code>: %s", i, chunk)
		}
		if strings.Contains(chunk, "</code></pre>") && !strings.Contains(chunk, "<pre><code") {
			t.Errorf("Chunk %d has unmatched </code></pre>: %s", i, chunk)
		}
	}
}

func TestSplitLongSingleLine(t *testing.T) {
	hugeLine := strings.Repeat("word ", 400) // ~2000 chars on a single line
	chunks := SplitTelegramHTML(hugeLine, 500)
	if len(chunks) < 3 {
		t.Errorf("Expected long single line to be split into >= 3 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > 500 {
			t.Errorf("Chunk %d exceeds maxLen (len=%d)", i, utf8.RuneCountInString(c))
		}
	}
}
