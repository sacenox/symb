- [ ] Encourage more subagent usage:

Update the prompts so the main agent is more of an orchestrator and the subagents do code searching/editting/commands etc.
This makes the most of subagents for context control.
Teach conflict free workflows for subagents
Trim subagent system prompt, align it with the new encouragement, perform the task from the main agent.

After the changes above and some testing:
speciallize subagent_prompt.md into separate internal agent types: Grepper, Editor, Reviewer, Tasker, Webfinder.
update subagent to support an optional type that uses these specialized subagent prompts, defaults to Tasker for non-specific tasks.

- [ ] LLM needs to improve todo usage!

- [ ] Investigate empty messages with some models

- [ ] Refactor provider layer:

Refactor opencode provider:
 - cover all model options: https://opencode.ai/docs/zen). Fix current issues and misleading comments.
 - ensure streaming + reasoning + tool call support is solid accross provider different formats.
 - Add functionality to list available models and variants (xtra/medium/low/etc)

Ollama changes:

 - ensure streaming + reasoning + tool call support is solid for ollama models.
 - Add functionality to list available models and variants

Update config usage:

- Config only has upstream providers: ollama or opencode zen.
- Config set's api key to use from `credentials.json`:

```
[provider.zen]
endpoint: [url]
api_key: [name] # optional

[provider.ollama]
endpoint: [url]
```

Add new modal, list available models, grouped by configured providers. When a model is selected it
becomes the active model, statusbar updates correctly, future messages go to new model.

Benefits: User can now switch between providers/models freely. Multiple providers connected.

Tackle in 3 phases:

# Phase 1
1. Refactor opencode provider
2. Update config format
3. Modal implementation and model switching

# Phase 2
Manual testing with user

# Phase 3
Refactor ollama

- [ ] Cleanup codebase:

Aggregate common helpers in packages
Breakdown visual tui components into their own tui files.
Separate modals into their own files (filesearch, keybinds, and base modal)
Find files larger than 750 lines write report for analysis


- [ ] Investigate reasoning messages missing from opencode provider but working with ollama. (Check if any opencode model shows reasoning).
