// Package hashline provides content-addressed line tagging for reliable file editing.
//
// Each line gets a short hex hash derived from its content. The LLM references
// these hashes when editing, so it never needs to reproduce old content verbatim.
// If the file changed since the last read, hashes won't match and the edit is
// rejected before anything gets corrupted.
package hashline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// HashLen is the number of hex characters per line hash (1 byte = 2 hex chars).
const HashLen = 2

// LineHash computes a short content hash for a single line.
func LineHash(line string) string {
	h := sha256.Sum256([]byte(line))
	return hex.EncodeToString(h[:1]) // first byte → 2 hex chars
}

// TaggedLine represents a line with its number and content hash.
type TaggedLine struct {
	Num     int    // 1-indexed line number
	Hash    string // 2-char hex hash
	Content string // raw line content
}

// Tag formats a tagged line as "num:hash|content".
func (t TaggedLine) Tag() string {
	return fmt.Sprintf("%d:%s|%s", t.Num, t.Hash, t.Content)
}

// TagLines takes file content and returns tagged lines.
// If startLine > 0, numbering begins at startLine (1-indexed).
func TagLines(content string, startLine int) []TaggedLine {
	if startLine <= 0 {
		startLine = 1
	}

	lines := strings.Split(content, "\n")
	tagged := make([]TaggedLine, len(lines))
	for i, line := range lines {
		tagged[i] = TaggedLine{
			Num:     startLine + i,
			Hash:    LineHash(line),
			Content: line,
		}
	}
	return tagged
}

// FormatTagged formats tagged lines into the string returned to the LLM.
func FormatTagged(tagged []TaggedLine) string {
	var b strings.Builder
	for i, t := range tagged {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(t.Tag())
	}
	return b.String()
}

// HashMismatchError is returned when an anchor's hash doesn't match the actual file content.
type HashMismatchError struct {
	Line     int
	Expected string
	Got      string
	Content  string
}

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("hash mismatch at line %d: expected %s, got %s — actual: %q (re-Read the file to get fresh hashes)", e.Line, e.Expected, e.Got, e.Content)
}

// Anchor identifies a line by number and hash.
type Anchor struct {
	Num  int    `json:"line"`
	Hash string `json:"hash"`
}

// Validate checks that the anchor matches the actual file lines.
// lines is 0-indexed; anchor.Num is 1-indexed.
func (a Anchor) Validate(lines []string) error {
	idx := a.Num - 1
	if idx < 0 || idx >= len(lines) {
		return fmt.Errorf("line %d out of range (file has %d lines)", a.Num, len(lines))
	}
	actual := LineHash(lines[idx])
	if actual != a.Hash {
		return &HashMismatchError{
			Line:     a.Num,
			Expected: a.Hash,
			Got:      actual,
			Content:  lines[idx],
		}
	}
	return nil
}

// ValidateRange checks that start and end anchors are valid and ordered.
func ValidateRange(start, end Anchor, lines []string) error {
	if err := start.Validate(lines); err != nil {
		return fmt.Errorf("start anchor: %w", err)
	}
	if err := end.Validate(lines); err != nil {
		return fmt.Errorf("end anchor: %w", err)
	}
	if start.Num > end.Num {
		return fmt.Errorf("start line %d is after end line %d", start.Num, end.Num)
	}
	return nil
}

// ParseAnchor parses a "line:hash" string (e.g. "5:ab") into an Anchor.
func ParseAnchor(s string) (Anchor, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return Anchor{}, fmt.Errorf("invalid anchor %q: expected line:hash", s)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return Anchor{}, fmt.Errorf("invalid anchor %q: bad line number", s)
	}
	h := parts[1]
	if len(h) != HashLen {
		return Anchor{}, fmt.Errorf("invalid anchor %q: hash must be %d hex chars", s, HashLen)
	}
	return Anchor{Num: n, Hash: h}, nil
}
