# Performance issues

Ordered by severity (most impactful first), based on investigation evidence.

1. **Per-edit SQLite writes for undo tracking (blocking, frequent).**
   - `delta.Tracker.RecordModify`/`RecordCreate` hit SQLite under a mutex per edit (SELECT + INSERT), likely dominating turn time during edits.
   - Evidence: `internal/delta/delta.go:33-39:a0`, `internal/delta/delta.go:48-90:a0`.

2. **Snapshotting on the LLM turn path (filesystem-heavy).**
   - Pre/post snapshots run inline each turn; full directory snapshotting adds latency.
   - Evidence: `internal/tui/messages.go:284:5a`, `internal/tui/messages.go:303:ed`, `internal/tui/messages.go:340:54-356:0f`.

3. **Sequential tool execution + high tool round cap.**
   - Tool calls are executed sequentially; slow tools block the turn.
   - Default `MaxToolRounds = 30` can cause multiple full LLM calls per turn.
   - Evidence: `internal/llm/loop.go:303-355:05`, `internal/llm/loop.go:122-166:f6`.

4. **Session save/load serialization with per-message inserts.**
   - Global mutex held across transaction; per-message `Exec` in loop.
   - Busy retry sleeps can add delay.
   - Evidence: `internal/store/session.go:71-85:fc`, `internal/store/session.go:96-155:fc`, `internal/store/session.go:158-199:d3`.

5. **O(n²) string concatenation during stream accumulation.**
   - Content and tool args concatenated per delta, causing quadratic behavior for long outputs.
   - Evidence: `internal/llm/loop.go:243-246:ea`, `internal/llm/loop.go:258-295:6c`.

6. **Per-delta UI dispatch overhead.**
   - One channel send per delta; can cause backpressure under high token rates.
   - Evidence: `internal/tui/messages.go:359:6c-365:a3`.

7. **LSP initialization/diagnostic waits block.**
   - LSP init can block up to 15s per server; notify waits for diagnostics (debounce + timeout).
   - Evidence: `internal/lsp/manager.go:182-195`, `internal/lsp/manager.go:221-226`, `internal/lsp/client.go:144-175`.

8. **File search overhead.**
   - WalkDir with per-entry stat, regex gitignore per entry, and full file scans even after `MaxResults`.
   - Evidence: `internal/filesearch/filesearch.go:69-126`, `internal/filesearch/filesearch.go:158-179`, `internal/filesearch/gitignore.go:73-94`.
