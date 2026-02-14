package highlight

import (
	"path/filepath"
	"strings"
)

// DetectLanguage returns the Chroma language identifier based on file extension.
func DetectLanguage(path string) string {
	// Map common extensions to Chroma language identifiers
	languageMap := map[string]string{
		".go":         "go",
		".py":         "python",
		".js":         "javascript",
		".ts":         "typescript",
		".jsx":        "jsx",
		".tsx":        "tsx",
		".java":       "java",
		".c":          "c",
		".cpp":        "cpp",
		".cc":         "cpp",
		".h":          "c",
		".hpp":        "cpp",
		".cs":         "csharp",
		".rb":         "ruby",
		".php":        "php",
		".rs":         "rust",
		".swift":      "swift",
		".kt":         "kotlin",
		".scala":      "scala",
		".sh":         "bash",
		".bash":       "bash",
		".zsh":        "zsh",
		".fish":       "fish",
		".ps1":        "powershell",
		".r":          "r",
		".sql":        "sql",
		".html":       "html",
		".htm":        "html",
		".xml":        "xml",
		".css":        "css",
		".scss":       "scss",
		".sass":       "sass",
		".less":       "less",
		".json":       "json",
		".yaml":       "yaml",
		".yml":        "yaml",
		".toml":       "toml",
		".ini":        "ini",
		".conf":       "nginx",
		".md":         "markdown",
		".markdown":   "markdown",
		".tex":        "tex",
		".vim":        "vim",
		".lua":        "lua",
		".perl":       "perl",
		".pl":         "perl",
		".dockerfile": "docker",
		".proto":      "protobuf",
	}

	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := languageMap[ext]; ok {
		return lang
	}

	// Check for specific filenames
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile":
		return "docker"
	case "makefile":
		return "make"
	case "gemfile":
		return "ruby"
	case "rakefile":
		return "ruby"
	}

	return "text" // Default fallback
}
