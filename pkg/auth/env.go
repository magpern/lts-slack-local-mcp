package auth

import (
	"context"
	"os"
	"strings"
	"time"
)

// EnvCredential returns a credential supplied through environment variables, in
// the same priority order the upstream project used: a user OAuth token
// (SLACK_MCP_XOXP_TOKEN), then a bot token (SLACK_MCP_XOXB_TOKEN), then a browser
// session pair (SLACK_MCP_XOXC_TOKEN + SLACK_MCP_XOXD_TOKEN). It is an optional
// override for CI and scripted use; the browser login is the primary path.
func EnvCredential() (Credential, bool) {
	get := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	if t := get("SLACK_MCP_XOXP_TOKEN"); t != "" {
		return Credential{Token: t}, true
	}
	if t := get("SLACK_MCP_XOXB_TOKEN"); t != "" {
		return Credential{Token: t}, true
	}
	if t, d := get("SLACK_MCP_XOXC_TOKEN"), get("SLACK_MCP_XOXD_TOKEN"); t != "" && d != "" {
		return Credential{Token: t, Cookie: d}, true
	}
	return Credential{}, false
}

// StaticHarvester offers one fixed credential. It never opens a browser.
type StaticHarvester struct{ Cred Credential }

// Harvest offers the fixed credential to accept.
func (h StaticHarvester) Harvest(_ context.Context, _ bool, _ time.Duration, accept func(Credential) bool) (bool, error) {
	return accept(h.Cred), nil
}

// IsOAuthToken reports whether a token is an official OAuth token (user or bot).
func IsOAuthToken(t string) bool {
	return strings.HasPrefix(t, "xoxp-") || strings.HasPrefix(t, "xoxb-") ||
		strings.HasPrefix(t, "xoxe.xoxp-") || strings.HasPrefix(t, "xoxe.xoxb-")
}

// IsBotToken reports whether a token is a bot token.
func IsBotToken(t string) bool {
	return strings.HasPrefix(t, "xoxb-") || strings.HasPrefix(t, "xoxe.xoxb-")
}

// Pin returns the persisted identity pin, if any.
func (s *SessionAuth) Pin() *Identity { return s.store.Pin() }

// SignedOut reports whether auto-login is suspended.
func (s *SessionAuth) SignedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suspended
}
