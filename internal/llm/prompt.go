// Package llm implements the LLM interaction loop with tool calling support.
package llm

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xonecas/symb/internal/treesitter"
)

// Embedded prompt files
//
//go:embed anthropic.md
var anthropicPrompt string

//go:embed gemini.md
var geminiPrompt string

//go:embed qwen.md
var qwenPrompt string

//go:embed gpt.md
var gptPrompt string

// SelectPrompt returns the appropriate system prompt for the given model.
func SelectPrompt(modelID string) string {
	modelLower := strings.ToLower(modelID)

	if strings.Contains(modelLower, "claude") {
		return anthropicPrompt
	}
	if strings.Contains(modelLower, "gemini") {
		return geminiPrompt
	}
	if strings.Contains(modelLower, "gpt") || strings.Contains(modelLower, "o1") {
		return gptPrompt
	}
	if strings.Contains(modelLower, "qwen") {
		return qwenPrompt
	}

	// Default fallback
	return anthropicPrompt
}

// LoadAgentInstructions searches for AGENTS.md files in the directory hierarchy
// and returns their concatenated contents. Searches from current working directory
// up to the root, then checks user's config directory.
func LoadAgentInstructions() string {
	var instructions []string

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Search up the directory tree from CWD
	dir := cwd
	for {
		agentsPath := filepath.Join(dir, "AGENTS.md")
		if content := readFileIfExists(agentsPath); content != "" {
			header := fmt.Sprintf("Instructions from: %s", agentsPath)
			instructions = append(instructions, header+"\n"+content)
		}

		// Check if we've reached the root
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Check user's config directory (~/.config/symb/AGENTS.md)
	home, err := os.UserHomeDir()
	if err == nil {
		configAgents := filepath.Join(home, ".config", "symb", "AGENTS.md")
		if content := readFileIfExists(configAgents); content != "" {
			header := fmt.Sprintf("Instructions from: %s", configAgents)
			instructions = append(instructions, header+"\n"+content)
		}
	}

	// Reverse order so project-level takes precedence over user-level
	// (prepended to prompt, so last in list appears first)
	for i := 0; i < len(instructions)/2; i++ {
		j := len(instructions) - 1 - i
		instructions[i], instructions[j] = instructions[j], instructions[i]
	}

	return strings.Join(instructions, "\n\n")
}

// BuildSystemPrompt constructs the complete system prompt by combining
// the model-specific base prompt with any AGENTS.md instructions and
// optionally a tree-sitter project symbol outline.
func BuildSystemPrompt(modelID string, idx *treesitter.Index) string {
	basePrompt := SelectPrompt(modelID)
	agentInstructions := LoadAgentInstructions()

	var parts []string
	if agentInstructions != "" {
		parts = append(parts, agentInstructions)
	}

	// Append tree-sitter project outline if available.
	if idx != nil {
		outline := treesitter.FormatOutline(idx.Snapshot())
		if outline != "" {
			parts = append(parts, outline)
		}
	}

	parts = append(parts, basePrompt)
	return strings.Join(parts, "\n\n---\n\n")
}

// readFileIfExists reads a file if it exists, returns empty string otherwise.
func readFileIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
