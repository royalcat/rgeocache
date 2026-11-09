//go:build linux

// Package diskcheck reports free space so long generation runs can fail before
// they start instead of hours in.
package diskcheck

import (
	"os"
	"syscall"
)

// Free returns the bytes available to an unprivileged user on the filesystem
// holding dir, or ok=false when it cannot be determined. dir may not exist yet:
// the nearest existing parent is used.
func Free(dir string) (free uint64, ok bool) {
	for {
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err == nil {
			return st.Bavail * uint64(st.Bsize), true
		}
		parent := dirParent(dir)
		if parent == dir {
			return 0, false
		}
		dir = parent
	}
}

func dirParent(p string) string {
	for len(p) > 1 && os.IsPathSeparator(p[len(p)-1]) {
		p = p[:len(p)-1]
	}
	i := len(p) - 1
	for i > 0 && !os.IsPathSeparator(p[i]) {
		i--
	}
	if i <= 0 {
		return "/"
	}
	return p[:i]
}
