//go:build windows

package auth

import (
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

// currentUserSID returns the SID of the user running this process.
func currentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// lockdown makes dir owner-only. Order matters: the owner's inheritable full
// control is granted FIRST, by SID (names such as DOMAIN\user do not resolve for
// every account type, e.g. Azure AD), and inherited entries are removed only
// after that grant succeeded. A failure at either step therefore can never leave
// the directory without an ACE for the owner.
func lockdown(dir string) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	if out, err := exec.Command("icacls", dir, "/grant:r", "*"+sid+":(OI)(CI)F").CombinedOutput(); err != nil {
		return fmt.Errorf("icacls grant: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("icacls", dir, "/inheritance:r").CombinedOutput(); err != nil {
		return fmt.Errorf("icacls inheritance: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
