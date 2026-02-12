# Symb System Prompts Documentation

This document details the system prompt architecture used in Symb, including model-specific optimizations and prompt engineering patterns.

## Overview

Symb uses a **model-specific prompting strategy** where each LLM family receives an optimized system prompt tailored to its strengths and characteristics. This approach maximizes performance while maintaining consistent behavior across providers.

## Architecture

### Prompt Location

System prompts are stored in `internal/llm/`:

- `anthropic.md` - For Claude models (Anthropic)
- `gemini.md` - For Gemini models (Google)
- `qwen.md` - For Qwen models (Alibaba Cloud)
- `gpt.md` - For GPT models (OpenAI)

### Selection Logic

The appropriate prompt is selected based on the configured provider's model identifier:

```go
func selectPrompt(modelID string) string {
    if strings.Contains(modelID, "claude") {
        return loadPrompt("anthropic.md")
    }
    if strings.Contains(modelID, "gemini") {
        return loadPrompt("gemini.md")
    }
    if strings.Contains(modelID, "gpt") || strings.Contains(modelID, "o1") {
        return loadPrompt("gpt.md")
    }
    if strings.Contains(modelID, "qwen") {
        return loadPrompt("qwen.md")
    }
    return loadPrompt("anthropic.md") // Default fallback
}
```

## References

**Inspiration:**

- OpenCode system prompts (anthropic.txt, gemini.txt, etc.)
- Anthropic prompt engineering guide
- OpenAI best practices documentation

**Related Docs:**

- `docs/TOOLS.md` - Tool implementation details
- `docs/DESIGN.md` - Architecture overview
- `internal/llm/loop.go` - LLM interaction implementation
