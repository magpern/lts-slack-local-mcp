package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// persisted is the on-disk auth state. It never contains a token or cookie.
type persisted struct {
	Pin       *Identity `json:"pin,omitempty"`
	SignedOut bool      `json:"signed_out"`
}

// Store persists the identity pin and the signed-out flag across restarts.
// It writes owner-only files and tolerates a missing/corrupt file (treated as
// empty). It holds no credentials.
type Store struct {
	path string
	mu   sync.Mutex
	st   persisted
}

// OpenStore loads (or initializes) the state file under dataDir.
func OpenStore(dataDir string) (*Store, error) {
	if err := secureMkdirAll(dataDir); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "session.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.st) // corrupt file -> empty state
	}
	return s, nil
}

// Pin returns the persisted identity pin, or nil if none.
func (s *Store) Pin() *Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Pin == nil {
		return nil
	}
	cp := *s.st.Pin
	return &cp
}

// SignedOut reports whether the persisted state suspends auto-login.
func (s *Store) SignedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.SignedOut
}

// SetPin persists a new identity pin and clears the signed-out flag.
func (s *Store) SetPin(id Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := id
	s.st.Pin = &cp
	s.st.SignedOut = false
	return s.flushLocked()
}

// MarkSignedOut clears the pin and persists the signed-out flag.
func (s *Store) MarkSignedOut() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Pin = nil
	s.st.SignedOut = true
	return s.flushLocked()
}

// ClearSignedOut re-enables auto-login without changing the pin.
func (s *Store) ClearSignedOut() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.SignedOut = false
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	return secureWriteFile(s.path, b)
}
