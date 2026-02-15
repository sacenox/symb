# Rendering bugs after frame-loop refactor

## Bug 1: Conversation pane — first/last lines of assistant response unstyled

**Symptom:** The cache connections with the incremental additions is visible
in the output, breaks the continuous highlight, most noticeable with markdown
but happens to all filetypes.

- Fix the rendering pipeline from ground up.
- Remove caching/buffering etc from rendering all together.
- with the tick tracking framerate we should render the TUI state in full each frame.
  - Only renders what is displayed in the TUI (cull scroll history no visible), keep memory and db cache only. (5 ish turns in memory, rest in db)
  - Aim for 60fps - we will adjust and see what our render times are and optimize with this info.

## Bug 2: Gray background on syntax highlighted areas (editor + conversation)

**Symptom:** Syntax highlighted regions show a explicitly unset background instead
of pure black (#000000) with some themes, more noticeable with "pure black" themes.

- Background fallback to our default might not be working - fix.
- Audit that background is being inherited correctly, this should be free, provided by our libraries.
