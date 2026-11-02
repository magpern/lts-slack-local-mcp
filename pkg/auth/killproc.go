package auth

import "strings"

// isBrowserName reports whether a process image name is a Chromium-family browser.
func isBrowserName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, p := range []string{"msedge", "chrome", "chromium", "brave"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// hasUserDataDirArg reports whether cmdline carries exactly this profile as its
// --user-data-dir (quoted or not), not merely a path that starts with it.
func hasUserDataDirArg(cmdline, dir string) bool {
	for _, form := range []string{"--user-data-dir=" + dir, "--user-data-dir=\"" + dir + "\""} {
		for rest := cmdline; ; {
			i := strings.Index(rest, form)
			if i < 0 {
				break
			}
			end := i + len(form)
			if end == len(rest) || rest[end] == ' ' || rest[end] == '"' {
				return true
			}
			rest = rest[end:]
		}
	}
	return false
}
