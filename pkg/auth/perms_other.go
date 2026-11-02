//go:build !windows

package auth

import "os"

// lockdown restricts dir to its owner.
func lockdown(dir string) error { return os.Chmod(dir, 0o700) }
