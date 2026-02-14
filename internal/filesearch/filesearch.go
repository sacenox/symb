// Package filesearch provides file and content search with fuzzy matching and gitignore support.
package filesearch

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Result represents a single search result.
type Result struct {
	Path    string // Relative path from search root
	Line    int    // Line number (1-indexed), 0 for file-only matches
	Content string // Line content, empty for file-only matches
}

// Options configures the search behavior.
type Options struct {
	Pattern       string // Pattern to search for (filename or content)
	ContentSearch bool   // If true, search file contents; otherwise just filenames
	MaxResults    int    // Maximum results to return (0 = unlimited)
	CaseSensitive bool   // Case-sensitive matching
	RootDir       string // Root directory to search from (defaults to current dir)
}

// Searcher performs file and content searches.
type Searcher struct {
	gitignore *GitignoreMatcher
}

// NewSearcher creates a new searcher for the given root directory.
func NewSearcher(rootDir string) (*Searcher, error) {
	gitignorePath := filepath.Join(rootDir, ".gitignore")
	matcher, err := NewGitignoreMatcher(gitignorePath)
	if err != nil {
		// Non-fatal: just won't filter gitignored files
		matcher, _ = NewGitignoreMatcher("")
	}

	return &Searcher{
		gitignore: matcher,
	}, nil
}

// Search performs a search with the given options.
func (s *Searcher) Search(ctx context.Context, opts Options) ([]Result, error) {
	if opts.RootDir == "" {
		var err error
		opts.RootDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
	}

	pattern := opts.Pattern
	if !opts.CaseSensitive {
		pattern = "(?i)" + pattern
	}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	var results []Result
	err = filepath.WalkDir(opts.RootDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if skip := s.shouldSkip(path, d, opts.RootDir); skip != nil {
			return *skip
		}
		matches := s.matchEntry(path, opts.RootDir, regex, opts.ContentSearch)
		results = append(results, matches...)
		if opts.MaxResults > 0 && len(results) >= opts.MaxResults {
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return nil, err
	}
	return results, nil
}

const maxSearchFileSize = 10 * 1024 * 1024 // 10 MB

// shouldSkip decides whether to skip a directory entry. Returns nil to proceed,
// or a pointer to the error to return from the walk callback.
func (s *Searcher) shouldSkip(path string, d os.DirEntry, rootDir string) *error {
	relPath, err := filepath.Rel(rootDir, path)
	if err != nil {
		skip := error(nil)
		return &skip
	}
	if d.IsDir() && d.Name() == ".git" {
		skip := filepath.SkipDir
		return &skip
	}
	if s.gitignore.Matches(relPath, d.IsDir()) {
		if d.IsDir() {
			skip := filepath.SkipDir
			return &skip
		}
		skip := error(nil)
		return &skip
	}
	if d.IsDir() {
		skip := error(nil)
		return &skip
	}
	info, err := d.Info()
	if err != nil || info.Size() > maxSearchFileSize {
		skip := error(nil)
		return &skip
	}
	return nil
}

// matchEntry checks a single file against the search pattern.
func (s *Searcher) matchEntry(path, rootDir string, regex *regexp.Regexp, contentSearch bool) []Result {
	relPath, _ := filepath.Rel(rootDir, path)
	if contentSearch {
		matches, err := s.searchFileContent(path, relPath, regex)
		if err != nil {
			return nil
		}
		return matches
	}
	if regex.MatchString(filepath.Base(path)) || regex.MatchString(relPath) {
		return []Result{{Path: relPath}}
	}
	return nil
}

// searchFileContent searches a single file for pattern matches.
func (s *Searcher) searchFileContent(absPath, relPath string, regex *regexp.Regexp) ([]Result, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var results []Result
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip binary files (heuristic: check for null bytes)
		if strings.Contains(line, "\x00") {
			return nil, nil
		}

		if regex.MatchString(line) {
			results = append(results, Result{
				Path:    relPath,
				Line:    lineNum,
				Content: line,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return results, nil
}
