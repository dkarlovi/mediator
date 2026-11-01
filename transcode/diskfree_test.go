package transcode

import (
	"path/filepath"
	"testing"
)

func TestFreeSpace(t *testing.T) {
	dir := t.TempDir()
	free, err := FreeSpace(dir)
	if err != nil || free == 0 {
		t.Fatalf("FreeSpace(%s) = %d, %v", dir, free, err)
	}
	// A missing directory is measured at its nearest existing parent.
	missing, err := FreeSpace(filepath.Join(dir, "not", "yet"))
	if err != nil || missing == 0 {
		t.Fatalf("missing dir: %d, %v", missing, err)
	}
}
