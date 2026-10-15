//go:build !windows

package discovery

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestNonExecutableFileIgnored(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "path")
	fallback := filepath.Join(root, "fallback")
	file(t, filepath.Join(first, "claude"), "not executable", 0600)
	file(t, filepath.Join(fallback, "claude"), "executable", 0700)
	if p, _ := find("claude", Options{OS: runtime.GOOS, Dirs: []string{first, fallback}}); p != filepath.Join(fallback, "claude") {
		t.Fatal("non-executable chosen")
	}
}
