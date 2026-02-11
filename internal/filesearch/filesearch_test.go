package filesearch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchFilenames(t *testing.T) {
	// Create temp directory with test files
	tmpDir := t.TempDir()

	files := []string{
		"main.go",
		"cmd/server/main.go",
		"internal/config/config.go",
		"internal/handler/handler.go",
		"README.md",
		"docs/design.md",
	}

	for _, f := range files {
		path := filepath.Join(tmpDir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	searcher, err := NewSearcher(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		pattern       string
		expectCount   int
		expectMatches []string
	}{
		{
			name:          "find all go files",
			pattern:       `\.go$`,
			expectCount:   4,
			expectMatches: []string{"main.go", "cmd/server/main.go"},
		},
		{
			name:          "find config",
			pattern:       `config`,
			expectCount:   1,
			expectMatches: []string{"internal/config/config.go"},
		},
		{
			name:          "find markdown files",
			pattern:       `\.md$`,
			expectCount:   2,
			expectMatches: []string{"README.md", "docs/design.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := searcher.Search(context.Background(), Options{
				Pattern:       tt.pattern,
				ContentSearch: false,
				RootDir:       tmpDir,
			})

			if err != nil {
				t.Fatalf("search failed: %v", err)
			}

			if len(results) != tt.expectCount {
				t.Errorf("expected %d results, got %d", tt.expectCount, len(results))
			}

			for _, expected := range tt.expectMatches {
				found := false
				for _, r := range results {
					if r.Path == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected to find %q in results", expected)
				}
			}
		})
	}
}

func TestSearchContent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files with content
	testFiles := map[string]string{
		"file1.txt": "hello world\nfoo bar\nbaz",
		"file2.txt": "hello universe\ntest line",
		"file3.txt": "no match here",
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	searcher, err := NewSearcher(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	results, err := searcher.Search(context.Background(), Options{
		Pattern:       `hello`,
		ContentSearch: true,
		RootDir:       tmpDir,
	})

	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	// Should find "hello" in file1.txt and file2.txt
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Line == 0 {
			t.Error("expected line number to be set")
		}
		if r.Content == "" {
			t.Error("expected content to be set")
		}
	}
}

func TestGitignore(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .gitignore
	gitignore := `*.log
node_modules/
dist/
`
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(gitignore), 0644); err != nil {
		t.Fatal(err)
	}

	// Create test files
	files := []string{
		"main.go",
		"test.log",
		"node_modules/package.json",
		"dist/bundle.js",
	}

	for _, f := range files {
		path := filepath.Join(tmpDir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	searcher, err := NewSearcher(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	results, err := searcher.Search(context.Background(), Options{
		Pattern:       `.*`,
		ContentSearch: false,
		RootDir:       tmpDir,
	})

	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	// Should find main.go and .gitignore (others are gitignored)
	foundMain := false
	for _, r := range results {
		if r.Path == "main.go" {
			foundMain = true
		}
		// Skip .gitignore in validation
		if r.Path != "main.go" && r.Path != ".gitignore" {
			t.Errorf("unexpected file found: %s (should be gitignored)", r.Path)
		}
	}

	if !foundMain {
		t.Error("expected to find main.go in results")
	}
}

func TestMaxResults(t *testing.T) {
	tmpDir := t.TempDir()

	// Create many files
	for i := 0; i < 20; i++ {
		path := filepath.Join(tmpDir, filepath.Join("file", string(rune('a'+i))+".txt"))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	searcher, err := NewSearcher(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	results, err := searcher.Search(context.Background(), Options{
		Pattern:       `\.txt$`,
		ContentSearch: false,
		MaxResults:    5,
		RootDir:       tmpDir,
	})

	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 5 {
		t.Errorf("expected 5 results (max), got %d", len(results))
	}
}
