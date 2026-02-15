package hashline

import (
	"strings"
	"testing"
)

func TestLineHash(t *testing.T) {
	// Deterministic: same input → same hash
	h1 := LineHash("hello world")
	h2 := LineHash("hello world")
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %s vs %s", h1, h2)
	}

	// Different input → (very likely) different hash
	h3 := LineHash("hello world!")
	if h1 == h3 {
		t.Errorf("different inputs produced same hash: %s", h1)
	}

	// Always 2 hex chars
	if len(h1) != HashLen {
		t.Errorf("expected hash length %d, got %d", HashLen, len(h1))
	}

	// Empty line gets a hash too
	h4 := LineHash("")
	if len(h4) != HashLen {
		t.Errorf("empty line hash length: expected %d, got %d", HashLen, len(h4))
	}
}

func TestTagLines(t *testing.T) {
	content := "func hello() {\n  return \"world\"\n}"

	tagged := TagLines(content, 1)
	if len(tagged) != 3 {
		t.Fatalf("expected 3 tagged lines, got %d", len(tagged))
	}

	// Check numbering
	for i, tl := range tagged {
		if tl.Num != i+1 {
			t.Errorf("line %d: expected Num=%d, got %d", i, i+1, tl.Num)
		}
		if len(tl.Hash) != HashLen {
			t.Errorf("line %d: expected hash length %d, got %d", i, HashLen, len(tl.Hash))
		}
	}

	if tagged[0].Content != "func hello() {" {
		t.Errorf("line 0 content: %q", tagged[0].Content)
	}
	if tagged[2].Content != "}" {
		t.Errorf("line 2 content: %q", tagged[2].Content)
	}
}

func TestTagLinesWithOffset(t *testing.T) {
	content := "line a\nline b"
	tagged := TagLines(content, 10)

	if tagged[0].Num != 10 {
		t.Errorf("expected first line num 10, got %d", tagged[0].Num)
	}
	if tagged[1].Num != 11 {
		t.Errorf("expected second line num 11, got %d", tagged[1].Num)
	}
}

func TestFormatTagged(t *testing.T) {
	tagged := []TaggedLine{
		{Num: 1, Hash: "a3", Content: "func hello() {"},
		{Num: 2, Hash: "f1", Content: "  return \"world\""},
		{Num: 3, Hash: "0e", Content: "}"},
	}

	output := FormatTagged(tagged)
	expected := "1:a3|func hello() {\n2:f1|  return \"world\"\n3:0e|}"
	if output != expected {
		t.Errorf("FormatTagged:\ngot:  %q\nwant: %q", output, expected)
	}
}

func TestAnchorValidate(t *testing.T) {
	lines := []string{"func hello() {", "  return \"world\"", "}"}

	// Valid anchor
	hash := LineHash(lines[0])
	a := Anchor{Num: 1, Hash: hash}
	if err := a.Validate(lines); err != nil {
		t.Errorf("valid anchor failed: %v", err)
	}

	// Out of range
	a2 := Anchor{Num: 0, Hash: "ff"}
	if err := a2.Validate(lines); err == nil {
		t.Error("line 0 should be out of range")
	}

	a3 := Anchor{Num: 4, Hash: "ff"}
	if err := a3.Validate(lines); err == nil {
		t.Error("line 4 should be out of range")
	}

	// Wrong hash (file changed) — error should include actual line content
	a4 := Anchor{Num: 1, Hash: "ff"}
	err := a4.Validate(lines)
	if err == nil {
		t.Error("wrong hash should fail validation")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "actual:") {
		t.Errorf("error should contain actual line content: %s", errMsg)
	}
	if !strings.Contains(errMsg, "func hello()") {
		t.Errorf("error should contain the line text: %s", errMsg)
	}
	if !strings.Contains(errMsg, "re-Read") {
		t.Errorf("error should suggest re-reading: %s", errMsg)
	}
}

func TestValidateRange(t *testing.T) {
	lines := []string{"aaa", "bbb", "ccc"}
	h1 := LineHash(lines[0])
	h2 := LineHash(lines[1])
	h3 := LineHash(lines[2])

	// Valid range
	if err := ValidateRange(Anchor{1, h1}, Anchor{3, h3}, lines); err != nil {
		t.Errorf("valid range failed: %v", err)
	}

	// Single line range
	if err := ValidateRange(Anchor{2, h2}, Anchor{2, h2}, lines); err != nil {
		t.Errorf("single line range failed: %v", err)
	}

	// Inverted range
	if err := ValidateRange(Anchor{3, h3}, Anchor{1, h1}, lines); err == nil {
		t.Error("inverted range should fail")
	}

	// Bad start hash
	if err := ValidateRange(Anchor{1, "ff"}, Anchor{3, h3}, lines); err == nil {
		t.Error("bad start hash should fail")
	}

	// Bad end hash
	if err := ValidateRange(Anchor{1, h1}, Anchor{3, "ff"}, lines); err == nil {
		t.Error("bad end hash should fail")
	}
}

func TestRoundTrip(t *testing.T) {
	// Simulate the full flow: tag lines, extract anchors, validate
	content := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}"
	tagged := TagLines(content, 1)

	lines := make([]string, len(tagged))
	for i, tl := range tagged {
		lines[i] = tl.Content
	}

	// Every tagged anchor should validate
	for _, tl := range tagged {
		a := Anchor{Num: tl.Num, Hash: tl.Hash}
		if err := a.Validate(lines); err != nil {
			t.Errorf("round-trip validation failed for line %d: %v", tl.Num, err)
		}
	}
}
