package treesitter

import (
	"os"
	"testing"
)

func TestIndexBuild(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Walk up to project root (internal/treesitter -> project root)
	root := cwd + "/../.."

	idx := NewIndex(root)
	if err := idx.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	files := idx.Files()
	if len(files) == 0 {
		t.Fatal("no files indexed")
	}
	t.Logf("Indexed %d files", len(files))

	snap := idx.Snapshot()
	outline := FormatOutline(snap)
	if outline == "" {
		t.Fatal("empty outline")
	}
	t.Logf("Outline (%d bytes):\n%s", len(outline), outline)
}
