package auth

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"go.uber.org/zap"
)

// readTokenJS reads the Slack web client's stored config from localStorage.
const readTokenJS = `() => { try { return window.localStorage.getItem('localConfig_v2'); } catch (e) { return null; } }`

// RodHarvester drives an installed browser against a dedicated persistent
// profile and reads the Slack session (xoxc token + d cookie) from it.
type RodHarvester struct {
	cfg Config
	log *zap.Logger
}

// NewRodHarvester returns a browser-backed Harvester.
func NewRodHarvester(cfg Config, log *zap.Logger) *RodHarvester {
	if log == nil {
		log = zap.NewNop()
	}
	return &RodHarvester{cfg: cfg, log: log}
}

// Harvest launches the browser (headless or visible), waits for the Slack web
// client to expose a token, reads the d cookie, and offers the pair to accept.
func (h *RodHarvester) Harvest(ctx context.Context, headless bool, timeout time.Duration, accept func(Credential) bool) (bool, error) {
	if err := secureMkdirAll(h.cfg.ProfileDir); err != nil {
		return false, err
	}

	l := launcher.New().
		UserDataDir(h.cfg.ProfileDir).
		Headless(headless).
		Leakless(false). // leakless helper is often flagged by endpoint security
		Set("disable-blink-features", "AutomationControlled")
	if bin := h.resolveBin(); bin != "" {
		l = l.Bin(bin)
	}
	controlURL, err := l.Launch()
	if err != nil {
		return false, err
	}
	defer l.Cleanup()

	browser := rod.New().ControlURL(controlURL).Context(ctx)
	if err := browser.Connect(); err != nil {
		return false, err
	}
	defer browser.Close()

	page, err := browser.Page(proto.TargetCreateTarget{URL: h.cfg.WorkspaceURL})
	if err != nil {
		return false, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		token := h.readToken(page)
		if token != "" {
			if cookie := h.readDCookie(browser); cookie != "" {
				if accept(Credential{Token: token, Cookie: cookie}) {
					return true, nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false, nil
}

func (h *RodHarvester) readToken(page *rod.Page) string {
	res, err := page.Eval(readTokenJS)
	if err != nil || res == nil {
		return ""
	}
	raw := res.Value.Str()
	if raw == "" {
		return ""
	}
	var cfg struct {
		LastActiveTeamID string `json:"lastActiveTeamId"`
		Teams            map[string]struct {
			Token string `json:"token"`
		} `json:"teams"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return ""
	}
	if t, ok := cfg.Teams[cfg.LastActiveTeamID]; ok && isXoxc(t.Token) {
		return t.Token
	}
	for _, t := range cfg.Teams {
		if isXoxc(t.Token) {
			return t.Token
		}
	}
	return ""
}

func (h *RodHarvester) readDCookie(browser *rod.Browser) string {
	cookies, err := browser.GetCookies()
	if err != nil {
		return ""
	}
	for _, c := range cookies {
		if c.Name == "d" && hasSuffixFold(trimDot(c.Domain), "slack.com") {
			return c.Value
		}
	}
	return ""
}

// resolveBin returns the configured browser binary, or the first installed one.
func (h *RodHarvester) resolveBin() string {
	if h.cfg.BrowserBin != "" {
		return h.cfg.BrowserBin
	}
	for _, p := range candidateBrowserPaths() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return "" // let rod locate or download a browser
}

func isXoxc(t string) bool { return len(t) > 5 && t[:5] == "xoxc-" }

func trimDot(s string) string {
	if len(s) > 0 && s[0] == '.' {
		return s[1:]
	}
	return s
}

func hasSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return strEqFold(s[len(s)-len(suffix):], suffix)
}
