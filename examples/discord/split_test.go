package main

import (
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	code := "```go\n" + strings.Repeat("fmt.Println(\"x\")\n", 300) + "```\n"
	text := "intro\n" + code + "outro " + strings.Repeat("é", 5000)
	parts := split(text, 2000)
	if len(parts) < 3 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	joined := 0
	for i, p := range parts {
		if n := len([]rune(p)); n > 2000 {
			t.Fatalf("part %d has %d runes", i, n)
		}
		if strings.Count(p, "```")%2 != 0 {
			t.Fatalf("part %d has unbalanced fences:\n%s", i, p[:80])
		}
		joined += len([]rune(p))
	}
	if split("  ", 2000) != nil {
		t.Fatal("blank text should produce no parts")
	}
	if got := split("hello", 2000); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("short text: %q", got)
	}
}
