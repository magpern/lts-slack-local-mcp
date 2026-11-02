package auth

import (
	"errors"
	"os"
	"path/filepath"
)

// sentinelName marks a directory this tool created and owns. The tool deletes,
// kills processes for, and re-ACLs only directories that carry it (or are empty),
// so a misconfigured path can never wipe a real browser profile or home folder.
const sentinelName = ".lts-slack-local-mcp"

// ErrDirNotOwned means a configured directory exists, is not empty, and was not
// created by this tool.
var ErrDirNotOwned = errors.New("refusing to use a directory this tool did not create (it is not empty and has no ownership marker); choose a new, empty directory")

// dirState reports whether dir exists, is empty, and carries the sentinel.
func dirState(dir string) (exists, empty, ours bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, true, false
	}
	if _, err := os.Stat(filepath.Join(dir, sentinelName)); err == nil {
		ours = true
	}
	return true, len(entries) == 0, ours
}

// secureMkdirAll claims dir with owner-only access: mode 0700 on Unix, and on
// Windows an owner-only ACL (see lockdown). It refuses a pre-existing non-empty
// directory that is not ours. An error from the ACL step is returned so callers
// can log it; the directory still exists and, because the grant is applied before
// inheritance is removed, is never left inaccessible.
func secureMkdirAll(dir string) error {
	if exists, empty, ours := dirState(dir); exists && !empty && !ours {
		return ErrDirNotOwned
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, sentinelName), nil, 0o600); err != nil {
		return err
	}
	return lockdown(dir)
}

// secureWriteFile writes data to path atomically (temp file + rename). Files
// inherit the owner-only protection of their directory (0600 on Unix, the
// directory's inheritable ACE on Windows).
func secureWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
