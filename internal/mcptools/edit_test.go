package mcptools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xonecas/symb/internal/hashline"
)

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
	return NewEditHandler(tracker, nil)
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
	content := "line one\nline two\nline three"
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
	handler := NewEditHandler(tracker, nil)
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
	handler := NewEditHandler(tracker, nil)
	_, isErr := callEdit(t, handler, EditArgs{
		File:   "test.go",
		Create: &CreateOp{Content: "new content"},
	})

	if !isErr {
		t.Error("create should fail if file already exists")
	}
}

func TestEditHashMismatch(t *testing.T) {
	content := "line one\nline two\nline three"
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
	handler := NewEditHandler(tracker, nil)
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
	handler := NewEditHandler(tracker, nil)
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
	handler := NewEditHandler(tracker, nil)

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
	handler := NewEditHandler(tracker, nil)
	_, isErr := callEdit(t, handler, EditArgs{
		File:   "brand-new.go",
		Create: &CreateOp{Content: "package new\n"},
	})

	if isErr {
		t.Fatal("create should not require prior read")
	}
}
