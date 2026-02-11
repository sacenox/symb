<p align="center">
  <img src="assets/symb.svg" alt="Symb Logo" width="200"/>
</p>

# Symb

_Pronounced "sim"_

A symbiotic code/pair programming TUI for developers who use agents in their main workflows.

Symb pairs you with an LLM, providing an agentic UI with tools the LLM can use to cooperate in coding and development.

## Features

- **Dual-pane TUI**: Code editor on one side, agent conversation on the other
- **Syntax highlighting**: Via Chroma for Go and other languages
- **Clean aesthetic**: Dark grayscale with minimal distractions
- **ELM architecture**: Built with BubbleTea for solid state management
- **LLM integration**: Ollama local + Opencode Zen support

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

Edit `config.toml` to configure LLM provider, editor settings, and UI preferences.

## Development

See `docs/DESIGN.md` for architecture and design philosophy.
