package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config controls the auth lifecycle. All fields have safe defaults.
type Config struct {
	// ProfileDir is the dedicated browser profile directory (never the user's
	// ordinary profile). Created 0700 on non-Windows.
	ProfileDir string
	// DataDir holds the persisted pin and signed-out state (0600 files).
	DataDir string
	// BrowserBin is the path to the installed browser binary (Edge/Chrome).
	BrowserBin string
	// LoginTimeout bounds an interactive (visible-window) harvest.
	LoginTimeout time.Duration
	// HeadlessTimeout bounds a silent (headless) harvest.
	HeadlessTimeout time.Duration
	// TokenTTL is how long a validated credential is trusted before a proactive
	// re-acquire. Slack session tokens carry no expiry claim, so this is a
	// conservative ceiling; the error-driven path is the real safety net.
	TokenTTL time.Duration
	// AllowedTeamIDs, when non-empty, restricts which workspaces may be linked.
	AllowedTeamIDs []string
	// WorkspaceURL is where the harvester navigates to boot the web client.
	WorkspaceURL string
}

const (
	appDirName          = "lts-slack-local-mcp"
	defaultLoginTimeout = 300 * time.Second
	defaultHeadless     = 90 * time.Second
	defaultTokenTTL     = 45 * time.Minute
	defaultWorkspace    = "https://app.slack.com/client"
)

// ConfigFromEnv builds a Config from SLACK_LOCAL_* environment variables,
// applying defaults. It never fails; bad values fall back to defaults.
func ConfigFromEnv() Config {
	c := Config{
		ProfileDir:      envOr("SLACK_LOCAL_PROFILE_DIR", defaultProfileDir()),
		DataDir:         envOr("SLACK_LOCAL_DATA_DIR", defaultDataDir()),
		BrowserBin:      os.Getenv("SLACK_LOCAL_BROWSER_BIN"),
		LoginTimeout:    envDuration("SLACK_LOCAL_LOGIN_TIMEOUT", defaultLoginTimeout),
		HeadlessTimeout: envDuration("SLACK_LOCAL_HEADLESS_TIMEOUT", defaultHeadless),
		TokenTTL:        envDuration("SLACK_LOCAL_TOKEN_TTL", defaultTokenTTL),
		WorkspaceURL:    envOr("SLACK_LOCAL_WORKSPACE_URL", defaultWorkspace),
	}
	if v := strings.TrimSpace(os.Getenv("SLACK_LOCAL_ALLOWED_TEAM_IDS")); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				c.AllowedTeamIDs = append(c.AllowedTeamIDs, p)
			}
		}
	}
	return c
}

// teamAllowed reports whether a team id passes the allow-list (empty = allow all).
func (c Config) teamAllowed(teamID string) bool {
	if len(c.AllowedTeamIDs) == 0 {
		return true
	}
	for _, t := range c.AllowedTeamIDs {
		if strEqFold(t, teamID) {
			return true
		}
	}
	return false
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return def
}

func defaultProfileDir() string { return filepath.Join(baseDataDir(), appDirName, "profile") }
func defaultDataDir() string    { return filepath.Join(baseDataDir(), appDirName, "state") }

// baseDataDir returns the per-OS base for application data.
func baseDataDir() string {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d
		}
	case "darwin":
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Library", "Application Support")
		}
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return d
		}
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, ".local", "share")
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return d
	}
	return os.TempDir()
}
