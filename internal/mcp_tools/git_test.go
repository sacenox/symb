package mcp_tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/tui/editor"
)

// setupGitRepo creates a temp dir with an initialised git repo and returns
// the path along with a cleanup func that restores the original working dir.
func setupGitRepo(t *testing.T) (string, func()) {
	t.Helper()

	dir := t.TempDir()
	origDir, _ := os.Getwd()

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Initialise a git repo with a first commit so HEAD exists.
	for _, cmd := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s – %v", cmd, out, err)
		}
	}

	// Create an initial commit so we have HEAD.
	initial := filepath.Join(dir, "init.txt")
	if err := os.WriteFile(initial, []byte("init\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
	} {
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s – %v", cmd, out, err)
		}
	}

	return dir, func() { os.Chdir(origDir) } //nolint:errcheck
}

func callHandler(t *testing.T, handler func(context.Context, json.RawMessage) (*mcp.ToolResult, error), args interface{}) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	text := ""
	if len(result.Content) > 0 {
		text = result.Content[0].Text
	}
	return text, result.IsError
}

// ---------------------------------------------------------------------------
// GitStatus tests
// ---------------------------------------------------------------------------

func TestGitStatusClean(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	handler := MakeGitStatusHandler()
	text, isErr := callHandler(t, handler, GitStatusArgs{})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "clean") {
		t.Errorf("expected clean status, got: %s", text)
	}
}

func TestGitStatusWithChanges(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create an untracked file.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := MakeGitStatusHandler()
	text, isErr := callHandler(t, handler, GitStatusArgs{})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "new.txt") {
		t.Errorf("expected new.txt in status, got: %s", text)
	}
}

func TestGitStatusLongFormat(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	handler := MakeGitStatusHandler()
	// long=true → long format which contains "On branch"
	text, isErr := callHandler(t, handler, GitStatusArgs{Long: true})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	// Long format includes "On branch" header.
	if !strings.Contains(text, "On branch") {
		t.Errorf("expected long format, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// GitDiff tests
// ---------------------------------------------------------------------------

func TestGitDiffNoChanges(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	handler := MakeGitDiffHandler()
	text, isErr := callHandler(t, handler, GitDiffArgs{})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "no unstaged changes") {
		t.Errorf("expected no changes message, got: %s", text)
	}
}

func TestGitDiffUnstaged(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Modify the tracked file.
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := MakeGitDiffHandler()
	text, isErr := callHandler(t, handler, GitDiffArgs{})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "diff") {
		t.Errorf("expected diff output, got: %s", text)
	}
	if !strings.Contains(text, "modified") {
		t.Errorf("expected 'modified' in diff, got: %s", text)
	}
}

func TestGitDiffStaged(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Modify and stage.
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("staged change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "init.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %s – %v", out, err)
	}

	handler := MakeGitDiffHandler()
	text, isErr := callHandler(t, handler, GitDiffArgs{Staged: true})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "staged change") {
		t.Errorf("expected staged diff, got: %s", text)
	}
}

func TestGitDiffSpecificFile(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create and commit a second file, then modify both.
	second := filepath.Join(dir, "second.txt")
	if err := os.WriteFile(second, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"git", "add", "second.txt"},
		{"git", "commit", "-m", "add second"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s – %v", c, out, err)
		}
	}

	// Modify both files.
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("changed init\n"), 0644)
	os.WriteFile(second, []byte("changed second\n"), 0644)

	handler := MakeGitDiffHandler()

	// Diff only second.txt.
	text, isErr := callHandler(t, handler, GitDiffArgs{File: "second.txt"})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "changed second") {
		t.Errorf("expected second.txt diff, got: %s", text)
	}
	if strings.Contains(text, "changed init") {
		t.Error("should not contain init.txt changes")
	}
}

