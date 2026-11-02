package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
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

	bin := h.resolveBin()
	if bin == "" {
		return false, ErrNoBrowser
	}
	proc, controlURL, err := h.startBrowser(ctx, bin, headless)
	if err != nil {
		return false, err
	}
	// Close politely on a fresh context (the caller's may already be cancelled),
	// then make sure the process is gone so it cannot hold the profile. The
	// profile directory itself is never deleted here: it is the persistent session.
	browser := rod.New().ControlURL(controlURL).Context(ctx)
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = browser.Context(cctx).Close()
		proc.stop(5 * time.Second)
	}()
	if err := browser.Connect(); err != nil {
		return false, err
	}

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
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
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
	return "" // fail closed: never let rod download and run a browser
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

// browserProc is a launched browser process.
type browserProc struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

// stop waits briefly for a graceful exit (after Browser.close), then kills.
func (p *browserProc) stop(grace time.Duration) {
	p.once.Do(func() {
		select {
		case <-p.done:
		case <-time.After(grace):
			_ = p.cmd.Process.Kill()
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
			}
		}
		time.Sleep(300 * time.Millisecond) // let the OS release profile file locks
	})
}

// startBrowser launches the browser against the dedicated profile and returns
// its DevTools websocket URL. We read DevToolsActivePort from the profile rather
// than the browser's stderr: Edge on Windows does not print the URL there, which
// makes rod's own launcher fail.
func (h *RodHarvester) startBrowser(ctx context.Context, bin string, headless bool) (*browserProc, string, error) {
	portFile := filepath.Join(h.cfg.ProfileDir, "DevToolsActivePort")
	_ = os.Remove(portFile) // never trust a stale file from a previous run

	args := []string{
		"--user-data-dir=" + h.cfg.ProfileDir,
		"--remote-debugging-port=0",
		"--remote-allow-origins=http://127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-blink-features=AutomationControlled",
		"--disable-background-networking",
		"--disable-sync",
	}
	if headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "about:blank")

	cmd := exec.Command(bin, args...)
	var errOut limitedBuf
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start browser: %w", err)
	}
	p := &browserProc{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()

	deadline := time.Now().Add(30 * time.Second)
	exited := p.done // the launcher process may exit after re-spawning the real browser
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			p.stop(0)
			return nil, "", ctx.Err()
		case <-exited:
			exited = nil
			if d := time.Now().Add(10 * time.Second); d.Before(deadline) {
				deadline = d
			}
		case <-time.After(200 * time.Millisecond):
		}
		if b, err := os.ReadFile(portFile); err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 2 && strings.TrimSpace(lines[0]) != "" {
				return p, "ws://127.0.0.1:" + strings.TrimSpace(lines[0]) + strings.TrimSpace(lines[1]), nil
			}
		}
	}
	p.stop(0)
	return nil, "", fmt.Errorf("browser did not expose a debug port (is another browser using the dedicated profile?): %s", errOut.String())
}

// limitedBuf keeps the first few KB of output for error messages.
type limitedBuf struct {
	mu sync.Mutex
	b  []byte
}

func (l *limitedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := 4096 - len(l.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.b = append(l.b, p[:room]...)
	}
	return len(p), nil
}

func (l *limitedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.TrimSpace(string(l.b))
}
