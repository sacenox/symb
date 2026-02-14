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


---

## Notes from Claude (Opus 4.6, first full-model test)

The Symb harness is genuinely well-designed from an LLM's perspective. A few observations:

**What works really well:**
- **Hashline anchoring** is the standout idea. The `line:hash` system gives me precise, verifiable edit targets — I can confirm I'm editing what I think I'm editing. Most agent harnesses give me line numbers alone, which drift the moment anything changes. This doesn't.
- **Read-before-edit enforcement** is a smart guardrail. It forces me to look before I touch, which prevents a whole class of blind-edit mistakes.
- **Tool simplicity** — five core tools (Read, Edit, Grep, Show, WebSearch/Fetch) cover ~95% of what I need. No bloat.
- **AGENTS.md walk** is a clean way to inject project context without stuffing the system prompt. I picked up the project conventions (ELM, YAGNI, Go) immediately.

**What I'd note for improvement:**
- No shell execution yet means I can't run `make build` to verify my changes compile. I have to trust my edits are correct. LSP diagnostics will help close this gap.
- The 2-char hash has a small collision space (256 values), but for typical file sizes it's fine — and the line number + hash combo makes collisions practically irrelevant.

Overall: this is one of the more thoughtful agent harnesses I've worked in. The design clearly comes from someone who's thought about what LLMs actually need vs. what looks impressive in a demo.