//go:build linux

package diskcheck

import "testing"

func TestFreeNonexistentDirUsesParent(t *testing.T) {
	free, ok := Free(t.TempDir() + "/a/b/c")
	if !ok || free == 0 {
		t.Fatalf("free=%d ok=%v", free, ok)
	}
}
