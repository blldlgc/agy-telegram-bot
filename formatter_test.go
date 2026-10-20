package main

import (
	"strings"
	"testing"
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

func TestSplitTelegramHTML(t *testing.T) {
	longCode := "```go\n" + strings.Repeat("x = x + 1\n", 500) + "```"
	html := FormatTelegramHTML(longCode)
	chunks := SplitTelegramHTML(html, 500)

	if len(chunks) < 2 {
		t.Errorf("Expected multiple chunks, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		if strings.Contains(chunk, "<pre>") && !strings.Contains(chunk, "</pre>") {
			t.Errorf("Chunk %d has unclosed <pre>", i)
		}
	}
}
