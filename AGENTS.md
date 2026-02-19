# Rules

**ALWAYS USE YAGNI**


- The user will ask when he wants documentation or summaries written.
- Short answers only, the user will ask for more details if he wants.

**DON'T EDIT DOCS/DESIGN.md UNLESS IT IS THE USERS REQUEST**

## Project info

- Always use [ELM](https://guide.elm-lang.org/architecture/) architecture.
- Go & Golintci
- [Bubbletea, Bubbles](https://github.com/charmbracelet/bubbletea)
- [Lipgloss](https://github.com/charmbracelet/lipgloss).
- Use Makefile commands to build, lint and test.
- Use `journalctl -u ollama --no-pager -n 100` to see ollama logs
- Data directory: `~/.config/symb/` (cache.db, credentials.json, logs/, user config file)
