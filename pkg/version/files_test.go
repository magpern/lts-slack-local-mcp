package version

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The version is single-sourced in the VERSION file; the committed plugin
// manifest must agree so a stale manifest cannot ship.
func TestUnitPluginManifestMatchesVersionFile(t *testing.T) {
	v, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../plugin/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(v)); m.Version != want {
		t.Fatalf("plugin.json version %q != VERSION %q", m.Version, want)
	}
}
