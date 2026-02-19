- [ ]: Investigate:

Timeout shouldn't interrupt subagents, this is breaking the subagent tool.

```
→  SubAgent(max_iterations=8, prompt=Explore `internal/delta/delta.go` and `i…, type=explore)
→  SubAgent(max_iterations=8, prompt=Explore `internal/tui/messages.go` focus…, type=explore)
→  SubAgent(max_iterations=8, prompt=Explore `internal/llm/loop.go` for perfo…, type=explore)
→  SubAgent(max_iterations=8, prompt=Explore `internal/store/session.go` and …, type=explore)
←  sub-agent failed: LLM stream failed: Post "https://opencode.ai/zen/v1/chat/completions": http2: timeout awaiting response headers  view
←  sub-agent failed: LLM stream failed: zen: request failed with status 429  view
←  sub-agent failed: LLM stream failed: zen: request failed with status 429  view
←  sub-agent failed: LLM stream failed: Post "https://opencode.ai/zen/v1/chat/completions": http2: timeout awaiting response headers  view
```

```
←  sub-agent failed: LLM stream failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)
```

```
←  sub-agent failed: LLM stream failed: Post "https://opencode.ai/zen/v1/chat/completions": context deadline exceeded (Client.Timeout
exceeded while awaiting headers)  view
```

```
→  SubAgent(max_iterations=5, prompt=Find status bar branch name label and gi…, type=explore)
←  sub-agent failed: LLM stream failed: Post "https://opencode.ai/zen/v1/chat/completions": unexpected EOF  view
```

- [ ]: Undo tracking is too slow, every turn feels like an eternity.

We need to be smarter and take a snapshot when the user sends a new message. And save it as a restore point
instead of granularly tracking each change that happens in a turn.

- [ ]: reasoning is not shown on some models from Zen?

---

- [ ]: Resize input with mouse

- [ ]: padding for conversation pane?

- [ ]: Tool view modal fixes/improvements

- [ ]: tool calls preview in pane (toggle?)

- [ ]: Cleanup readme with recent design changes
