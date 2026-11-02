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
	removeAll func(string) error
	rename    func(oldpath, newpath string) error

	mu         sync.Mutex
	cond       *sync.Cond
	harvesting bool
	gen        uint64
	cur        *candidate
	rejected   map[string]bool
	suspended  bool

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
		clock:     time.Now,
		removeAll: os.RemoveAll,
		rename:    os.Rename,
		rejected:  map[string]bool{},
		suspended: store.SignedOut(),
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
	// A profile left behind by a failed logout must never be reused.
	if s.store.ProfileDirty() {
		if err := s.deleteProfile(); err != nil {
			return nil, fmt.Errorf("a previous logout could not remove the browser profile; close any browser using it and call login again: %w", err)
		}
	}
	s.mu.Lock()
	s.suspended = false
	s.cur = nil
	s.rejected = map[string]bool{}
	_ = s.store.ClearSignedOut()
	s.mu.Unlock()
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
	s.mu.Lock()
	s.suspended = true
	s.cur = nil
	s.rejected = map[string]bool{}
	if err := s.store.MarkSignedOut(); err != nil {
		s.log.Warn("failed to persist signed-out state", zap.Error(err))
	}
	s.mu.Unlock()
	return s.deleteProfile()
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
	s.mu.Lock()
	for {
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
	rejected := make(map[string]bool, len(s.rejected))
	for k := range s.rejected {
		rejected[k] = true
	}
	s.mu.Unlock()

	accepted, refusals := s.runHarvest(ctx, pin, rejected, allowSwitch)

	s.mu.Lock()
	s.harvesting = false
	s.cond.Broadcast()
	if s.suspended { // logout happened during the harvest: discard the result.
		s.mu.Unlock()
		return Credential{}, Identity{}, 0, ErrSignedOut
	}
	if accepted == nil {
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

// runHarvest performs the harvest without holding the mutex: headless first,
// then (only when interactive, i.e. an explicit login) a visible window if
// nothing valid was found and no account was explicitly refused.
func (s *SessionAuth) runHarvest(ctx context.Context, pin *Identity, rejected map[string]bool, interactive bool) (*candidate, map[string]bool) {
	var accepted *candidate
	refusals := map[string]bool{}

	accept := func(c Credential) bool {
		if accepted != nil || rejected[c.FP()] {
			return accepted != nil
		}
		id, err := s.val.Identify(ctx, c)
		if err != nil || id == nil {
			return false
		}
		if !s.cfg.teamAllowed(id.TeamID) {
			refusals["notallowed"] = true
			return false
		}
		if pin != nil && !id.SameAccount(*pin) {
			refusals["identity"] = true
			return false
		}
		accepted = &candidate{cred: c, id: *id, refreshAt: s.clock().Add(s.ttl())}
		return true
	}

	_, _ = s.harv.Harvest(ctx, true, s.cfg.HeadlessTimeout, accept)
	if interactive && accepted == nil && len(refusals) == 0 {
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