func TestGitDiffNoStagedChanges(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	handler := MakeGitDiffHandler()
	text, isErr := callHandler(t, handler, GitDiffArgs{Staged: true})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "no staged changes") {
		t.Errorf("expected no staged changes message, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// runGit edge cases
// ---------------------------------------------------------------------------

func TestRunGitNotARepo(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir) //nolint:errcheck
	os.Chdir(dir)           //nolint:errcheck

	handler := MakeGitStatusHandler()
	text, isErr := callHandler(t, handler, GitStatusArgs{})
	if !isErr {
		t.Errorf("expected error outside git repo, got: %s", text)
	}
	if !strings.Contains(text, "git error") {
		t.Errorf("expected git error message, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// ParseDiffMarkers tests
// ---------------------------------------------------------------------------

func TestParseDiffMarkersEmpty(t *testing.T) {
	m := ParseDiffMarkers("")
	if m != nil {
		t.Errorf("expected nil for empty diff, got %v", m)
	}
}

func TestParseDiffMarkersAdd(t *testing.T) {
	// Pure addition: 3 new lines starting at line 5.
	diff := `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -4,0 +5,3 @@ func foo() {
+line1
+line2
+line3
`
	m := ParseDiffMarkers(diff)
	if len(m) != 3 {
		t.Fatalf("expected 3 markers, got %d: %v", len(m), m)
	}
	for _, row := range []int{4, 5, 6} { // 0-indexed
		if m[row] != editor.GutterAdd {
			t.Errorf("row %d: expected GutterAdd, got %v", row, m[row])
		}
	}
}

func TestParseDiffMarkersDelete(t *testing.T) {
	// Pure deletion: 2 old lines removed, new file continues at line 10.
	diff := `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -8,2 +10,0 @@ func bar() {
-old1
-old2
`
	m := ParseDiffMarkers(diff)
	if len(m) != 1 {
		t.Fatalf("expected 1 marker, got %d: %v", len(m), m)
	}
	// newStart=10, newCount=0 → mark row 10-1=9 (0-indexed)
	if m[9] != editor.GutterDelete {
		t.Errorf("row 9: expected GutterDelete, got %v", m[9])
	}
}

func TestParseDiffMarkersChange(t *testing.T) {
	// Modification: 2 old lines replaced by 3 new lines starting at line 7.
	diff := `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -7,2 +7,3 @@ func baz() {
-old
-old2
+new1
+new2
+new3
`
	m := ParseDiffMarkers(diff)
	if len(m) != 3 {
		t.Fatalf("expected 3 markers, got %d: %v", len(m), m)
	}
	for _, row := range []int{6, 7, 8} { // 0-indexed
		if m[row] != editor.GutterChange {
			t.Errorf("row %d: expected GutterChange, got %v", row, m[row])
		}
	}
}

func TestParseDiffMarkersMultipleHunks(t *testing.T) {
	diff := `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -1,0 +2,1 @@
+added
@@ -10,1 +12,1 @@
-old
+changed
`
	m := ParseDiffMarkers(diff)
	if len(m) != 2 {
		t.Fatalf("expected 2 markers, got %d: %v", len(m), m)
	}
	if m[1] != editor.GutterAdd { // line 2, 0-indexed = 1
		t.Errorf("row 1: expected GutterAdd, got %v", m[1])
	}
	if m[11] != editor.GutterChange { // line 12, 0-indexed = 11
		t.Errorf("row 11: expected GutterChange, got %v", m[11])
	}
}

func TestParseDiffMarkersSingleLineNoComma(t *testing.T) {
	// When count is 1, git omits the comma: @@ -5 +5 @@
	diff := `@@ -5 +5 @@
-old
+new
`
	m := ParseDiffMarkers(diff)
	if len(m) != 1 {
		t.Fatalf("expected 1 marker, got %d: %v", len(m), m)
	}
	if m[4] != editor.GutterChange { // line 5, 0-indexed = 4
		t.Errorf("row 4: expected GutterChange, got %v", m[4])
	}
}

func TestGitFileMarkersIntegration(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Modify the tracked file.
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := GitFileMarkers(context.Background(), "init.txt")
	if m == nil {
		t.Fatal("expected markers for modified file, got nil")
	}
	if _, ok := m[0]; !ok {
		t.Errorf("expected marker on row 0, got %v", m)
	}
}
