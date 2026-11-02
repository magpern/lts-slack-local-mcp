package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

// funcHarvester delegates each Harvest call to fn, so a test can block, panic or
// answer per call.
type funcHarvester struct {
	mu    sync.Mutex
	n     int
	fn    func(call int, ctx context.Context, headless bool, accept func(Credential) bool) (bool, error)
	calls []bool
}

func (f *funcHarvester) Harvest(ctx context.Context, headless bool, _ time.Duration, accept func(Credential) bool) (bool, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.calls = append(f.calls, headless)
	f.mu.Unlock()
	return f.fn(n, ctx, headless, accept)
}

func twoAccounts() *fakeValidator {
	return &fakeValidator{ids: map[string]Identity{
		"xoxc-A": {TeamID: "T1", UserID: "U1"},
		"xoxc-B": {TeamID: "T1", UserID: "U2"},
	}}
}

// A harvest that started before logout must not install its credential even if
// the user logs in again before it finishes.
func TestUnitLogoutThenLoginDuringHarvestDiscardsOldResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := &funcHarvester{fn: func(call int, ctx context.Context, headless bool, accept func(Credential) bool) (bool, error) {
		if call == 1 {
			close(started)
			<-release // ignores ctx on purpose: a misbehaving harvester must still be discarded
			return accept(cred("xoxc-A")), nil
		}
		return accept(cred("xoxc-B")), nil
	}}
	sa, store, _ := newTestAuth(t, h, twoAccounts())

	getErr := make(chan error, 1)
	go func() { _, _, _, err := sa.Get(context.Background()); getErr <- err }()
	<-started

	logoutDone := make(chan error, 1)
	go func() { logoutDone <- sa.Logout(context.Background()) }()
	time.Sleep(50 * time.Millisecond) // Logout is now waiting for the harvest to stop
	close(release)
	if err := <-logoutDone; err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if err := <-getErr; err == nil {
		t.Fatal("harvest that straddled logout must not return a credential")
	}
	if !store.SignedOut() {
		t.Fatal("signed-out state must be persisted")
	}

	id, err := sa.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if id.UserID != "U2" {
		t.Fatalf("login must install the new account, got %v", id)
	}
	if p := store.Pin(); p == nil || p.UserID != "U2" {
		t.Fatalf("pin must follow the login, got %v", p)
	}
}

// Logout must stop a running harvest (the browser) before deleting the profile.
func TestUnitLogoutCancelsHarvestBeforeDeletingProfile(t *testing.T) {
	started := make(chan struct{})
	var mu sync.Mutex
	harvestEnded := false
	h := &funcHarvester{fn: func(call int, ctx context.Context, headless bool, accept func(Credential) bool) (bool, error) {
		close(started)
		<-ctx.Done()
		mu.Lock()
		harvestEnded = true
		mu.Unlock()
		return false, ctx.Err()
	}}
	sa, _, _ := newTestAuth(t, h, twoAccounts())
	sa.removeAll = func(string) error {
		mu.Lock()
		defer mu.Unlock()
		if !harvestEnded {
			t.Error("profile deleted while the harvest (browser) was still running")
		}
		return nil
	}

	go func() { _, _, _, _ = sa.Get(context.Background()) }()
	<-started
	if err := sa.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
}

// A failed proactive refresh keeps the credential Slack has not rejected.
func TestUnitFailedTTLRefreshKeepsValidCredential(t *testing.T) {
	h := &funcHarvester{fn: func(call int, ctx context.Context, headless bool, accept func(Credential) bool) (bool, error) {
		if call == 1 {
			return accept(cred("xoxc-A")), nil
		}
		return false, nil // refresh finds nothing
	}}
	sa, _, _ := newTestAuth(t, h, twoAccounts())
	now := time.Now()
	sa.clock = func() time.Time { return now }
	if _, _, _, err := sa.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour) // past the TTL
	c, _, _, err := sa.Get(context.Background())
	if err != nil || c.Token != "xoxc-A" {
		t.Fatalf("expected the still-unrejected credential, got %v %v", c.Token, err)
	}
}

// A panicking harvester must not leave the single-harvester flag set.
func TestUnitHarvesterPanicDoesNotWedgeSession(t *testing.T) {
	h := &funcHarvester{fn: func(call int, ctx context.Context, headless bool, accept func(Credential) bool) (bool, error) {
		if call == 1 {
			panic("boom")
		}
		return accept(cred("xoxc-A")), nil
	}}
	sa, _, _ := newTestAuth(t, h, twoAccounts())
	if _, _, _, err := sa.Get(context.Background()); err == nil {
		t.Fatal("first Get should fail")
	}
	done := make(chan error, 1)
	go func() { _, _, _, err := sa.Get(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Get: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session wedged after a harvester panic")
	}
}

// A caller waiting behind a running harvest must honour its own context.
func TestUnitWaiterHonoursContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := &funcHarvester{fn: func(call int, ctx context.Context, headless bool, accept func(Credential) bool) (bool, error) {
		close(started)
		<-release
		return false, nil
	}}
	sa, _, _ := newTestAuth(t, h, twoAccounts())
	go func() { _, _, _, _ = sa.Get(context.Background()) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, _, err := sa.Get(ctx); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a context error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter ignored its context")
	}
	close(release)
}

// Logout gives up waiting for a harvester that ignores cancellation; a Login that
// then starts must not be overwritten by the old harvest finishing later. Only the
// epoch (not the suspended flag, which Login has cleared) can discard it.
func TestUnitStaleHarvestDiscardedByEpochAfterLogoutTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := &funcHarvester{fn: func(call int, ctx context.Context, headless bool, accept func(Credential) bool) (bool, error) {
		if call == 1 {
			close(started)
			<-release
			return accept(cred("xoxc-A")), nil
		}
		return accept(cred("xoxc-B")), nil
	}}
	sa, store, _ := newTestAuth(t, h, twoAccounts())
	sa.logoutWait = 50 * time.Millisecond

	getErr := make(chan error, 1)
	go func() { _, _, _, err := sa.Get(context.Background()); getErr <- err }()
	<-started
	if err := sa.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	loginID := make(chan *Identity, 1)
	go func() { id, _ := sa.Login(context.Background()); loginID <- id }()
	time.Sleep(100 * time.Millisecond) // Login is queued behind the stuck harvest
	close(release)                     // the old harvest now finishes with suspended=false

	if err := <-getErr; err == nil {
		t.Fatal("the pre-logout harvest must be discarded")
	}
	id := <-loginID
	if id == nil || id.UserID != "U2" {
		t.Fatalf("login must end up on the new account, got %v", id)
	}
	if p := store.Pin(); p == nil || p.UserID != "U2" {
		t.Fatalf("pin = %v", p)
	}
}
