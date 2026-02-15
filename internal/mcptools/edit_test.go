package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xonecas/symb/internal/hashline"
)

const threeLineContent = "line one\nline two\nline three"

// setupTestFile creates a temp file with the given content and returns its path and cleanup func.
func setupTestFile(t *testing.T, content string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Change to temp dir so path validation passes
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return path, func() {
		os.Chdir(origDir) //nolint:errcheck
	}
}

// newTrackedHandler creates an EditHandler with a tracker where the file is already marked as read.
func newTrackedHandler(t *testing.T, absPath string) *EditHandler {
	t.Helper()
	tracker := NewFileReadTracker()
	tracker.MarkRead(absPath)
	return NewEditHandler(tracker, nil, nil)
}

func callEdit(t *testing.T, handler *EditHandler, args EditArgs) (string, bool) {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	result, err := handler.Handle(context.Background(), argsJSON)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	text := ""
	if len(result.Content) > 0 {
		text = result.Content[0].Text
	}
	return text, result.IsError
}

func TestEditReplace(t *testing.T) {
	content := "line one\nline two\nline three\nline four"
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	h2 := hashline.LineHash(lines[1])
	h3 := hashline.LineHash(lines[2])

	handler := newTrackedHandler(t, path)
	text, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 2, Hash: h2},
			End:     hashline.Anchor{Num: 3, Hash: h3},
			Content: "replaced line",
		},
	})

	if isErr {
		t.Fatalf("replace failed: %s", text)
	}

	got, _ := os.ReadFile(path)
	expected := "line one\nreplaced line\nline four"
	if string(got) != expected {
		t.Errorf("file content:\ngot:  %q\nwant: %q", string(got), expected)
	}

	// Result should contain hashline-tagged output
	if !strings.Contains(text, "Edited") {
		t.Errorf("result should mention 'Edited': %s", text)
	}
}

func TestEditInsert(t *testing.T) {
	content := threeLineContent
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	h1 := hashline.LineHash(lines[0])

	handler := newTrackedHandler(t, path)
	text, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Insert: &InsertOp{
			After:   hashline.Anchor{Num: 1, Hash: h1},
			Content: "inserted a\ninserted b",
		},
	})

	if isErr {
		t.Fatalf("insert failed: %s", text)
	}

	got, _ := os.ReadFile(path)
	expected := "line one\ninserted a\ninserted b\nline two\nline three"
	if string(got) != expected {
		t.Errorf("file content:\ngot:  %q\nwant: %q", string(got), expected)
	}
}

func TestEditDelete(t *testing.T) {
	content := "line one\nline two\nline three\nline four"
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	h2 := hashline.LineHash(lines[1])
	h3 := hashline.LineHash(lines[2])

	handler := newTrackedHandler(t, path)
	text, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Delete: &DeleteOp{
			Start: hashline.Anchor{Num: 2, Hash: h2},
			End:   hashline.Anchor{Num: 3, Hash: h3},
		},
	})

	if isErr {
		t.Fatalf("delete failed: %s", text)
	}

	got, _ := os.ReadFile(path)
	expected := "line one\nline four"
	if string(got) != expected {
		t.Errorf("file content:\ngot:  %q\nwant: %q", string(got), expected)
	}
}

