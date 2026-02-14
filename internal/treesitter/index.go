package treesitter

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/xonecas/symb/internal/filesearch"
)

// Index holds a project-wide symbol map built from tree-sitter parsing.
type Index struct {
	mu    sync.RWMutex
	files map[string][]Symbol // relPath -> symbols
	root  string
}

// NewIndex creates an empty index rooted at dir.
func NewIndex(root string) *Index {
	return &Index{
		files: make(map[string][]Symbol),
		root:  root,
	}
}

// Build walks the project tree, parsing every supported file.
// Respects .gitignore via filesearch.GitignoreMatcher.
func (idx *Index) Build() error {
	gitignorePath := filepath.Join(idx.root, ".gitignore")
	matcher, err := filesearch.NewGitignoreMatcher(gitignorePath)
	if err != nil {
		matcher, _ = filesearch.NewGitignoreMatcher("")
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	return filepath.WalkDir(idx.root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(idx.root, path)
		if err != nil {
			return nil
		}

		// Skip .git and gitignored paths.
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if matcher.Matches(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if matcher.Matches(rel, false) {
			return nil
		}
		if !Supported(path) {
			return nil
		}

		// Skip large files (>1MB).
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}

		syms, err := ParseFile(path)
		if err != nil || len(syms) == 0 {
			return nil
		}
		idx.files[rel] = syms
		return nil
	})
}

// UpdateFile re-parses a single file and updates the index.
func (idx *Index) UpdateFile(absPath string) {
	rel, err := filepath.Rel(idx.root, absPath)
	if err != nil || !Supported(absPath) {
		return
	}
	syms, err := ParseFile(absPath)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	if err != nil || len(syms) == 0 {
		delete(idx.files, rel)
		return
	}
	idx.files[rel] = syms
}

// Files returns a snapshot of all indexed file paths (sorted is not guaranteed).
func (idx *Index) Files() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	paths := make([]string, 0, len(idx.files))
	for p := range idx.files {
		paths = append(paths, p)
	}
	return paths
}

// Symbols returns symbols for a given relative path.
func (idx *Index) Symbols(relPath string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.files[relPath]
}

// Snapshot returns a copy of the full index map.
func (idx *Index) Snapshot() map[string][]Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make(map[string][]Symbol, len(idx.files))
	for k, v := range idx.files {
		out[k] = v
	}
	return out
}
