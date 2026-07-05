package handlers

import (
	"strings"
	"testing"
)

func TestSticonResourceByteRangeUsesUTF16Offsets(t *testing.T) {
	prefix := "\u30d6\u30e9\u30b8\u30eb "
	placeholder := "\U000f0084"
	text := prefix + placeholder + " \u3064\u307e\u3089\u306a\u304b\u3063\u305f"
	start := utf16Units(prefix)
	end := start + utf16Units(placeholder)

	startByte, endByte, ok := sticonResourceByteRange(text, SticonResource{Start: start, End: end})
	if !ok {
		t.Fatal("expected UTF-16 sticon offsets to resolve")
	}
	if got := text[startByte:endByte]; got != placeholder {
		t.Fatalf("resolved substring = %q, want %q", got, placeholder)
	}
}

func TestBuildSticonMessageBodiesRemovesLinePlaceholders(t *testing.T) {
	first := "\U000f0084"
	second := "\U000f0085"
	text := "\u30d6\u30e9\u30b8\u30eb " + first + " \u3064\u307e\u3089\u306a\u304b\u3063\u305f\n\n\u6226\u3048 " + second + " \u7b11"
	firstStart := strings.Index(text, first)
	secondStart := strings.Index(text, second)

	body, formatted := buildSticonMessageBodies(text, []sticonReplacement{
		{start: secondStart, end: secondStart + len(second), mxc: "mxc://line/emoji2"},
		{start: firstStart, end: firstStart + len(first), mxc: "mxc://line/emoji1"},
	})

	if strings.Contains(body, first) || strings.Contains(body, second) {
		t.Fatalf("body still contains LINE placeholders: %q", body)
	}
	if strings.Contains(formatted, first) || strings.Contains(formatted, second) {
		t.Fatalf("formatted body still contains LINE placeholders: %q", formatted)
	}

	wantBody := "\u30d6\u30e9\u30b8\u30eb [Emoji] \u3064\u307e\u3089\u306a\u304b\u3063\u305f\n\n\u6226\u3048 [Emoji] \u7b11"
	if body != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
	for _, want := range []string{
		`<img data-mx-emoticon src="mxc://line/emoji1" alt="[Emoji]" title="[Emoji]" height="32" />`,
		`<img data-mx-emoticon src="mxc://line/emoji2" alt="[Emoji]" title="[Emoji]" height="32" />`,
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted body missing %q in %q", want, formatted)
		}
	}
}

func TestCleanInlineSticonPlaceholders(t *testing.T) {
	text := "a\U000f0084\U000f0085b"
	if got, want := cleanInlineSticonPlaceholders(text), "a[Emoji]b"; got != want {
		t.Fatalf("cleanInlineSticonPlaceholders = %q, want %q", got, want)
	}
}

func TestHasSticonBody(t *testing.T) {
	body := `{"text":"x\udbc0\udc84","REPLACE":{"sticon":{"resources":[{"S":1,"E":3,"productId":"670e0cce840a8236ddd4ee4c","sticonId":"211","version":1,"resourceType":"STATIC"}]}}}`
	if !HasSticonBody(body) {
		t.Fatal("expected REPLACE.sticon body to be detected")
	}
}

func TestSticonRefRegexAllowsHexProductIDs(t *testing.T) {
	matches := sticonRefRegex.FindStringSubmatch("$STK:670e0cce840a8236ddd4ee4c:211$")
	if len(matches) != 3 {
		t.Fatalf("expected sticon ref to match, got %#v", matches)
	}
}

func utf16Units(text string) int {
	units := 0
	for _, r := range text {
		if r <= 0xffff {
			units++
		} else {
			units += 2
		}
	}
	return units
}
