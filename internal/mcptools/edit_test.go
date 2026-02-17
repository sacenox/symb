package mcptools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xonecas/symb/internal/hashline"
	"github.com/xonecas/symb/internal/mcp"
)

const threeLineContent = "aaa\nbbb\nccc\n"

func setupTestFile(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte(threeLineContent), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func newTrackedHandler(t *testing.T, dir string) *EditHandler {
	t.Helper()
	tracker := NewFileReadTracker()
	h := NewEditHandler(tracker, nil, nil)
	h.SetRootDir(dir)
	return h
}

func callEdit(t *testing.T, handler *EditHandler, jsonStr string) *mcp.ToolResult {
	t.Helper()
	result, err := handler.Handle(context.Background(), json.RawMessage(jsonStr))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

// hashFor returns the hash for a given line in content.
func hashFor(content string, lineNum int) string {
	tagged := hashline.TagLines(content, 1)
	if lineNum < 1 || lineNum > len(tagged) {
		panic("line out of range")
	}
	return tagged[lineNum-1].Hash
}

func TestEditReplace(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	h1 := hashFor(threeLineContent, 1)
	h2 := hashFor(threeLineContent, 2)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "replace",
		"start": "1:`+h1+`",
		"end": "2:`+h2+`",
		"content": "xxx\nyyy"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "xxx\nyyy\nccc\n" {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestEditReplaceSingleLine(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	h2 := hashFor(threeLineContent, 2)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "replace",
		"start": "2:`+h2+`",
		"end": "2:`+h2+`",
		"content": "BBB"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "aaa\nBBB\nccc\n" {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestEditReplaceWithMoreLines(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	h2 := hashFor(threeLineContent, 2)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "replace",
		"start": "2:`+h2+`",
		"end": "2:`+h2+`",
		"content": "B1\nB2\nB3"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "aaa\nB1\nB2\nB3\nccc\n" {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestEditInsert(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	h1 := hashFor(threeLineContent, 1)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "insert",
		"after": "1:`+h1+`",
		"content": "inserted"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "aaa\ninserted\nbbb\nccc\n" {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestEditDelete(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	h2 := hashFor(threeLineContent, 2)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "delete",
		"start": "2:`+h2+`",
		"end": "2:`+h2+`"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "aaa\nccc\n" {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestEditCreate(t *testing.T) {
	dir, _ := setupTestFile(t)
	handler := newTrackedHandler(t, dir)

	result := callEdit(t, handler, `{
		"file": "new.txt",
		"operation": "create",
		"content": "hello\nworld"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
	if string(got) != "hello\nworld" {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestEditCreateFailsIfExists(t *testing.T) {
	dir, _ := setupTestFile(t)
	handler := newTrackedHandler(t, dir)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "create",
		"content": "overwrite"
	}`)

	if !result.IsError {
		t.Fatal("should fail when file exists")
	}
}

func TestEditHashMismatch(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "replace",
		"start": "1:zz",
		"end": "1:zz",
		"content": "xxx"
	}`)

	if !result.IsError {
		t.Fatal("should fail with hash mismatch")
	}
}

func TestEditNoOperation(t *testing.T) {
	dir, _ := setupTestFile(t)
	handler := newTrackedHandler(t, dir)

	result := callEdit(t, handler, `{"file": "test.txt"}`)

	if !result.IsError {
		t.Fatal("should fail without operation")
	}
}

func TestEditPathTraversal(t *testing.T) {
	dir, _ := setupTestFile(t)
	handler := newTrackedHandler(t, dir)

	result := callEdit(t, handler, `{
		"file": "../../../etc/passwd",
		"operation": "create",
		"content": "bad"
	}`)

	if !result.IsError {
		t.Fatal("should reject path traversal")
	}
}

func TestEditRequiresReadFirst(t *testing.T) {
	dir, _ := setupTestFile(t)
	handler := newTrackedHandler(t, dir)

	h1 := hashFor(threeLineContent, 1)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "replace",
		"start": "1:`+h1+`",
		"end": "1:`+h1+`",
		"content": "xxx"
	}`)

	if !result.IsError {
		t.Fatal("should require Read first")
	}
	if !strings.Contains(result.Content[0].Text, "must Read") {
		t.Errorf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestEditCreateBypassesReadCheck(t *testing.T) {
	dir, _ := setupTestFile(t)
	handler := newTrackedHandler(t, dir)

	// create should work without prior Read
	result := callEdit(t, handler, `{
		"file": "brand_new.txt",
		"operation": "create",
		"content": "hello"
	}`)

	if result.IsError {
		t.Fatalf("create should not require Read: %s", result.Content[0].Text)
	}
}

func TestEditWindowedResponse(t *testing.T) {
	// Build a file with 100 lines.
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		sb.WriteString("line\n")
	}
	bigContent := sb.String()

	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}

	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	h50 := hashFor(bigContent, 50)

	result := callEdit(t, handler, `{
		"file": "big.txt",
		"operation": "replace",
		"start": "50:`+h50+`",
		"end": "50:`+h50+`",
		"content": "REPLACED"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "showing") {
		t.Errorf("expected windowed output, got: %s", text[:min(len(text), 200)])
	}
}

func TestEditSmallFileFullResponse(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	h1 := hashFor(threeLineContent, 1)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "replace",
		"start": "1:`+h1+`",
		"end": "1:`+h1+`",
		"content": "XXX"
	}`)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	text := result.Content[0].Text
	if strings.Contains(text, "showing") {
		t.Errorf("small file should not be windowed: %s", text)
	}
}

func TestEditUnknownOperation(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "patch"
	}`)

	if !result.IsError {
		t.Fatal("should fail with unknown operation")
	}
}

func TestEditBadAnchorFormat(t *testing.T) {
	dir, path := setupTestFile(t)
	handler := newTrackedHandler(t, dir)
	handler.tracker.MarkRead(path)

	// Missing hash
	result := callEdit(t, handler, `{
		"file": "test.txt",
		"operation": "replace",
		"start": "1",
		"end": "2",
		"content": "x"
	}`)

	if !result.IsError {
		t.Fatal("should fail with bad anchor")
	}
}
