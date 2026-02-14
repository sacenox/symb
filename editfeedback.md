**Analysis of `edit.go`:**

The core design is solid. Hashline anchoring works. What could be improved:

**1. Error messages are too generic**
```go
// Current (line 83 in hashline.go):
"hash mismatch at line %d: expected %s, got %s"
```
Could include: actual line content, suggestion to re-read

**2. No diff in response**
Currently returns full file with new hashes. Could add optional diff:
```
-3|old line
+4|new line
```

**3. Verbose JSON structure**
```json
{"replace": {"start": {"line": 5, "hash": "ab"}, "end": {...}}}
```
Could simplify to `"start": "5ab"` or similar shorthand

**4. No "dry run" / validation mode**
LLM could check if edit is safe before committing

**What I'd keep:**
- Read-before-edit enforcement ✅
- Hash validation ✅
- LSP diagnostics ✅

**Quick wins:**
- Enhanced error messages with context
- Optional diff output in response
- Maybe shorthand anchor syntax (`"5ab"` vs `{"line":5,"hash":"ab"}`)
