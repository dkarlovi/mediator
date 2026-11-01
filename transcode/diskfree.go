package transcode

import (
	"os"
	"path/filepath"
)

// FreeSpace returns the bytes available to this user on the volume holding
// dir. A dir that doesn't exist yet is measured at its nearest existing
// parent, so a dry run can check a work directory before creating it.
func FreeSpace(dir string) (uint64, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return freeSpace(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return freeSpace(dir)
		}
		dir = parent
	}
}
