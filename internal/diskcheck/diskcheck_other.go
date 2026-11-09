//go:build !linux

package diskcheck

// Free is not implemented on this platform.
func Free(string) (uint64, bool) { return 0, false }
