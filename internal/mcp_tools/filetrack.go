package mcp_tools

import "sync"

// FileReadTracker tracks which files have been read via Open.
// Edit checks this before allowing modifications.
type FileReadTracker struct {
	mu   sync.RWMutex
	read map[string]struct{} // absolute paths that have been opened
}

// NewFileReadTracker creates a new tracker.
func NewFileReadTracker() *FileReadTracker {
	return &FileReadTracker{read: make(map[string]struct{})}
}

// MarkRead records that a file was read via Open.
func (t *FileReadTracker) MarkRead(absPath string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.read[absPath] = struct{}{}
}

// WasRead returns true if the file was previously opened.
func (t *FileReadTracker) WasRead(absPath string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.read[absPath]
	return ok
}
