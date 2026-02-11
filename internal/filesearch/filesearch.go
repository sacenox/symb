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

	// Compile regex pattern
	pattern := opts.Pattern
	if !opts.CaseSensitive {
		pattern = "(?i)" + pattern
	}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	var results []Result
	err = filepath.WalkDir(opts.RootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		relPath, err := filepath.Rel(opts.RootDir, path)
		if err != nil {
			return nil
		}

		// Skip .git directory
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		// Check gitignore
		if s.gitignore.Matches(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Check file size (skip large files)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > 10*1024*1024 { // Skip files > 10MB
			return nil
		}

		if opts.ContentSearch {
			// Search file contents
			matches, err := s.searchFileContent(path, relPath, regex)
			if err != nil {
				return nil // Skip files we can't read
			}
			results = append(results, matches...)
		} else {
			// Search filename only
			if regex.MatchString(filepath.Base(path)) || regex.MatchString(relPath) {
				results = append(results, Result{
					Path: relPath,
				})
			}
		}

		// Check max results
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
