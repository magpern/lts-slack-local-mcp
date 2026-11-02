// Package auth implements the browser-session authentication lifecycle for the
// local-only Slack MCP server: it acquires a Slack web session (an xoxc token
// plus the d cookie) from a dedicated browser profile, validates it, pins the
// identity, and re-acquires it when Slack rejects it. Credentials are never
// serialized by this application; only a short fingerprint is logged.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Credential is a captured Slack web session. Its fields are never logged.
type Credential struct {
	Token  string `json:"-"` // xoxc-...
	Cookie string `json:"-"` // the d cookie value (xoxd-...)
}

// GoString redacts the credential for %#v.
func (c Credential) GoString() string { return c.String() }

// String redacts the credential so it never leaks via %v/%s or logs.
func (c Credential) String() string { return "Credential(" + c.FP() + ")" }

// FP returns a short, non-reversible fingerprint of the credential, safe to log.
func (c Credential) FP() string {
	if c.Token == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(c.Token + "\x00" + c.Cookie))
	return hex.EncodeToString(sum[:])[:12]
}

// Cookies returns the HTTP cookies Slack expects for a browser session.
func (c Credential) Cookies() []*http.Cookie {
	val := c.Cookie
	if dec, err := url.QueryUnescape(val); err != nil || dec == val {
		// store raw; Slack accepts the escaped value as-is
	}
	return []*http.Cookie{
		{Name: "d", Value: val, Domain: ".slack.com", Path: "/", Secure: true, HttpOnly: true},
		{Name: "d-s", Value: strconv.FormatInt(time.Now().Unix()-10, 10), Domain: ".slack.com", Path: "/", Secure: true},
	}
}

// Identity is the account a credential belongs to, from auth.test.
type Identity struct {
	TeamID       string `json:"team_id"`
	UserID       string `json:"user_id"`
	Team         string `json:"team,omitempty"`
	User         string `json:"user,omitempty"`
	EnterpriseID string `json:"enterprise_id,omitempty"`
}

// SameAccount reports whether two identities are the same Slack account.
// It compares team then user; an empty field on either side is not a match.
func (i Identity) SameAccount(o Identity) bool {
	if i.TeamID == "" || o.TeamID == "" || !strEqFold(i.TeamID, o.TeamID) {
		return false
	}
	if i.UserID != "" && o.UserID != "" {
		return strEqFold(i.UserID, o.UserID)
	}
	return false
}

func strEqFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// Error sentinels surfaced to MCP tools. Messages tell the agent what to do.
var (
	// ErrSignedOut means logout was called and auto-login is suspended.
	ErrSignedOut = errors.New("signed out. Call the login tool to sign in again")
	// ErrIdentityChanged means the browser is signed in to a different account.
	ErrIdentityChanged = errors.New("the browser is signed in to a different Slack account than this session selected. Call the login tool to select the account again")
	// ErrNotLoggedIn means no account has been linked yet; login must be called.
	ErrNotLoggedIn = errors.New("not signed in to Slack yet. Call the login tool and complete sign-in in the browser window")
	// ErrNoSession means no usable session could be acquired.
	ErrNoSession = errors.New("could not obtain a Slack session. Call the login tool and complete sign-in in the browser window")
	// ErrNoBrowser means no supported browser was found. rod is never allowed to
	// download one: this tool holds session credentials.
	ErrNoBrowser = errors.New("no supported browser found (Edge or Chrome). Install one or set SLACK_LOCAL_BROWSER_BIN")
	// ErrNotAllowed means the account is outside the configured team allow-list.
	ErrNotAllowed = errors.New("the signed-in Slack workspace is not in the configured allow-list. Sign in with an allowed workspace, then call login")
	// ErrAuthRejected means a freshly acquired credential was still rejected
	// (e.g. an IP/session restriction that re-acquisition cannot fix).
	ErrAuthRejected = errors.New("Slack rejected a freshly acquired session; re-acquisition did not help (possible IP or session restriction). Call login")
)
