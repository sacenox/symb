# Subagent Types

This project supports specialized subagents to keep context focused and limit tool use.

## Types

### explore
Read-only codebase exploration.

Allowed tools:
- Read
- Grep
- Shell

Default tool rounds: 10

### editor
Surgical code changes only.

Allowed tools:
- Read
- Edit
- Grep
- Shell

Default tool rounds: 8

### reviewer
Code review, read-only.

Allowed tools:
- Read
- Grep
- Shell

Default tool rounds: 10

### web
Documentation and API research.

Allowed tools:
- WebSearch
- WebFetch

Default tool rounds: 5

## Usage

Example tool call:

```json
{"prompt": "Find all error handling in internal/tui/", "type": "explore"}
```

If type is omitted, the subagent runs with the default tool set and 5 tool rounds.
