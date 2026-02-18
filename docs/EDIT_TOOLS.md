# Edit Tools: Hashline-Anchored File Editing

## Background

The standard approaches to LLM-driven file editing all share the same fundamental
weakness: they require the model to reproduce file content it already saw.

- **str_replace** (Claude Code, most agents) makes the model reproduce
  the exact old text including whitespace. Multiple matches or slight misrecall
  cause failures. The "string not found" error is one of the most common complaints.

- **apply_patch** (OpenAI Codex) expects models to produce a custom diff format.
  Models not specifically tuned for it fail catastrophically. Grok 4 had a 50.7%
  patch failure rate in benchmarks.

- **Cursor's approach** trains a separate 70B model just to apply edits. Even then,
  full file rewrites outperform diffs for files under 400 lines.

These are not model failures. They are harness failures. The model understood the
task but could not express the change reliably through the tool interface.

Source: Can Boluk, "The Harness Problem" (Feb 2026). Benchmark of 16 models across
3 edit formats showed that format choice alone swings success rates by 10-60
percentage points.

## Our Approach: Hashline

Instead of making the model reproduce old content, we give it a stable, verifiable
identifier for every line. When the model reads a file, each line comes back tagged
with a 2-character content hash derived from SHA-256.

The model references these tags to express edits. It never needs to reproduce
whitespace, indentation, or surrounding context. If it can recall a 2-char hex tag,
it knows what it is editing.

If the file changed between the read and the edit, the hashes will not match and
the operation is rejected before any corruption can occur.

## Open Tool

**File:** `internal/mcp_tools/open.go`

Opens a file in the TUI editor and returns hashline-tagged content to the LLM.
Each line is formatted as `linenum:hash|content`, where the hash is a 2-char hex
digest of that line's content.

### Behaviour

1. Validates the file path (absolute resolution, CWD-scoped, no traversal).
2. Reads the file from disk.
3. Records the file as "read" in the shared FileReadTracker.
4. Optionally extracts a line range (1-indexed start/end).
5. Sends the raw content to the TUI editor pane for display.
6. Computes hashline tags for each line.
7. Returns the tagged output to the LLM.

The tagged output gives the model everything it needs for subsequent edits: line
numbers for positioning and content hashes for verification.

### Parameters

- **file** (required): Path to the file.
- **start** (optional): Starting line number, 1-indexed.
- **end** (optional): Ending line number, 1-indexed.

## Edit Tool

**File:** `internal/mcp_tools/edit.go`

Modifies files using hash-anchored operations. Exactly one operation per call.
Returns the full updated file with fresh hashes after every edit.

### Operations

**Replace** removes lines from a start anchor to an end anchor (inclusive) and
inserts new content in their place. Both anchors are validated against the current
file before any modification. The replacement content may have more or fewer lines
than the range it replaces.

**Insert** adds new content after a single anchored line. The anchor is validated,
then the new lines are spliced in immediately after it. Existing lines below shift
down.

**Delete** removes lines from a start anchor to an end anchor (inclusive). Both
anchors are validated. The lines between them (inclusive) are removed and the file
is compacted.

**Create** writes a brand new file. It fails if the file already exists. It does
not require a prior Open call since there is no existing content to read. Parent
directories are created automatically.

### Anchors

An anchor is a pair of line number (1-indexed) and hash (2-char hex). The hash is
the first byte of the SHA-256 digest of the line's content. Both values come from
the Open tool's output.

Before any edit, the handler re-reads the file from disk and validates each anchor
against the current content. If a hash does not match, the edit is rejected with a
clear error indicating the file changed. The model must re-Open the file to get
fresh hashes.

### Fresh Hashes After Edit

Every successful edit returns the entire updated file with new hashline tags. This
is critical for chaining: if the model needs to make a second edit, it must use the
hashes from the edit response, not the original Open response. Line numbers shift
after inserts and deletes, and content hashes change after replacements.

## Safety Layers

The system enforces four independent safety checks, any one of which can reject a
bad edit.

### Read-Before-Edit

**File:** `internal/mcp_tools/filetrack.go`

Open and Edit share a FileReadTracker. When Open successfully reads a file, it
records the absolute path. When Edit receives a replace, insert, or delete, it
checks that the file was previously opened. If not, it returns an error directing
the model to Open the file first.

This prevents the model from guessing hashes or editing files it has not seen.
Create operations bypass this check since they target files that do not exist yet.

The tracker is thread-safe (sync.RWMutex) and scoped to the session lifetime.

### Hash Validation

**File:** `internal/hashline/hashline.go`

Before any modification, the edit handler re-reads the file and validates every
anchor. The Anchor.Validate method checks that the line number is in range and that
the hash matches the current content. ValidateRange additionally checks that start
comes before end.

This catches concurrent modifications, external edits, or stale hashes from a
previous edit in a chain.

### Path Traversal Prevention

Both Open and Edit resolve paths to absolute form, then verify the result is within
or below the current working directory. Paths containing ".." that escape the CWD
are rejected.

### Operation Count Enforcement

Each Edit call must contain exactly one operation. Zero operations or multiple
operations are rejected before any file I/O occurs.

## Hashline Package

**File:** `internal/hashline/hashline.go`

The hashline package is a standalone library with no dependencies on the tool layer.

**LineHash** computes SHA-256 of a line's content and returns the first byte as
2 hex characters. This gives 256 possible values per line. Collisions are possible
but rare for lines in the same file, and a collision only means the edit is not
rejected when it perhaps should be. It never causes corruption since the line number
is also checked.

**TagLines** splits content by newline and produces a TaggedLine for each, with
sequential numbering starting at a given offset.

**FormatTagged** joins tagged lines into the string format returned to the LLM.

**Anchor** pairs a line number and hash. Validate checks both against actual file
content. ValidateRange validates a start-end pair and checks ordering.

## Registration

**File:** `cmd/symb/main.go`

A single FileReadTracker is created and shared between the Open and Edit handlers.
Both tools are registered on the MCP proxy. After the Bubbletea program is created,
both handlers receive a reference to it so they can send messages to the TUI.

## Comparison With Other Approaches

| Property | str_replace | apply_patch | Hashline |
|---|---|---|---|
| Model must reproduce old content | Yes | Yes (as diff context) | No |
| Sensitive to whitespace | Very | Somewhat | Not at all |
| Detects concurrent changes | No (silent corruption) | No | Yes (hash mismatch) |
| Requires model-specific tuning | No | Effectively yes | No |
| Enforces read-before-edit | Varies by agent | No | Yes |
| Token cost of anchoring | High (full old text) | Medium (context lines) | Low (2-char hash) |

## Test Coverage

**hashline package:** 7 tests, 96.7% coverage. Tests cover hashing determinism,
tag formatting, anchor validation (in range, out of range, wrong hash), range
validation, and a full round-trip from tagging to validation.

**Edit tool:** 13 tests. Covers replace (single line, multi-line, expanding),
insert, delete, create, create-fails-if-exists, hash mismatch rejection, no
operation, multiple operations, path traversal rejection, read-before-edit
enforcement, and create bypassing the read check.

References: https://blog.can.ac/2026/02/12/the-harness-problem/
