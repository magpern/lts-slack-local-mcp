package edge

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type scriptedClient struct {
	bodies []string
	status []int
}

func (s *scriptedClient) Do(req *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(req.Body)
	s.bodies = append(s.bodies, string(b))
	code := s.status[len(s.bodies)-1]
	h := http.Header{}
	if code == http.StatusTooManyRequests {
		h.Set("Retry-After", "0")
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

// After a 429 the retry must carry the full request body (including the token);
// a consumed body would go out empty and look like an authentication failure.
func TestUnitRateLimitRetryResendsFullBody(t *testing.T) {
	sc := &scriptedClient{status: []int{http.StatusTooManyRequests, http.StatusOK}}
	cl := &Client{cl: sc, token: "xoxc-secret", webclientAPI: "https://x.slack.com/api/", tape: nopTape{}}
	form := url.Values{"channel": {"C1"}}
	resp, err := cl.PostFormRaw(context.Background(), "https://x.slack.com/api/x.y", form)
	if err != nil {
		t.Fatalf("PostFormRaw: %v", err)
	}
	resp.Body.Close()
	if len(sc.bodies) != 2 {
		t.Fatalf("expected one retry, got %d requests", len(sc.bodies))
	}
	if sc.bodies[0] == "" || sc.bodies[1] != sc.bodies[0] {
		t.Fatalf("retry body differs from the first attempt: %q vs %q", sc.bodies[1], sc.bodies[0])
	}
	if !strings.Contains(sc.bodies[1], "token=xoxc-secret") {
		t.Fatalf("retry lost the token: %q", sc.bodies[1])
	}
}
