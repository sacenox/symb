
# TODO write cancels assistant message

- Session: 017549ac3301e35884ed78aa7063ead8

```
Error: LLM stream failed: stream request status 400:
{"type":"error","error":{"type":"invalid_request_error","message":"messages.38.content.1: unexpected `tool_use_id` found in `tool_result`
blocks: toolu_01Mq8eCePrJUNK7eQJU2MopD. Each `tool_result` block must have a corresponding `tool_use` block in the previous
message."},"request_id":"req_011CYArEN9fedd36dxC3Mr8i"}event: ping
data: {"type":"ping","cost":"0"}
```

Error flow:

Do some work, a few messages all good.
When work is done, last assistant message is blank, last action was a tool call to TodoWrite that returned an error.

User sends another message, all works.

**User exits and resumes**

Can't send new messages, see's the error above.

FACTS:

- Happened after a tool call with no assistant message as the last message. (this is a bug alone).
- Same symptoms as the other "no assistant message at the end" scenarios as:
  - `ESC` (adds a syntetic message "User interrupted") to avoid this issue
  - and CTRL+c which is un-addressed(but not the cause here, not used in this error flow)
- Before restoring, the session resumes gracefully, message ordering is correct. (in memory?)
- After restoring from the db, the session is broken and the error complains about missing tool calls.

Conclusion: DB message order and "live" session message order are not a match, they aren't the same.

Fix: Need to investigate the assistent reply with no content, what happened?, And could empty content messages with only tool calls being filtered out?
     This same symptom was/is present when the assistant response is interrupted or the app exists with CTRL+C, maybe it's the same bug. causing the mismatch between db and live session?
     Audit the code so the db is the source of truth, in memory only to render the TUI (with a cap as it is today)
