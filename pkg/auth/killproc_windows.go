//go:build windows

package auth

import (
	"os/exec"
	"strings"
	"syscall"
)

// killProfileProcesses stops any browser process whose command line references
// the dedicated profile directory, so an orphan cannot keep the profile locked.
func killProfileProcesses(profileDir string) {
	if profileDir == "" {
		return
	}
	quoted := strings.ReplaceAll(profileDir, "'", "''")
	script := "Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -and $_.CommandLine.Contains('" +
		strings.ReplaceAll(quoted, "`", "``") + "') } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
