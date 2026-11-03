//go:build unix

package publish

import (
	"os"
	"syscall"
)

// linkCount reports a file's hardlink count.
func linkCount(path string) (uint64, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
