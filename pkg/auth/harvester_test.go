package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The persistent profile is the whole point of the dedicated-browser design: a
// harvest must leave it on disk (rod's launcher.Cleanup would delete it).
func TestUnitHarvestKeepsProfileOnDisk(t *testing.T) {
	h := NewRodHarvester(Config{}, nil)
	if h.resolveBin() == "" {
		t.Skip("no Edge/Chrome installed")
	}
	dir := t.TempDir()
	cfg := Config{ProfileDir: filepath.Join(dir, "profile"), WorkspaceURL: "about:blank"}
	h = NewRodHarvester(cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ok, err := h.Harvest(ctx, true, 3*time.Second, func(Credential) bool { return true })
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if ok {
		t.Fatal("about:blank must not yield a credential")
	}
	entries, err := os.ReadDir(cfg.ProfileDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("profile was removed or empty after harvest: %v (%d entries)", err, len(entries))
	}
}

func TestUnitHarvestFailsClosedWithoutBrowser(t *testing.T) {
	h := &RodHarvester{cfg: Config{ProfileDir: t.TempDir(), BrowserBin: ""}}
	if h.resolveBin() != "" {
		t.Skip("a browser is installed; fail-closed path not reachable here")
	}
	if _, err := h.Harvest(context.Background(), true, time.Second, func(Credential) bool { return false }); err != ErrNoBrowser {
		t.Fatalf("want ErrNoBrowser, got %v", err)
	}
}
