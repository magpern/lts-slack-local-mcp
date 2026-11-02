package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fakes ----

type fakeHarvester struct {
	mu     sync.Mutex
	rounds [][]Credential
	calls  []bool // headless flag per Harvest call
	delay  time.Duration
}

func (f *fakeHarvester) Harvest(ctx context.Context, headless bool, timeout time.Duration, accept func(Credential) bool) (bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, headless)
	var round []Credential
	if len(f.rounds) > 0 {
		round = f.rounds[0]
		f.rounds = f.rounds[1:]
	}
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	for _, c := range round {
		if accept(c) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeHarvester) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeValidator struct {
	ids  map[string]Identity
	errs map[string]error
}

func (v *fakeValidator) Identify(ctx context.Context, c Credential) (*Identity, error) {
	if e, ok := v.errs[c.Token]; ok {
		return nil, e
	}
	if id, ok := v.ids[c.Token]; ok {
		cp := id
		return &cp, nil
	}
	return nil, fmt.Errorf("unknown token")
}

func cred(tok string) Credential { return Credential{Token: tok, Cookie: "xoxd-" + tok} }

func newTestAuth(t *testing.T, h Harvester, v Validator) (*SessionAuth, *Store, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		ProfileDir:      filepath.Join(dir, "profile"),
		DataDir:         filepath.Join(dir, "state"),
		LoginTimeout:    time.Second,
		HeadlessTimeout: time.Second,
		TokenTTL:        time.Hour,
	}
	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	sa := NewSessionAuth(cfg, store, h, v, nil)
	sa.AllowAutoFirstLogin() // tests exercise the lifecycle; first-run gating has its own test
	return sa, store, cfg
}

// ---- tests ----

func TestUnitSilentHarvestSuccess(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	sa, store, _ := newTestAuth(t, h, v)

	c, id, gen, err := sa.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if c.Token != "xoxc-A" || id.TeamID != "T1" || gen != 1 {
		t.Fatalf("unexpected: %v %v %d", c.Token, id, gen)
	}
	if h.callCount() != 1 || h.calls[0] != true {
		t.Fatalf("expected one headless harvest, got %v", h.calls)
	}
	if p := store.Pin(); p == nil || p.TeamID != "T1" {
		t.Fatalf("pin not persisted: %v", p)
	}
}

func TestUnitWrongAccountNoWindow(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-B")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-B": {TeamID: "T2", UserID: "U2"}}}
	sa, store, _ := newTestAuth(t, h, v)
	_ = store.SetPin(Identity{TeamID: "T1", UserID: "U1"}) // pinned to a different account

	_, _, _, err := sa.Get(context.Background())
	if err != ErrIdentityChanged {
		t.Fatalf("expected ErrIdentityChanged, got %v", err)
	}
	if h.callCount() != 1 { // headless only; no visible window for a wrong account
		t.Fatalf("expected no visible window, calls=%v", h.calls)
	}
}

func TestUnitLoginOpensVisibleWindowWhenHeadlessEmpty(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{}, {cred("xoxc-A")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	sa, _, _ := newTestAuth(t, h, v)

	if _, err := sa.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if h.callCount() != 2 || h.calls[0] != true || h.calls[1] != false {
		t.Fatalf("expected headless then visible, got %v", h.calls)
	}
}

func TestUnitAutoReacquireNeverOpensWindow(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{}}} // headless finds nothing
	v := &fakeValidator{ids: map[string]Identity{}}
	sa, store, _ := newTestAuth(t, h, v)
	_ = store.SetPin(Identity{TeamID: "T1", UserID: "U1"})

	if _, _, _, err := sa.Get(context.Background()); err != ErrNoSession {
		t.Fatalf("expected ErrNoSession, got %v", err)
	}
	if h.callCount() != 1 || h.calls[0] != true {
		t.Fatalf("unattended acquire must be headless only, got %v", h.calls)
	}
}

func TestUnitFirstRunRequiresLogin(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	dir := t.TempDir()
	cfg := Config{ProfileDir: filepath.Join(dir, "p"), DataDir: filepath.Join(dir, "s"),
		LoginTimeout: time.Second, HeadlessTimeout: time.Second, TokenTTL: time.Hour}
	store, _ := OpenStore(cfg.DataDir)
	sa := NewSessionAuth(cfg, store, h, v, nil) // no AllowAutoFirstLogin

	if _, _, _, err := sa.Get(context.Background()); err != ErrNotLoggedIn {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
	if h.callCount() != 0 {
		t.Fatalf("first run must not harvest without login, calls=%v", h.calls)
	}
}

func TestUnitLogoutDuringHarvestDiscardsResult(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}, delay: 150 * time.Millisecond}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	sa, _, _ := newTestAuth(t, h, v)

	done := make(chan error, 1)
	go func() { _, _, _, err := sa.Get(context.Background()); done <- err }()
	time.Sleep(40 * time.Millisecond) // harvest is in flight
	if err := sa.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if err := <-done; err != ErrSignedOut {
		t.Fatalf("harvest finishing after logout must be discarded, got %v", err)
	}
	if _, _, _, err := sa.Get(context.Background()); err != ErrSignedOut {
		t.Fatalf("still signed out expected, got %v", err)
	}
}

