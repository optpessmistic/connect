//go:build !windows

// Package privatefile restricts files containing client credentials and backups.
package privatefile

import "os"

func Directory(path string) error { return os.Chmod(path, 0700) }
func File(f *os.File) error       { return f.Chmod(0600) }