func TestEditCreate(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	tracker := NewFileReadTracker()
	handler := NewEditHandler(tracker, nil, nil)
	text, isErr := callEdit(t, handler, EditArgs{
		File:   "newfile.go",
		Create: &CreateOp{Content: "package main\n\nfunc main() {}\n"},
	})

	if isErr {
		t.Fatalf("create failed: %s", text)
	}

	got, err := os.ReadFile(filepath.Join(dir, "newfile.go"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	expected := "package main\n\nfunc main() {}\n"
	if string(got) != expected {
		t.Errorf("file content:\ngot:  %q\nwant: %q", string(got), expected)
	}
}

func TestEditCreateFailsIfExists(t *testing.T) {
	_, cleanup := setupTestFile(t, "existing content")
	defer cleanup()

	tracker := NewFileReadTracker()
	handler := NewEditHandler(tracker, nil, nil)
	_, isErr := callEdit(t, handler, EditArgs{
		File:   "test.go",
		Create: &CreateOp{Content: "new content"},
	})

	if !isErr {
		t.Error("create should fail if file already exists")
	}
}

func TestEditHashMismatch(t *testing.T) {
	content := threeLineContent
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	handler := newTrackedHandler(t, path)
	_, isErr := callEdit(t, handler, EditArgs{
		File: "test.go",
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 1, Hash: "ff"},
			End:     hashline.Anchor{Num: 2, Hash: "ff"},
			Content: "whatever",
		},
	})

	if !isErr {
		t.Error("should fail on hash mismatch")
	}
}

func TestEditNoOperation(t *testing.T) {
	_, cleanup := setupTestFile(t, "single line")
	defer cleanup()

	tracker := NewFileReadTracker()
	handler := NewEditHandler(tracker, nil, nil)
	_, isErr := callEdit(t, handler, EditArgs{
		File: "test.go",
	})

	if !isErr {
		t.Error("should fail when no operation specified")
	}
}

func TestEditMultipleOperations(t *testing.T) {
	testContent := "single line"
	path, cleanup := setupTestFile(t, testContent)
	defer cleanup()

	lines := strings.Split(testContent, "\n")
	h1 := hashline.LineHash(lines[0])

	handler := newTrackedHandler(t, path)
	_, isErr := callEdit(t, handler, EditArgs{
		File: "test.go",
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 1, Hash: h1},
			End:     hashline.Anchor{Num: 1, Hash: h1},
			Content: "new",
		},
		Delete: &DeleteOp{
			Start: hashline.Anchor{Num: 1, Hash: h1},
			End:   hashline.Anchor{Num: 1, Hash: h1},
		},
	})

	if !isErr {
		t.Error("should fail when multiple operations specified")
	}
}

func TestEditPathTraversal(t *testing.T) {
	_, cleanup := setupTestFile(t, "single line")
	defer cleanup()

	tracker := NewFileReadTracker()
	handler := NewEditHandler(tracker, nil, nil)
	_, isErr := callEdit(t, handler, EditArgs{
		File:   "../../../etc/passwd",
		Create: &CreateOp{Content: "hacked"},
	})

	if !isErr {
		t.Error("should reject path traversal")
	}
}

func TestEditReplaceSingleLine(t *testing.T) {
	content := "aaa\nbbb\nccc"
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	h2 := hashline.LineHash(lines[1])

	handler := newTrackedHandler(t, path)
	_, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 2, Hash: h2},
			End:     hashline.Anchor{Num: 2, Hash: h2},
			Content: "BBB",
		},
	})

	if isErr {
		t.Fatal("single line replace should work")
	}

	got, _ := os.ReadFile(path)
	if string(got) != "aaa\nBBB\nccc" {
		t.Errorf("got: %q", string(got))
	}
}

func TestEditReplaceWithMoreLines(t *testing.T) {
	content := "aaa\nbbb\nccc"
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	h2 := hashline.LineHash(lines[1])

	handler := newTrackedHandler(t, path)
	_, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 2, Hash: h2},
			End:     hashline.Anchor{Num: 2, Hash: h2},
			Content: "BBB\nDDD\nEEE",
		},
	})

	if isErr {
		t.Fatal("replace expanding lines should work")
	}

	got, _ := os.ReadFile(path)
	if string(got) != "aaa\nBBB\nDDD\nEEE\nccc" {
		t.Errorf("got: %q", string(got))
	}
}

