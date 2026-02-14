package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
