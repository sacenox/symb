- [ ]: Investigate:

Timeout shouldn't interrupt subagents, this is breaking the subagent tool.

```
←  sub-agent failed: LLM stream failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)
```

```
←  sub-agent failed: LLM stream failed: Post "https://opencode.ai/zen/v1/chat/completions": context deadline exceeded (Client.Timeout
exceeded while awaiting headers)  view
```

- [ ]: Undo tracking is too slow, every turn feels like an eternity.

We need to be smarter and take a snapshot when the user sends a new message. And save it as a restore point
instead of granularly tracking each change that happens in a turn.

---

- [ ]: Resize input with mouse

- [ ]: padding for conversation pane?

- [ ]: Tool view modal fixes/improvements

- [ ]: tool calls preview in pane (toggle?)

- [ ]: Cleanup readme with recent design changes
