package auth

import (
	"os"
	"os/exec"
	"runtime"
)

// secureMkdirAll creates dir with owner-only access. On non-Windows it uses
// mode 0700. On Windows, Go's permission bits do not map to ACLs, so it applies
// a best-effort owner-only ACL via icacls (inheritance disabled, current user
// granted full control). ACL failures are returned for the caller to log, but
// the directory still exists.
func secureMkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return windowsLockdown(dir)
	}
	return os.Chmod(dir, 0o700)
}

// secureWriteFile writes data to path with owner-only access, atomically.
func secureWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = windowsLockdown(tmp)
	}
	return os.Rename(tmp, path)
}

// windowsLockdown removes inherited ACEs and grants only the current user.
// Best-effort: returns the icacls error if it fails.
func windowsLockdown(path string) error {
	user := os.Getenv("USERDOMAIN") + "\\" + os.Getenv("USERNAME")
	if os.Getenv("USERNAME") == "" {
		return nil
	}
	cmd := exec.Command("icacls", path, "/inheritance:r", "/grant:r", user+":(OI)(CI)F")
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run()
}
