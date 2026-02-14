package filesearch

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GitignoreMatcher matches paths against gitignore patterns.
type GitignoreMatcher struct {
	patterns []*gitignorePattern
}

type gitignorePattern struct {
	pattern  string
	regex    *regexp.Regexp
	negation bool
	dirOnly  bool
	anchored bool
}

// NewGitignoreMatcher creates a new gitignore matcher from a .gitignore file.
func NewGitignoreMatcher(gitignorePath string) (*GitignoreMatcher, error) {
	matcher := &GitignoreMatcher{}

	if gitignorePath == "" {
		return matcher, nil
	}

	file, err := os.Open(gitignorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return matcher, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		pattern := parseGitignorePattern(line)
		if pattern != nil {
			matcher.patterns = append(matcher.patterns, pattern)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return matcher, nil
}

// Matches checks if a path should be ignored.
func (m *GitignoreMatcher) Matches(path string, isDir bool) bool {
	if m == nil || len(m.patterns) == 0 {
		return false
	}

	// Normalize path separators to forward slashes
	path = filepath.ToSlash(path)

	var lastMatch bool
	for _, pattern := range m.patterns {
		// For directory-only patterns, check if path is or is within that directory
		if pattern.dirOnly {
			if isDir && pattern.regex.MatchString(path) {
				lastMatch = !pattern.negation
			} else if !isDir && pattern.regex.MatchString(filepath.Dir(path)) {
				// File within directory
				lastMatch = !pattern.negation
			}
			continue
		}

		// For anchored patterns, only match against full path
		if pattern.anchored {
			if pattern.regex.MatchString(path) {
				lastMatch = !pattern.negation
			}
		} else {
			// For non-anchored, try both full path and basename
			if pattern.regex.MatchString(path) || pattern.regex.MatchString(filepath.Base(path)) {
				lastMatch = !pattern.negation
			}
		}
	}

	return lastMatch
}

// parseGitignorePattern converts a gitignore pattern to a regex.
func parseGitignorePattern(pattern string) *gitignorePattern {
	original := pattern
	negation := false
	dirOnly := false
	anchored := false

	// Handle negation
	if strings.HasPrefix(pattern, "!") {
		negation = true
		pattern = pattern[1:]
	}

	// Check for anchored pattern
	if strings.HasPrefix(pattern, "/") {
		anchored = true
	}

	// Handle directory-only patterns
	if strings.HasSuffix(pattern, "/") {
		dirOnly = true
		pattern = strings.TrimSuffix(pattern, "/")
	}

	// Convert gitignore glob to regex
	regexPattern := gitignoreGlobToRegex(pattern)
	regex, err := regexp.Compile(regexPattern)
	if err != nil {
		// If pattern is invalid, skip it
		return nil
	}

	return &gitignorePattern{
		pattern:  original,
		regex:    regex,
		negation: negation,
		dirOnly:  dirOnly,
		anchored: anchored,
	}
}

// gitignoreGlobToRegex converts a gitignore glob pattern to a regex.
func gitignoreGlobToRegex(pattern string) string {
	var result strings.Builder

	anchored := false
	if strings.HasPrefix(pattern, "/") {
		result.WriteString("^")
		pattern = pattern[1:]
		anchored = true
	} else {
		result.WriteString("(^|/)")
	}

	for i := 0; i < len(pattern); {
		i += convertGlobChar(&result, pattern, i)
	}

	if anchored {
		result.WriteString("$")
	} else {
		result.WriteString("(/.*)?$")
	}
	return result.String()
}

// convertGlobChar writes the regex equivalent of pattern[i] into b and returns
// the number of characters consumed.
func convertGlobChar(b *strings.Builder, pattern string, i int) int {
	ch := pattern[i]
	switch ch {
	case '*':
		return convertGlobStar(b, pattern, i)
	case '?':
		b.WriteString("[^/]")
		return 1
	case '.', '+', '(', ')', '|', '^', '$', '@', '%':
		b.WriteByte('\\')
		b.WriteByte(ch)
		return 1
	case '[':
		return convertGlobCharClass(b, pattern, i)
	case '\\':
		if i+1 < len(pattern) {
			b.WriteByte('\\')
			b.WriteByte(pattern[i+1])
			return 2
		}
		b.WriteString("\\\\")
		return 1
	default:
		b.WriteByte(ch)
		return 1
	}
}

func convertGlobStar(b *strings.Builder, pattern string, i int) int {
	if i+1 < len(pattern) && pattern[i+1] == '*' {
		if i+2 < len(pattern) && pattern[i+2] == '/' {
			b.WriteString("(.*/)?")
			return 3
		}
		b.WriteString(".*")
		return 2
	}
	b.WriteString("[^/]*")
	return 1
}

func convertGlobCharClass(b *strings.Builder, pattern string, i int) int {
	j := i + 1
	for j < len(pattern) && pattern[j] != ']' {
		j++
	}
	if j < len(pattern) {
		b.WriteString(pattern[i : j+1])
		return j + 1 - i
	}
	b.WriteString("\\[")
	return 1
}
