package auth

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Harvester drives a browser (headless or visible) against a dedicated profile
// and offers each captured credential to accept. It returns true once accept
// has returned true for a credential. Implementations must honour ctx/timeout.
type Harvester interface {
	Harvest(ctx context.Context, headless bool, timeout time.Duration, accept func(Credential) bool) (bool, error)
}

// Validator validates a credential over the network and reports its identity.
// It must fail closed: any uncertainty returns a non-nil error.
type Validator interface {
	Identify(ctx context.Context, c Credential) (*Identity, error)
}

type candidate struct {
	cred      Credential
	id        Identity
	refreshAt time.Time
}

// SessionAuth owns the live credential and re-acquires it as needed.
//
// Concurrency: a single mutex guards all fields, but it is NOT held during the
// (possibly minutes-long) browser harvest. Only one harvest runs at a time
// (guarded by the harvesting flag and a cond); other callers wait and then
// reuse the result. A generation counter lets a stale auth failure be scoped to
// the exact credential that failed, and lets a harvest that finishes after a
// logout be discarded.
type SessionAuth struct {
	cfg   Config
	store *Store
	harv  Harvester
	val   Validator
	log   *zap.Logger
	clock func() time.Time

	// Filesystem hooks, replaceable in tests to inject cleanup failures.
	logoutWait time.Duration // how long Logout waits for a harvest to stop
	removeAll  func(string) error
	rename     func(oldpath, newpath string) error

	lifeMu     sync.Mutex // serializes Login and Logout, including profile deletion
	mu         sync.Mutex
	cond       *sync.Cond
	harvesting bool
	gen        uint64
	cur        *candidate
	rejected   map[string]bool
	suspended  bool
	epoch      uint64             // bumped by Logout and Login; a harvest from an older epoch is discarded
	cancelHarv context.CancelFunc // cancels the in-flight harvest

	// autoFirst allows an unattended first acquisition with no pinned identity.
	// Used only with a static (environment-supplied) credential source.
	autoFirst bool
}

