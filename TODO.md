- [ ]: Clean up prompts:

Move tool usage to the tool description instead of duplicating instructions.
Compact prompts to the essentials, leave personal customization for the agents.md files.

- [ ]: Improve our internal review subagent: https://blakesmith.me/2015/02/09/code-review-essentials-for-software-teams.html

- [ ]: Resize input with mouse

- [ ]: padding for conversation pane?

- [ ]: Tool view modal fixes/improvements

- [ ]: tool calls preview in pane (toggle?)

- [ ]: Cleanup readme with recent design changes

- [ ]: Investigate:

Timeout shouldn't interrupt subagents.

```
←  sub-agent failed: LLM stream failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)
```

We just did a big change in the codebase, new provider stack,
