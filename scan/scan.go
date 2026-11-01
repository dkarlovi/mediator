// Package scan finds the media files under the paths given to mediator.
package scan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dkarlovi/mediator/media"
)

// nasDirs are folders NAS systems keep inside shares: Synology's recycle bin,
// snapshots and thumbnail/index metadata, QNAP's recycle bin. Media in them
// is deleted, a snapshot, or not media at all, and must never be converted.
var nasDirs = map[string]bool{
	"#recycle":           true,
	"#snapshot":          true,
	"@eaDir":             true,
	"@Recycle":           true,
	"@Recently-Snapshot": true,
}

// skip reports whether a name is something mediator must never touch: its own
// in-progress output, hidden files and macOS/NAS metadata.
func skip(name string) bool {
	return strings.Contains(name, ".mediator-tmp.") ||
		strings.HasPrefix(name, ".") ||
		nasDirs[name]
}

// Files returns the media files at the given paths, sorted. A path can be a
// file or a directory, which is searched recursively when recurse is set.
// Paths are anything the OS can open: local, a mounted share, or on Windows a
// UNC path such as \\NAS\Shows.
func Files(paths []string, recurse bool) ([]string, error) {
	var files []string
	for _, root := range paths {
		fi, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			if !media.IsMediaFile(root) {
				return nil, fmt.Errorf("%s: not a supported media file", root)
			}
			files = append(files, root)
			continue
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && (!recurse || skip(d.Name())) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && !skip(d.Name()) && media.IsMediaFile(path) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}
