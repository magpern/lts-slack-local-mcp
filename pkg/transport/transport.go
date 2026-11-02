package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// IsSlackURL reports whether rawURL is an HTTPS URL on a Slack-owned domain.
func IsSlackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && allowedHost(u)
}

// allowedHost reports whether u is an HTTPS URL on a Slack-owned domain.
// Replaceable in tests that point the client at a local fake server.
var allowedHost = func(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range []string{"slack.com", "slack-edge.com", "slack-files.com", "slack-gov.com"} {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// UserAgentTransport wraps another RoundTripper to add User-Agent and cookies
type UserAgentTransport struct {
	roundTripper http.RoundTripper
	userAgent    string
	cookies      []*http.Cookie
	logger       *zap.Logger
}

// NewUserAgentTransport creates a new UserAgentTransport
func NewUserAgentTransport(roundTripper http.RoundTripper, userAgent string, cookies []*http.Cookie, logger *zap.Logger) *UserAgentTransport {
	return &UserAgentTransport{
		roundTripper: roundTripper,
		userAgent:    userAgent,
		cookies:      cookies,
		logger:       logger,
	}
}

// RoundTrip implements the RoundTripper interface
func (t *UserAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Everything this client sends carries Slack session credentials (the d
	// cookie, and the token in the body or Authorization header). Refuse any
	// request, including redirect hops, that is not HTTPS to a Slack host, so a
	// hostile file URL or redirect cannot receive them.
	if !allowedHost(req.URL) {
		return nil, fmt.Errorf("refusing to send a Slack session to a non-Slack or non-HTTPS URL (%s)", req.URL.Hostname())
	}
	clonedReq := req.Clone(req.Context())
	clonedReq.Header.Set("User-Agent", t.userAgent)

	for _, cookie := range t.cookies {
		clonedReq.AddCookie(cookie)
	}

	t.logger.Debug("Making request", zap.String("url", clonedReq.URL.String()))

	resp, err := t.roundTripper.RoundTrip(clonedReq)
	if err != nil {
		t.logger.Error("Request failed", zap.Error(err))
	}
	return resp, err
}

// ProvideHTTPClient creates the HTTP client used for every Slack request.
func ProvideHTTPClient(cookies []*http.Cookie, logger *zap.Logger) *http.Client {
	if os.Getenv("SLACK_MCP_CUSTOM_TLS") != "" {
		logger.Fatal("SLACK_MCP_CUSTOM_TLS is no longer supported (TLS fingerprint impersonation was removed)")
	}

	var proxy func(*http.Request) (*url.URL, error)
	if proxyURL := os.Getenv("SLACK_MCP_PROXY"); proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			logger.Fatal("Failed to parse proxy URL",
				zap.String("proxy_url", proxyURL),
				zap.Error(err))
		}
		proxy = http.ProxyURL(parsed)
	}

	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}

	if localCertFile := os.Getenv("SLACK_MCP_SERVER_CA"); localCertFile != "" {
		certs, err := ioutil.ReadFile(localCertFile)
		if err != nil {
			logger.Fatal("Failed to read local certificate file",
				zap.String("cert_file", localCertFile),
				zap.Error(err))
		}
		if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
			logger.Warn("No certs appended, using system certs only")
		}
	}

	// Disabling certificate verification is not supported: this process holds
	// session credentials and must never send them over an unverified channel.
	for _, removed := range []string{"SLACK_MCP_SERVER_CA_INSECURE", "SLACK_MCP_SERVER_CA_TOOLKIT"} {
		if os.Getenv(removed) != "" {
			logger.Fatal(removed + " is no longer supported; trust a custom CA with SLACK_MCP_SERVER_CA instead")
		}
	}

	userAgent := defaultUA
	if ua := os.Getenv("SLACK_MCP_USER_AGENT"); ua != "" {
		userAgent = ua
	}

	var transport http.RoundTripper = &http.Transport{
		Proxy: proxy,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    rootCAs,
		},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	transport = NewUserAgentTransport(transport, userAgent, cookies, logger)

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if !allowedHost(req.URL) {
				return fmt.Errorf("refusing redirect to non-Slack or non-HTTPS host %q", req.URL.Hostname())
			}
			return nil
		},
	}

	return client
}
