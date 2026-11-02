package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// The unit tests elsewhere reuse one in-memory Store to simulate a restart; this
// test reloads from DISK, which is what a real restart does.
func TestUnitStorePersistsAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := s.SetPin(Identity{TeamID: "T1", UserID: "U1"}); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := s.MarkSignedOut(); err != nil {
		t.Fatalf("MarkSignedOut: %v", err)
	}
	if err := s.SetProfileDirty(true); err != nil {
		t.Fatalf("SetProfileDirty: %v", err)
	}

	s2, err := OpenStore(dir) // fresh process
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !s2.SignedOut() || !s2.ProfileDirty() {
		t.Fatalf("signed-out/dirty flags did not survive a reopen")
	}
	if err := s2.SetPin(Identity{TeamID: "T2", UserID: "U2"}); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	s3, _ := OpenStore(dir)
	if p := s3.Pin(); p == nil || p.TeamID != "T2" || s3.SignedOut() {
		t.Fatalf("pin did not persist or signed-out was not cleared: %+v", p)
	}
}

// The directory must stay fully usable by its owner after lockdown (a previous
// implementation emptied the ACL and made it inaccessible on Windows).
func TestUnitLockdownLeavesDirUsable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locked")
	if err := secureMkdirAll(dir); err != nil {
		t.Fatalf("secureMkdirAll: %v", err)
	}
	f := filepath.Join(dir, "x.json")
	if err := secureWriteFile(f, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("write in locked dir: %v", err)
	}
	if b, err := os.ReadFile(f); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("read back: %q %v", b, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("owner must be able to delete the dir: %v", err)
	}
}
