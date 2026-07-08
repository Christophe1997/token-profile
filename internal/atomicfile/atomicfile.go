// Package atomicfile writes a file's full contents in one atomic
// operation, shared by every package that persists local state on every
// invocation (internal/snapshot, internal/runhistory): a temp file created
// in the target's own directory, then a rename, so a process killed
// mid-write leaves either the previous complete file or the new complete
// file — never a torn/partial one.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write writes data to path via a temp file created in dir followed by a
// rename.
func Write(dir, path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpPath)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
