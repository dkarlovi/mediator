//go:build !(linux || darwin || freebsd || netbsd || openbsd || windows)

package transcode

import "errors"

func freeSpace(string) (uint64, error) {
	return 0, errors.New("free space unknown on this platform")
}