func TestEditRequiresReadFirst(t *testing.T) {
	content := "line one\nline two"
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	h1 := hashline.LineHash(lines[0])

	// Handler with empty tracker — file NOT read
	tracker := NewFileReadTracker()
	handler := NewEditHandler(tracker, nil, nil)

	text, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 1, Hash: h1},
			End:     hashline.Anchor{Num: 1, Hash: h1},
			Content: "replaced",
		},
	})

	if !isErr {
		t.Fatal("should fail when file was not read first")
	}
	if !strings.Contains(text, "Read") {
		t.Errorf("error should mention Read tool: %s", text)
	}

	// Now mark as read and retry — should succeed
	absPath, _ := filepath.Abs(filepath.Base(path))
	tracker.MarkRead(absPath)

	_, isErr = callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 1, Hash: h1},
			End:     hashline.Anchor{Num: 1, Hash: h1},
			Content: "replaced",
		},
	})

	if isErr {
		t.Fatal("should succeed after file was read")
	}
}

func TestEditCreateBypassesReadCheck(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	// Empty tracker — no file read. Create should still work.
	tracker := NewFileReadTracker()
	handler := NewEditHandler(tracker, nil, nil)
	_, isErr := callEdit(t, handler, EditArgs{
		File:   "brand-new.go",
		Create: &CreateOp{Content: "package new\n"},
	})

	if isErr {
		t.Fatal("create should not require prior read")
	}
}

func TestEditWindowedResponse(t *testing.T) {
	// Build a file with >50 lines so windowed output kicks in.
	var b strings.Builder
	totalLines := 80
	for i := 1; i <= totalLines; i++ {
		if i > 1 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "line %d", i)
	}
	content := b.String()

	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	// Replace line 40
	h40 := hashline.LineHash(lines[39])

	handler := newTrackedHandler(t, path)
	text, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 40, Hash: h40},
			End:     hashline.Anchor{Num: 40, Hash: h40},
			Content: "REPLACED LINE 40",
		},
	})

	if isErr {
		t.Fatalf("windowed replace failed: %s", text)
	}

	// Should show windowed output (±20 around line 40 → lines 20–60)
	if !strings.Contains(text, "showing") {
		t.Errorf("large file should trigger windowed output: %s", text)
	}

	// Should NOT contain line 1 (outside window)
	if strings.Contains(text, "|line 1\n") {
		t.Errorf("windowed output should not contain line 1")
	}

	// Should contain the replaced line
	if !strings.Contains(text, "REPLACED LINE 40") {
		t.Errorf("windowed output should contain replaced content")
	}

	// File on disk should be complete
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "line 1") {
		t.Error("full file should still have line 1")
	}
	if !strings.Contains(string(got), "REPLACED LINE 40") {
		t.Error("full file should have replaced content")
	}
}

func TestEditSmallFileFullResponse(t *testing.T) {
	// Files with <=50 lines should get full output (no "showing" header).
	content := threeLineContent
	path, cleanup := setupTestFile(t, content)
	defer cleanup()

	lines := strings.Split(content, "\n")
	h1 := hashline.LineHash(lines[0])

	handler := newTrackedHandler(t, path)
	text, isErr := callEdit(t, handler, EditArgs{
		File: filepath.Base(path),
		Replace: &ReplaceOp{
			Start:   hashline.Anchor{Num: 1, Hash: h1},
			End:     hashline.Anchor{Num: 1, Hash: h1},
			Content: "REPLACED",
		},
	})

	if isErr {
		t.Fatalf("small file replace failed: %s", text)
	}

	if strings.Contains(text, "showing") {
		t.Errorf("small file should NOT trigger windowed output: %s", text)
	}
}

func TestEditCreateStringGivesHint(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	tracker := NewFileReadTracker()
	handler := NewEditHandler(tracker, nil, nil)

	// Simulate the LLM mistake: {"file":"TODO.md", "create":"some content"}
	raw := json.RawMessage(`{"file":"TODO.md","create":"some content"}`)
	result, err := handler.Handle(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError {
		t.Fatal("should fail with string create")
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "expected an object") {
		t.Errorf("should hint about object vs string: %s", text)
	}
	if !strings.Contains(text, `"create":{"content"`) {
		t.Errorf("should show correct format: %s", text)
	}
}
