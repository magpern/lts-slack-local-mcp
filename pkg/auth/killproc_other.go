//go:build !windows

package auth

import (
	"os/exec"
	"regexp"
)

// killProfileProcesses stops processes launched with exactly this
// --user-data-dir, so an orphan cannot keep the dedicated profile locked.
func killProfileProcesses(profileDir string) {
	if profileDir == "" {
		return
	}
	_ = exec.Command("pkill", "-f", "--", "--user-data-dir="+regexp.QuoteMeta(profileDir)+"( |$)").Run()
}
