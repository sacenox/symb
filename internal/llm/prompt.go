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

// Embedded prompt files — shared base + model-specific overrides.
//
//go:embed prompts/base.md
var basePrompt string

//go:embed prompts/subagent_base.md
var subagentBasePrompt string

//go:embed prompts/subagent.md
var subagentPrompt string

//go:embed prompts/subagent_explore.md
var subagentExplorePrompt string

//go:embed prompts/subagent_editor.md
var subagentEditorPrompt string

//go:embed prompts/subagent_reviewer.md
var subagentReviewerPrompt string

//go:embed prompts/subagent_web.md
var subagentWebPrompt string

//go:embed prompts/anthropic.md
var anthropicPrompt string

//go:embed prompts/gemini.md
var geminiPrompt string

//go:embed prompts/qwen.md
var qwenPrompt string

//go:embed prompts/gpt.md
var gptPrompt string

// selectModelPrompt returns the model-specific override for the given model.
func selectModelPrompt(modelID string) string {
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

	// Default fallback — no model-specific additions
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

// BasePrompt returns the shared base prompt content.
func BasePrompt() string {
	return basePrompt
}

// SubAgentBasePrompt returns the base prompt content for sub-agents.
func SubAgentBasePrompt() string {
	return subagentBasePrompt
}

// SubAgentPrompt returns the base sub-agent prompt content.
func SubAgentPrompt() string {
	return subagentPrompt
}

// SubAgentTypePrompt returns the sub-agent type prompt content.
func SubAgentTypePrompt(agentType string) string {
	switch agentType {
	case "explore":
		return subagentExplorePrompt
	case "editor":
		return subagentEditorPrompt
	case "reviewer":
		return subagentReviewerPrompt
	case "web":
		return subagentWebPrompt
	default:
		return subagentPrompt
	}
}

// BuildSystemPrompt constructs the complete system prompt:
// 1. Base prompt (shared across all models)
// 2. Model-specific overrides
// 3. AGENTS.md instructions
// 4. Tree-sitter project outline
func BuildSystemPrompt(modelID string, idx *treesitter.Index) string {
	parts := []string{basePrompt}

	if modelOverride := selectModelPrompt(modelID); modelOverride != "" {
		parts = append(parts, modelOverride)
	}

	if agentInstructions := LoadAgentInstructions(); agentInstructions != "" {
		parts = append(parts, agentInstructions)
	}

	if idx != nil {
		if outline := treesitter.FormatOutline(idx.Snapshot()); outline != "" {
			parts = append(parts, outline)
		}
	}

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
