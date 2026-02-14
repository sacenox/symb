# LSP Integration TODO

## Current State
- Go works end-to-end via gopls (closed-loop diagnostics + red/yellow line numbers)
- Uses charmbracelet/x/powernap for LSP client management
- Powernap ships with 300+ server configs via lsps.json

## TODO

### lookPath coverage
- `lookPath` checks `$PATH`, `$GOBIN`, `$GOPATH/bin`, `~/go/bin`, `~/.cargo/bin`, `~/.local/bin`
- Missing: npm global bin (`~/.npm/bin`), nvm dirs (`~/.nvm/versions/node/*/bin`), pipx (`~/.local/pipx/venvs/*/bin`)
- Consider reading `npm prefix -g` or `$NVM_DIR` at startup

### Test with other languages
- [ ] TypeScript — `typescript-language-server --stdio` (npm install -g typescript-language-server typescript)
- [ ] Python — `pyright` or `pylsp` (pip install pyright / python-lsp-server)
- [ ] Rust — `rust-analyzer` (~/.cargo/bin, ships with rustup)
- [ ] C/C++ — `clangd` (system package)
- [ ] Lua — `lua-language-server`

### Future features (not now)
- LLM tools: `lsp_diagnostics`, `lsp_references`, `lsp_restart`
- User-configurable LSP servers in config.toml
- Auto-install common LSP servers
- Statusbar error/warning count
- Workspace/applyEdit support
- Unit tests for `internal/lsp/` pure functions (FormatDiagnostics, diagLineSeverities, findRoot, lookPath)
