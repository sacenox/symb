- [ ] Cleanup codebase:

Aggregate common helpers in packages
Breakdown visual tui components into their own tui files.
Separate modals into their own files (filesearch, keybinds, and base modal)
Find files larger than 750 lines write report for analysis

- [ ] Encourage more subagent usage:

Update the prompts so the main agent is more of an orchestrator and the subagents do code searching/editting/commands etc.
This makes the most of subagents for context control.
Teach conflict free workflows for subagents
Trim subagent system prompt, align it with the new encouragement, perform the task from the main agent.
speciallize subagent_prompt.md into separate internal agent types: Grepper, Editor, Reviewer, Tasker, Webfinder.
update subagent to support an optional type that uses these specialized subagent prompts, defaults to Tasker for non-specific tasks.

- [ ] Enforce todo usage!

- [ ] Investigate empty messages with some models

- [ ] Investigate reasoning messages missing from opencode provider but working with ollama. (Check if any opencode model shows reasoning).
