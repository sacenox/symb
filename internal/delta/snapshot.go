package delta

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileSnapshot holds mtime+size+content for change detection and undo.
type FileSnapshot struct {
	ModTime time.Time
	Size    int64
	Content []byte // pre-read for undo; nil for large files
}

// maxSnapshotFileSize is the max file size we pre-read for undo (1 MB).
const maxSnapshotFileSize = 1 << 20

// skipDirs are directories skipped during snapshot walks.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true,
	".venv": true, "vendor": true, ".cache": true, ".next": true,
	"dist": true, "build": true, "target": true,
}

// SnapshotDir walks root and returns a map of relative path -> FileSnapshot.
// Files under maxSnapshotFileSize have their content pre-read for undo.
func SnapshotDir(root string) map[string]FileSnapshot {
	snap := make(map[string]FileSnapshot)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fs := FileSnapshot{ModTime: info.ModTime(), Size: info.Size()}
		if info.Size() <= maxSnapshotFileSize {
			fs.Content, _ = os.ReadFile(path)
		}
		snap[rel] = fs
		return nil
	})
	return snap
}

// RecordDeltas compares pre/post snapshots and records deltas for undo.
func RecordDeltas(dt *Tracker, root string, pre, post map[string]FileSnapshot) {
	// New or modified files.
	for rel, postInfo := range post {
		absPath := filepath.Join(root, rel)
		preInfo, existed := pre[rel]
		if !existed {
			dt.RecordCreate(absPath)
			continue
		}
		if preInfo.ModTime != postInfo.ModTime || preInfo.Size != postInfo.Size {
			dt.RecordModify(absPath, preInfo.Content)
		}
	}
	// Deleted files — existed in pre but not in post.
	for rel, preInfo := range pre {
		if _, exists := post[rel]; !exists {
			absPath := filepath.Join(root, rel)
			if preInfo.Content != nil {
				dt.RecordModify(absPath, preInfo.Content)
			}
		}
	}
}