// NewSessionAuth builds the lifecycle, loading persisted signed-out state.
func NewSessionAuth(cfg Config, store *Store, harv Harvester, val Validator, log *zap.Logger) *SessionAuth {
	if log == nil {
		log = zap.NewNop()
	}
	s := &SessionAuth{
		cfg: cfg, store: store, harv: harv, val: val, log: log,
		clock:      time.Now,
		logoutWait: 25 * time.Second,
		removeAll:  os.RemoveAll,
		rename:     os.Rename,
		rejected:   map[string]bool{},
		suspended:  store.SignedOut(),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// AllowAutoFirstLogin lets Get acquire a first credential without a pinned
// identity and without an explicit Login. It is intended for static credential
// sources (environment variables); browser harvesting stays opt-in via Login.
func (s *SessionAuth) AllowAutoFirstLogin() {
	s.mu.Lock()
	s.autoFirst = true
	s.mu.Unlock()
}

// Generation returns the current credential generation.
func (s *SessionAuth) Generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func (s *SessionAuth) validLocked() bool {
	if s.cur == nil || s.rejected[s.cur.cred.FP()] {
		return false
	}
	return s.clock().Before(s.cur.refreshAt)
}

// Get returns a usable credential, acquiring one if necessary.
func (s *SessionAuth) Get(ctx context.Context) (Credential, Identity, uint64, error) {
	return s.acquire(ctx, modeAuto)
}

// HandleAuthFailure is called when Slack rejects the credential at failedGen. It
// marks that credential rejected and re-acquires, unless a newer generation
// already installed a valid credential (then that is returned).
func (s *SessionAuth) HandleAuthFailure(ctx context.Context, failedGen uint64, failedFP string) (Credential, Identity, uint64, error) {
	s.mu.Lock()
	if s.suspended {
		s.mu.Unlock()
		return Credential{}, Identity{}, 0, ErrSignedOut
	}
	if failedFP != "" {
		s.rejected[failedFP] = true
	}
	if s.gen != failedGen && s.validLocked() {
		c, id, g := s.cur.cred, s.cur.id, s.gen
		s.mu.Unlock()
		return c, id, g, nil
	}
	s.mu.Unlock()
	return s.acquire(ctx, modeAuto)
}

// Login forces a fresh acquisition and re-pins whatever allowed account is
// signed in, re-enabling auto-login. It authorizes switching accounts.
func (s *SessionAuth) Login(ctx context.Context) (*Identity, error) {
	s.lifeMu.Lock()
	// A profile left behind by a failed logout must never be reused.
	if s.store.ProfileDirty() {
		if err := s.deleteProfile(); err != nil {
			s.lifeMu.Unlock()
			return nil, fmt.Errorf("a previous logout could not remove the browser profile; close any browser using it and call login again: %w", err)
		}
	}
	s.mu.Lock()
	s.epoch++
	s.suspended = false
	s.cur = nil
	s.rejected = map[string]bool{}
	_ = s.store.ClearSignedOut()
	s.mu.Unlock()
	// The sign-in itself (which can wait minutes for the user) runs outside the
	// lifecycle lock so Logout can interrupt it: Logout bumps the epoch, cancels
	// the harvest and this acquire is discarded.
	s.lifeMu.Unlock()
	_, id, _, err := s.acquire(ctx, modeLogin)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// Logout clears the live credential, persists the signed-out state, and deletes
// the dedicated profile (quarantining it if deletion fails). A harvest already
// in flight will find suspended=true when it completes and discard its result.
func (s *SessionAuth) Logout(ctx context.Context) error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	s.epoch++
	s.suspended = true
	s.cur = nil
	s.rejected = map[string]bool{}
	persistErr := s.store.MarkSignedOut()
	if s.cancelHarv != nil {
		s.cancelHarv() // stop any browser before touching its profile
	}
	s.waitIdleLocked(s.logoutWait)
	s.mu.Unlock()
	if persistErr != nil {
		// Without a durable signed-out marker a restart would reconnect.
		_ = s.deleteProfile()
		return fmt.Errorf("could not persist the signed-out state: %w", persistErr)
	}
	return s.deleteProfile()
}

// waitIdleLocked waits (bounded) for the in-flight harvest to finish. s.mu held.
func (s *SessionAuth) waitIdleLocked(max time.Duration) {
	t := time.AfterFunc(max, func() { s.mu.Lock(); s.cond.Broadcast(); s.mu.Unlock() })
	defer t.Stop()
	deadline := time.Now().Add(max)
	for s.harvesting && time.Now().Before(deadline) {
		s.cond.Wait()
	}
}

// deleteProfile removes the dedicated profile. The dirty flag is persisted
// first and cleared only on success, so a failure leaves a durable marker that
// blocks reuse. If removal fails, the profile is quarantined (renamed); a
// quarantined profile is no longer at ProfileDir, so it cannot be reused.
func (s *SessionAuth) deleteProfile() error {
	dir := s.cfg.ProfileDir
	if dir == "" {
		return nil
	}
	wasDirty := s.store.ProfileDirty()
	if exists, empty, ours := dirState(dir); exists && !empty && !ours && !wasDirty {
		return ErrDirNotOwned // never delete a directory this tool did not create
	}
	_ = s.store.SetProfileDirty(true)
	if err := s.removeAll(dir); err != nil {
		q := dir + ".quarantined-" + s.clock().Format("20060102T150405")
		if rerr := s.rename(dir, q); rerr != nil {
			s.log.Warn("profile deletion and quarantine both failed", zap.Error(err))
			return err // dirty flag stays set
		}
		s.log.Warn("profile deletion failed; quarantined residual profile")
	}
	_ = s.store.SetProfileDirty(false)
	return nil
}

type acquireMode int

const (
	// modeAuto is an unattended re-acquire: headless only, persisted pin must match.
	modeAuto acquireMode = iota
	// modeLogin is explicit: headless first, then a visible window, any allowed
	// account is accepted and re-pinned.
	modeLogin
)

// acquire obtains a credential. Only one harvest runs at a time; concurrent
// callers wait on the cond and reuse the result.
func (s *SessionAuth) acquire(ctx context.Context, mode acquireMode) (Credential, Identity, uint64, error) {
	allowSwitch := mode == modeLogin
	stop := context.AfterFunc(ctx, func() { s.mu.Lock(); s.cond.Broadcast(); s.mu.Unlock() })
	defer stop()
	s.mu.Lock()
	for {
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return Credential{}, Identity{}, 0, err
		}
		if s.suspended {
			s.mu.Unlock()
			return Credential{}, Identity{}, 0, ErrSignedOut
		}
		if !allowSwitch && s.validLocked() {
			c, id, g := s.cur.cred, s.cur.id, s.gen
			s.mu.Unlock()
			return c, id, g, nil
		}
		if s.harvesting {
			s.cond.Wait()
			continue
		}
		break
	}
	// We are the harvester. Snapshot state, then release the lock for the slow part.
	var pin *Identity
	if !allowSwitch {
		pin = s.store.Pin()
		if pin == nil && !s.autoFirst {
			// First run: never open a window or block a tool call on an
			// unattended sign-in. The user must call login explicitly.
			s.mu.Unlock()
			return Credential{}, Identity{}, 0, ErrNotLoggedIn
		}
	}
	s.harvesting = true
	epoch := s.epoch
	hctx, cancel := context.WithCancel(ctx)
	s.cancelHarv = cancel
	rejected := make(map[string]bool, len(s.rejected))
	for k := range s.rejected {
		rejected[k] = true
	}
	s.mu.Unlock()

	accepted, refusals := s.safeHarvest(hctx, pin, rejected, allowSwitch)
	cancel()

	s.mu.Lock()
	s.harvesting = false
	s.cancelHarv = nil
	s.cond.Broadcast()
	if s.suspended || s.epoch != epoch { // logout/login happened during the harvest: discard.
		s.mu.Unlock()
		if s.suspended {
			return Credential{}, Identity{}, 0, ErrSignedOut
		}
		return Credential{}, Identity{}, 0, ErrSuperseded
	}
	if accepted == nil {
		// A proactive TTL refresh that fails must not discard a credential Slack
		// has not rejected: keep serving it and retry the refresh later.
		if !allowSwitch && s.cur != nil && !s.rejected[s.cur.cred.FP()] {
			s.cur.refreshAt = s.clock().Add(time.Minute)
			c, id, g := s.cur.cred, s.cur.id, s.gen
			s.mu.Unlock()
			return c, id, g, nil
		}
		s.mu.Unlock()
		return Credential{}, Identity{}, 0, refusalError(refusals)
	}
	s.gen++
	s.cur = accepted
	g := s.gen
	if allowSwitch || pin == nil {
		if err := s.store.SetPin(accepted.id); err != nil {
			s.log.Warn("failed to persist identity pin", zap.Error(err))
		}
	}
	s.log.Info("acquired Slack session",
		zap.String("cred", accepted.cred.FP()), zap.Uint64("generation", g))
	s.mu.Unlock()
	return accepted.cred, accepted.id, g, nil
}

// safeHarvest runs runHarvest, converting a panic in the harvester or validator
// into a failed harvest so the single-harvester flag is always released.
func (s *SessionAuth) safeHarvest(ctx context.Context, pin *Identity, rejected map[string]bool, interactive bool) (c *candidate, r map[string]bool) {
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("harvest panicked", zap.Any("panic", rec))
			c, r = nil, map[string]bool{}
		}
	}()
	return s.runHarvest(ctx, pin, rejected, interactive)
}

