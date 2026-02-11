package filesearch

import "testing"

func TestGitignoreMatcher(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		isDir   bool
		want    bool
	}{
		// Simple patterns
		{"*.log", "test.log", false, true},
		{"*.log", "test.txt", false, false},
		{"*.log", "logs/test.log", false, true},

		// Directory patterns
		{"node_modules/", "node_modules", true, true},
		{"node_modules/", "node_modules/package.json", false, true},
		{"node_modules/", "src/node_modules", true, true},

		// Wildcard patterns
		{"build/*", "build/output.txt", false, true},
		{"build/*", "build", true, false},
		{"build/*", "src/build/output.txt", false, true},

		// Negation patterns
		{"!important.log", "important.log", false, false},

		// Double asterisk
		{"**/temp", "temp", false, true},
		{"**/temp", "src/temp", false, true},
		{"**/temp", "src/lib/temp", false, true},

		// Leading slash (root-only)
		{"/root.txt", "root.txt", false, true},
		{"/root.txt", "src/root.txt", false, false},
	}

	for _, tt := range tests {
		pattern := parseGitignorePattern(tt.pattern)
		if pattern == nil {
			t.Errorf("failed to parse pattern: %s", tt.pattern)
			continue
		}

		matcher := &GitignoreMatcher{patterns: []*gitignorePattern{pattern}}
		got := matcher.Matches(tt.path, tt.isDir)

		if got != tt.want {
			t.Errorf("pattern %q, path %q (isDir=%v): got %v, want %v",
				tt.pattern, tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestGitignoreMultiplePatterns(t *testing.T) {
	patterns := []string{
		"*.log",
		"!important.log",
	}

	matcher := &GitignoreMatcher{}
	for _, p := range patterns {
		pattern := parseGitignorePattern(p)
		if pattern != nil {
			matcher.patterns = append(matcher.patterns, pattern)
		}
	}

	tests := []struct {
		path string
		want bool
	}{
		{"test.log", true},       // Ignored by *.log
		{"important.log", false}, // Un-ignored by !important.log
		{"other.txt", false},     // Not matched
	}

	for _, tt := range tests {
		got := matcher.Matches(tt.path, false)
		if got != tt.want {
			t.Errorf("path %q: got %v, want %v", tt.path, got, tt.want)
		}
	}
}
