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
	return NewSessionAuth(cfg, store, h, v, nil), store, cfg
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

func TestUnitVisibleWindowWhenHeadlessEmpty(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{}, {cred("xoxc-A")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	sa, _, _ := newTestAuth(t, h, v)

	if _, _, _, err := sa.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if h.callCount() != 2 || h.calls[0] != true || h.calls[1] != false {
		t.Fatalf("expected headless then visible, got %v", h.calls)
	}
}

func TestUnitConcurrentGetShareOneHarvest(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}, delay: 80 * time.Millisecond}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	sa, _, _ := newTestAuth(t, h, v)

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _, _, errs[i] = sa.Get(context.Background()) }(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d: %v", i, e)
		}
	}
	if h.callCount() != 1 {
		t.Fatalf("expected exactly one harvest, got %d", h.callCount())
	}
}

func TestUnitReacquireOnAuthFailure(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}, {cred("xoxc-B")}}}
	v := &fakeValidator{ids: map[string]Identity{
		"xoxc-A": {TeamID: "T1", UserID: "U1"},
		"xoxc-B": {TeamID: "T1", UserID: "U1"},
	}}
	sa, _, _ := newTestAuth(t, h, v)

	c1, _, g1, err := sa.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	c2, _, g2, err := sa.HandleAuthFailure(context.Background(), g1, c1.FP())
	if err != nil {
		t.Fatalf("HandleAuthFailure: %v", err)
	}
	if c2.Token != "xoxc-B" || g2 <= g1 {
		t.Fatalf("expected re-acquired credential at newer gen, got %s g=%d", c2.Token, g2)
	}
}

func TestUnitStaleFailureScopedToGeneration(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}, {cred("xoxc-B")}}}
	v := &fakeValidator{ids: map[string]Identity{
		"xoxc-A": {TeamID: "T1", UserID: "U1"},
		"xoxc-B": {TeamID: "T1", UserID: "U1"},
	}}
	sa, _, _ := newTestAuth(t, h, v)

	c1, _, g1, _ := sa.Get(context.Background())
	// Request Y refreshes to B at gen2.
	_, _, g2, _ := sa.HandleAuthFailure(context.Background(), g1, c1.FP())
	// Request X (still on gen1) reports its stale failure; must reuse B, not harvest.
	before := h.callCount()
	c3, _, g3, err := sa.HandleAuthFailure(context.Background(), g1, c1.FP())
	if err != nil {
		t.Fatalf("stale HandleAuthFailure: %v", err)
	}
	if c3.Token != "xoxc-B" || g3 != g2 {
		t.Fatalf("stale failure should reuse gen2/B, got %s g=%d", c3.Token, g3)
	}
	if h.callCount() != before {
		t.Fatalf("stale failure must not trigger a harvest")
	}
}

func TestUnitLogoutSignedOutReloginAcrossRestart(t *testing.T) {
	h := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}}
	v := &fakeValidator{ids: map[string]Identity{"xoxc-A": {TeamID: "T1", UserID: "U1"}}}
	sa, store, cfg := newTestAuth(t, h, v)

	if _, _, _, err := sa.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Populate the profile dir so logout has something to delete.
	_ = os.MkdirAll(cfg.ProfileDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfg.ProfileDir, "x"), []byte("x"), 0o600)

	if err := sa.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := os.Stat(cfg.ProfileDir); !os.IsNotExist(err) {
		t.Fatalf("profile dir should be deleted, stat err=%v", err)
	}
	before := h.callCount()
	if _, _, _, err := sa.Get(context.Background()); err != ErrSignedOut {
		t.Fatalf("expected ErrSignedOut after logout, got %v", err)
	}
	if h.callCount() != before {
		t.Fatalf("Get after logout must not harvest")
	}

	// Simulate a process restart: a fresh SessionAuth from the same store.
	h2 := &fakeHarvester{rounds: [][]Credential{{cred("xoxc-A")}}}
	sa2 := NewSessionAuth(cfg, store, h2, v, nil)
	if _, _, _, err := sa2.Get(context.Background()); err != ErrSignedOut {
		t.Fatalf("restart should remain signed out, got %v", err)
	}
	if h2.callCount() != 0 {
		t.Fatalf("restart signed-out must not harvest")
	}
	// Explicit login re-enables and re-pins.
	if id, err := sa2.Login(context.Background()); err != nil || id.TeamID != "T1" {
		t.Fatalf("Login after restart: id=%v err=%v", id, err)
	}
	if store.SignedOut() {
		t.Fatalf("login should clear signed-out state")
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
