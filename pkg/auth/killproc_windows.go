//go:build windows

package auth

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func system32(name string) string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", name)
}

// killProfileProcesses stops browser processes launched with exactly this
// --user-data-dir, so an orphan cannot keep the dedicated profile locked. The
// filtering happens here (not in a shell pattern) so only browser executables whose
// argument matches exactly are touched.
func killProfileProcesses(profileDir string) {
	if profileDir == "" {
		return
	}
	list := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		"-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance Win32_Process | Where-Object { $_.CommandLine } | ForEach-Object { \"$($_.ProcessId)`t$($_.Name)`t$($_.CommandLine)\" }")
	list.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := list.Output()
	if err != nil {
		return
	}
	self := os.Getpid()
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		pid, perr := strconv.Atoi(strings.TrimSpace(parts[0]))
		if perr != nil || pid == self || !isBrowserName(parts[1]) || !hasUserDataDirArg(parts[2], profileDir) {
			continue
		}
		kill := exec.Command(system32("taskkill.exe"), "/PID", strconv.Itoa(pid), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = kill.Run()
	}
}
