//go:build !windows

package auth

import "os/exec"

// killProfileProcesses stops any browser process whose command line references
// the dedicated profile directory, so an orphan cannot keep the profile locked.
func killProfileProcesses(profileDir string) {
	if profileDir == "" {
		return
	}
	_ = exec.Command("pkill", "-f", "--", "--user-data-dir="+profileDir).Run()
}