// runHarvest performs the harvest without holding the mutex: headless first,
// then (only when interactive, i.e. an explicit login) a visible window if
// nothing valid was found and no account was explicitly refused.
func (s *SessionAuth) runHarvest(ctx context.Context, pin *Identity, rejected map[string]bool, interactive bool) (*candidate, map[string]bool) {
	var accepted *candidate
	refusals := map[string]bool{}

	refused := map[string]bool{} // fingerprints already refused: do not re-validate every poll
	accept := func(c Credential) bool {
		if accepted != nil || rejected[c.FP()] || refused[c.FP()] {
			return accepted != nil
		}
		id, err := s.val.Identify(ctx, c)
		if err != nil || id == nil {
			return false
		}
		if !s.cfg.teamAllowed(id.TeamID) {
			refusals["notallowed"] = true
			refused[c.FP()] = true
			return false
		}
		if pin != nil && !id.SameAccount(*pin) {
			refusals["identity"] = true
			refused[c.FP()] = true
			return false
		}
		accepted = &candidate{cred: c, id: *id, refreshAt: s.clock().Add(s.ttl())}
		return true
	}

	_, _ = s.harv.Harvest(ctx, true, s.cfg.HeadlessTimeout, accept)
	if interactive && accepted == nil && ctx.Err() == nil {
		_, _ = s.harv.Harvest(ctx, false, s.cfg.LoginTimeout, accept)
	}
	return accepted, refusals
}

func (s *SessionAuth) ttl() time.Duration {
	if s.cfg.TokenTTL > 0 {
		return s.cfg.TokenTTL
	}
	return defaultTokenTTL
}

func refusalError(refusals map[string]bool) error {
	switch {
	case refusals["identity"]:
		return ErrIdentityChanged
	case refusals["notallowed"]:
		return ErrNotAllowed
	default:
		return ErrNoSession
	}
}
