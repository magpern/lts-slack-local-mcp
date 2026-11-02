package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/magpern/lts-slack-local-mcp/pkg/auth"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// fakeSlack is an httptest server that accepts only the tokens in `valid` and
// otherwise answers HTTP 200 {"ok":false,"error":<reject>}, like the real API.
type fakeSlack struct {
	srv    *httptest.Server
	mu     sync.Mutex
	valid  map[string]bool
	reject string // error code returned for invalid tokens
	perm   string // if set, every authenticated call fails with this error code
	hits   map[string]int
}

func newFakeSlack(valid ...string) *fakeSlack {
	f := &fakeSlack{valid: map[string]bool{}, hits: map[string]int{}, reject: "invalid_auth"}
	for _, v := range valid {
		f.valid[v] = true
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeSlack) handle(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	method := strings.TrimPrefix(r.URL.Path, "/")
	tok := r.Form.Get("token")
	if tok == "" {
		tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	f.mu.Lock()
	f.hits[method]++
	ok := f.valid[tok]
	perm := f.perm
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case !ok:
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": f.reject})
	case perm != "":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": perm})
	case method == "chat.postMessage":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C1", "ts": "1.1"})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "users": []any{map[string]any{"id": "U1", "name": "u"}},
		})
	}
}

func (f *fakeSlack) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[method]
}

// scriptHarvester hands out credentials in order, one per Harvest call.
type scriptHarvester struct {
	mu    sync.Mutex
	creds []auth.Credential
	calls int
}

func (h *scriptHarvester) Harvest(_ context.Context, _ bool, _ time.Duration, accept func(auth.Credential) bool) (bool, error) {
	h.mu.Lock()
	h.calls++
	var c *auth.Credential
	if len(h.creds) > 0 {
		c, h.creds = &h.creds[0], h.creds[1:]
	}
	h.mu.Unlock()
	if c == nil {
		return false, nil
	}
	return accept(*c), nil
}

func (h *scriptHarvester) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

type okValidator struct{}

func (okValidator) Identify(context.Context, auth.Credential) (*auth.Identity, error) {
	return &auth.Identity{TeamID: "T1", UserID: "U1"}, nil
}

func cr(tok string) auth.Credential { return auth.Credential{Token: tok, Cookie: "xoxd-" + tok} }

func newTestSessionClient(t *testing.T, f *fakeSlack, creds ...auth.Credential) (*sessionClient, *scriptHarvester) {
	t.Helper()
	dir := t.TempDir()
	cfg := auth.Config{
		ProfileDir: filepath.Join(dir, "p"), DataDir: filepath.Join(dir, "s"),
		LoginTimeout: time.Second, HeadlessTimeout: time.Second, TokenTTL: time.Hour,
	}
	store, err := auth.OpenStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	h := &scriptHarvester{creds: creds}
	sess := auth.NewSessionAuth(cfg, store, h, okValidator{}, nil)
	sess.AllowAutoFirstLogin()
	sc := newSessionClient(sess, zap.NewNop(), nil)
	sc.newClient = func(c auth.Credential) (*MCPSlackClient, error) {
		return &MCPSlackClient{slackClient: slack.New(c.Token, slack.OptionAPIURL(f.srv.URL+"/"))}, nil
	}
	return sc, h
}

func TestUnitReadRetriesOnceAfterReacquire(t *testing.T) {
	f := newFakeSlack("xoxc-NEW") // OLD is rejected
	defer f.srv.Close()
	sc, h := newTestSessionClient(t, f, cr("xoxc-OLD"), cr("xoxc-NEW"))

	if _, err := sc.GetUsersInfo("U1"); err != nil {
		t.Fatalf("read should succeed after re-acquire: %v", err)
	}
	if got := f.count("users.info"); got != 2 {
		t.Fatalf("expected one rejected + one retried read, got %d", got)
	}
	if h.callCount() != 2 {
		t.Fatalf("expected 2 harvests, got %d", h.callCount())
	}
}

func TestUnitMutationIsNeverReplayed(t *testing.T) {
	f := newFakeSlack("xoxc-NEW")
	defer f.srv.Close()
	sc, _ := newTestSessionClient(t, f, cr("xoxc-OLD"), cr("xoxc-NEW"))

	_, _, err := sc.PostMessageContext(context.Background(), "C1", slack.MsgOptionText("hi", false))
	if err == nil || !strings.Contains(err.Error(), "NOT retried") {
		t.Fatalf("expected a not-retried error, got %v", err)
	}
	if got := f.count("chat.postMessage"); got != 1 {
		t.Fatalf("mutation must be sent exactly once, got %d", got)
	}
	// The session was refreshed, so the NEXT explicit call works.
	if _, _, err := sc.PostMessageContext(context.Background(), "C1", slack.MsgOptionText("again", false)); err != nil {
		t.Fatalf("follow-up mutation should succeed on the refreshed session: %v", err)
	}
	if got := f.count("chat.postMessage"); got != 2 {
		t.Fatalf("expected exactly 2 sends in total, got %d", got)
	}
}

func TestUnitPermissionErrorDoesNotReharvest(t *testing.T) {
	f := newFakeSlack("xoxc-A")
	f.perm = "missing_scope"
	defer f.srv.Close()
	sc, h := newTestSessionClient(t, f, cr("xoxc-A"), cr("xoxc-B"))

	if _, err := sc.GetUsersInfo("U1"); err == nil {
		t.Fatal("expected the permission error to be returned")
	}
	if h.callCount() != 1 {
		t.Fatalf("a permission error must not trigger re-acquire, harvests=%d", h.callCount())
	}
	if got := f.count("users.info"); got != 1 {
		t.Fatalf("a permission error must not be retried, got %d", got)
	}
}

func TestUnitPersistentRejectionIsBounded(t *testing.T) {
	f := newFakeSlack() // nothing is valid: e.g. an IP/session restriction
	defer f.srv.Close()
	sc, h := newTestSessionClient(t, f, cr("xoxc-A"), cr("xoxc-B"), cr("xoxc-C"))

	_, err := sc.GetUsersInfo("U1")
	if err == nil || !strings.Contains(err.Error(), "re-acquisition did not help") {
		t.Fatalf("expected ErrAuthRejected, got %v", err)
	}
	if got := f.count("users.info"); got != 2 {
		t.Fatalf("retry must be bounded to one, got %d attempts", got)
	}
	if h.callCount() != 2 {
		t.Fatalf("expected exactly 2 harvests, got %d", h.callCount())
	}
}

func TestUnitIsAuthErrorClassification(t *testing.T) {
	for _, c := range []string{"invalid_auth", "token_expired", "token_revoked", "not_authed", "account_inactive"} {
		if !isAuthError(slack.SlackErrorResponse{Err: c}) {
			t.Errorf("%s should be an auth error", c)
		}
	}
	for _, c := range []string{"missing_scope", "not_allowed_token_type", "channel_not_found", "ratelimited", "internal_error"} {
		if isAuthError(slack.SlackErrorResponse{Err: c}) {
			t.Errorf("%s must NOT be an auth error", c)
		}
	}
	if isAuthError(nil) {
		t.Error("nil is not an auth error")
	}
}
