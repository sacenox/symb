<p align="center">
  <img src="assets/symb.svg" alt="Symb Logo" width="200"/>
</p>

# Symb

_Pronounced "sim"_

A symbiotic code/pair programming TUI for developers who use agents in their main workflows.

Symb pairs you with an LLM, providing an agentic UI with tools the LLM can use to cooperate in coding and development.

# Preview

<p align="center">
  <img src="assets/preview.gif" alt="Symb Demo"/>
</p>

## Features

- **Dual-pane TUI**: Code editor on one side, agent conversation on the other
- **Syntax highlighting**: Via Chroma for Go and other languages
- **Clean aesthetic**: Dark grayscale with minimal distractions
- **ELM architecture**: Built with BubbleTea for solid state management
- **LLM integration**: Ollama local + Opencode Zen support
- **Hashline-assisted edits**: LLM edit operations use line hashes for precise, conflict-free file modifications
- **Web search**: Exa AI integration with configurable SQLite cache and content-aware redundant search prevention

## Quick Start

```bash
# Build
make build

# Run
make run

# Development mode
make dev
```

## Configuration

Edit `config.toml` to configure LLM providers, UI preferences, and cache settings:

```toml
[providers.ollama-qwen]
endpoint = "http://localhost:11434"
model = "qwen3:8b"
temperature = 0.3

[cache]
ttl_hours = 24  # how long web search/fetch results are cached
```

API keys are stored separately in `~/.config/symb/credentials.json`:

```json
{
  "providers": {
    "opencode_zen": { "api_key": "your-key" },
    "exa_ai": { "api_key": "your-exa-key" }
  }
}
```

## Development

See `docs/DESIGN.md` for architecture and design philosophy.
