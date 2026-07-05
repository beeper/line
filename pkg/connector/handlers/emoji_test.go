package handlers

import (
	"strings"
	"testing"
)

const (
	testLineSticonPlaceholder1 = "\U00100084"
	testLineSticonPlaceholder2 = "\U00100085"
)

func TestSticonResourceByteRangeUsesUTF16Offsets(t *testing.T) {
	prefix := "\u30d6\u30e9\u30b8\u30eb "
	text := prefix + testLineSticonPlaceholder1 + " \u3064\u307e\u3089\u306a\u304b\u3063\u305f"
	start := utf16Units(prefix)
	end := start + utf16Units(testLineSticonPlaceholder1)

	startByte, endByte, ok := sticonResourceByteRange(text, SticonResource{Start: start, End: end})
	if !ok {
		t.Fatal("expected UTF-16 sticon offsets to resolve")
	}
	if got := text[startByte:endByte]; got != testLineSticonPlaceholder1 {
		t.Fatalf("resolved substring = %q, want %q", got, testLineSticonPlaceholder1)
	}
}

func TestSticonResourceByteRangeRejectsInvalidOffsets(t *testing.T) {
	text := "abc"
	tests := map[string]SticonResource{
		"empty":        {Start: 1, End: 1},
		"reversed":     {Start: 2, End: 1},
		"negative":     {Start: -1, End: 1},
		"unresolvable": {Start: 0, End: 100},
	}

	for name, resource := range tests {
		t.Run(name, func(t *testing.T) {
			if start, end, ok := sticonResourceByteRange(text, resource); ok {
				t.Fatalf("sticonResourceByteRange = %d/%d/true, want false", start, end)
			}
		})
	}
}

func TestBuildSticonMessageBodiesRemovesLinePlaceholders(t *testing.T) {
	text := "\u30d6\u30e9\u30b8\u30eb " + testLineSticonPlaceholder1 + " \u3064\u307e\u3089\u306a\u304b\u3063\u305f\n\n\u6226\u3048 " + testLineSticonPlaceholder2 + " \u7b11"
	firstStart := strings.Index(text, testLineSticonPlaceholder1)
	secondStart := strings.Index(text, testLineSticonPlaceholder2)

	body, formatted := buildSticonMessageBodies(text, []sticonReplacement{
		{start: secondStart, end: secondStart + len(testLineSticonPlaceholder2), mxc: "mxc://line/emoji2"},
		{start: firstStart, end: firstStart + len(testLineSticonPlaceholder1), mxc: "mxc://line/emoji1"},
	})

	if strings.Contains(body, testLineSticonPlaceholder1) || strings.Contains(body, testLineSticonPlaceholder2) {
		t.Fatalf("body still contains LINE placeholders: %q", body)
	}
	if strings.Contains(formatted, testLineSticonPlaceholder1) || strings.Contains(formatted, testLineSticonPlaceholder2) {
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

func TestBuildSticonMessageBodiesSkipsInvalidReplacements(t *testing.T) {
	text := "a" + testLineSticonPlaceholder1 + "def"
	start := strings.Index(text, testLineSticonPlaceholder1)
	end := start + len(testLineSticonPlaceholder1)

	body, formatted := buildSticonMessageBodies(text, []sticonReplacement{
		{start: start, end: end, mxc: "mxc://line/emoji1"},
		{start: start + 1, end: end + 1, mxc: "mxc://line/overlap"},
		{start: end, end: end, mxc: "mxc://line/empty"},
		{start: end, end: start, mxc: "mxc://line/reversed"},
		{start: 99, end: 100, mxc: "mxc://line/out-of-range"},
	})

	if want := "a[Emoji]def"; body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if strings.Contains(formatted, "overlap") || strings.Contains(formatted, "empty") ||
		strings.Contains(formatted, "reversed") || strings.Contains(formatted, "out-of-range") {
		t.Fatalf("formatted body included skipped replacement: %q", formatted)
	}
	if !strings.Contains(formatted, "mxc://line/emoji1") {
		t.Fatalf("formatted body missing valid replacement: %q", formatted)
	}

	body, formatted = buildSticonMessageBodies("abcdef", []sticonReplacement{
		{start: 3, end: 3, mxc: "mxc://line/empty"},
		{start: 99, end: 100, mxc: "mxc://line/out-of-range"},
	})
	if body != "abcdef" || formatted != "abcdef" {
		t.Fatalf("invalid-only replacements = %q/%q, want original text", body, formatted)
	}
}

func TestCleanInlineSticonPlaceholders(t *testing.T) {
	text := "a" + testLineSticonPlaceholder1 + testLineSticonPlaceholder2 + "b"
	if got, want := cleanInlineSticonPlaceholders(text), "a[Emoji]b"; got != want {
		t.Fatalf("cleanInlineSticonPlaceholders = %q, want %q", got, want)
	}
}

func TestLineSticonPlaceholderDetectionIsNarrow(t *testing.T) {
	if !ContainsLineSticonPlaceholder(testLineSticonPlaceholder1) {
		t.Fatal("expected LINE sticon placeholder to be detected")
	}
	for _, text := range []string{"\uf8ff", "\U000f0084"} {
		if ContainsLineSticonPlaceholder(text) {
			t.Fatalf("unexpected placeholder match for %U", []rune(text)[0])
		}
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