func TestUnitLogoutCleanupFailureQuarantines(t *testing.T) {
	sa, store, cfg := newTestAuth(t, &fakeHarvester{}, &fakeValidator{})
	_ = os.MkdirAll(cfg.ProfileDir, 0o700)
	sa.removeAll = func(string) error { return fmt.Errorf("locked") } // deletion fails
	if err := sa.Logout(context.Background()); err != nil {
		t.Fatalf("quarantine should make Logout succeed, got %v", err)
	}
	if _, err := os.Stat(cfg.ProfileDir); !os.IsNotExist(err) {
		t.Fatalf("residual profile must not remain at ProfileDir (stat err=%v)", err)
	}
	if !store.SignedOut() || store.ProfileDirty() {
		t.Fatalf("expected signed-out and clean flag after successful quarantine")
	}
}

func TestUnitLogoutCleanupTotalFailureBlocksReuseAcrossRestart(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	sa, store, cfg := newTestAuth(t, h, v)
	_ = os.MkdirAll(cfg.ProfileDir, 0o700)
	sa.removeAll = func(string) error { return fmt.Errorf("locked") }
	sa.rename = func(string, string) error { return fmt.Errorf("locked") }

	if err := sa.Logout(context.Background()); err == nil {
		t.Fatalf("expected Logout to report the cleanup failure")
	}
	if !store.SignedOut() || !store.ProfileDirty() {
		t.Fatalf("must stay signed out with the dirty flag persisted")
	}
	if _, _, _, err := sa.Get(context.Background()); err != ErrSignedOut {
		t.Fatalf("expected ErrSignedOut, got %v", err)
	}

	// Restart: new process, same store. Login must refuse to reuse the residue
	// while it still cannot be removed.
	sa2 := NewSessionAuth(cfg, store, h, v, nil)
	sa2.removeAll = func(string) error { return fmt.Errorf("locked") }
	sa2.rename = func(string, string) error { return fmt.Errorf("locked") }
	if _, err := sa2.Login(context.Background()); err == nil {
		t.Fatalf("Login must fail while the residual profile cannot be removed")
	}
	if h.callCount() != 0 {
		t.Fatalf("no harvest may touch a residual profile, calls=%v", h.calls)
	}

	// Once removal works, Login cleans the residue first, then proceeds.
	sa3 := NewSessionAuth(cfg, store, h, v, nil)
	if _, err := sa3.Login(context.Background()); err != nil {
		t.Fatalf("Login after cleanup works: %v", err)
	}
	if store.ProfileDirty() {
		t.Fatalf("dirty flag must clear after successful cleanup")
	}
}

func TestUnitAllowListRejection(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T123", UserID: "U1"}}}
	sa, _, _ := newTestAuth(t, h, v)
	sa.cfg.AllowedTeamIDs = []string{"T999"}

	if _, _, _, err := sa.Get(context.Background()); err != ErrNotAllowed {
		t.Fatalf("expected ErrNotAllowed, got %v", err)
	}
	if h.callCount() != 1 {
		t.Fatalf("disallowed account must not open a window, calls=%v", h.calls)
	}
}

func TestUnitProactiveRefreshOnTTL(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}, {cred("xoxc-B")}}}
	v := &fakeValidator{ids: map[string]Identity{
		"xoxc-A": {TeamID: "T1", UserID: "U1"},
		"xoxc-B": {TeamID: "T1", UserID: "U1"},
	}}
	sa, _, _ := newTestAuth(t, h, v)
	now := time.Unix(1000, 0)
	sa.clock = func() time.Time { return now }
	sa.cfg.TokenTTL = 10 * time.Minute

	c1, _, _, _ := sa.Get(context.Background())
	if c1.Token != "xoxc-A" {
		t.Fatalf("first get: %s", c1.Token)
	}
	now = now.Add(11 * time.Minute) // past refreshAt
	c2, _, _, err := sa.Get(context.Background())
	if err != nil || c2.Token != "xoxc-B" {
		t.Fatalf("expected refresh to B, got %s err=%v", c2.Token, err)
	}
}

func TestUnitCredentialRedaction(t *testing.T) {
	c := Credential{Token: "xoxc-SECRETTOKEN", Cookie: "xoxd-SECRETCOOKIE"}
	for _, s := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%s", c), c.String()} {
		if strings.Contains(s, "SECRET") {
			t.Fatalf("credential leaked value: %q", s)
		}
	}
	if len(c.FP()) != 12 {
		t.Fatalf("fingerprint length = %d", len(c.FP()))
	}
}
