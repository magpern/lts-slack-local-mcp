package auth

import "os"

// secureMkdirAll creates dir with owner-only access: mode 0700 on Unix, and on
// Windows an owner-only ACL (see lockdown). An error from the ACL step is
// returned so callers can log it; the directory still exists and, because the
// grant is applied before inheritance is removed, is never left inaccessible.
func secureMkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
