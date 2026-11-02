package transport

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"go.uber.org/zap"
)

type recordRT struct{ hit *[]string }

func (r recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	*r.hit = append(*r.hit, req.URL.Host+"|cookie="+req.Header.Get("Cookie"))
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func TestUnitTransportOnlyTalksHTTPSToSlack(t *testing.T) {
	var hit []string
	tr := NewUserAgentTransport(recordRT{&hit}, "ua", []*http.Cookie{{Name: "d", Value: "SECRET"}}, zap.NewNop())

	for _, bad := range []string{
		"https://evil.example/x", "http://files.slack.com/x", "https://slack.com.evil.example/x",
		"https://evilslack.com/x", "ftp://slack.com/x",
	} {
		req, _ := http.NewRequest("GET", bad, nil)
		if _, err := tr.RoundTrip(req); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
	if len(hit) != 0 {
		t.Fatalf("credentials were sent to a refused host: %v", hit)
	}
	for _, ok := range []string{"https://slack.com/api/x", "https://files.slack.com/x", "https://edgeapi.slack.com/x", "https://a.slack-edge.com/x"} {
		req, _ := http.NewRequest("GET", ok, nil)
		if _, err := tr.RoundTrip(req); err != nil {
			t.Errorf("%s should be allowed: %v", ok, err)
		}
	}
}

// A redirect from a Slack host to a foreign host must not be followed, so the
// session cookie and Authorization header never reach it.
func TestUnitRedirectToForeignHostIsRefused(t *testing.T) {
	foreignHit := false
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignHit = true
	}))
	defer foreign.Close()

	// Treat the first server as a Slack host for this test only.
	slackish := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/steal", http.StatusFound)
	}))
	defer slackish.Close()
	su, _ := url.Parse(slackish.URL)
	old := allowedHost
	allowedHost = func(u *url.URL) bool { return u.Host == su.Host }
	defer func() { allowedHost = old }()

	c := slackish.Client()
	c.Transport = NewUserAgentTransport(c.Transport, "ua", []*http.Cookie{{Name: "d", Value: "SECRET"}}, zap.NewNop())
	req, _ := http.NewRequest("GET", slackish.URL, nil)
	req.Header.Set("Authorization", "Bearer SECRET")
	// The default redirect policy follows the hop through our transport, which
	// must refuse it before anything is sent.
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect to a foreign host must fail")
	}
	if foreignHit {
		t.Fatal("the redirect reached the foreign host")
	}
}

func TestUnitIsSlackURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://files.slack.com/files-pri/T-F/x": true, "https://slack.com/": true, "http://slack.com/": false,
		"https://evil.example/": false, "": false, "https://slack.com.evil.example/": false,
	} {
		if IsSlackURL(u) != want {
			t.Errorf("IsSlackURL(%q) != %v", u, want)
		}
	}
}
