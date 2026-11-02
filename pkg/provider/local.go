package provider

import (
	"context"
	"os"
	"sync/atomic"

	"github.com/magpern/lts-slack-local-mcp/pkg/auth"
	"github.com/magpern/lts-slack-local-mcp/pkg/limiter"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// NewLocal builds a provider whose Slack client is created lazily from a
// SessionAuth. It never contacts Slack or fails at construction, so the server
// can start signed out; login/logout and tool listing work throughout.
func NewLocal(transport string, logger *zap.Logger, sess *auth.SessionAuth) *ApiProvider {
	ap := &ApiProvider{
		transport:          transport,
		logger:             logger,
		sess:               sess,
		rateLimiter:        limiter.Tier2.Limiter(),
		cacheTTL:           getCacheTTL(),
		minRefreshInterval: getMinRefreshInterval(),
	}
	// Tool registration depends on the token kind, which must be known before
	// login: a browser session is always xoxc; an env override is by prefix.
	if cred, ok := auth.EnvCredential(); ok {
		ap.isOAuth = auth.IsOAuthToken(cred.Token)
		ap.isBot = auth.IsBotToken(cred.Token)
	}
	ap.sc = newSessionClient(sess, logger, ap.bindIdentity)
	ap.client = ap.sc
	ap.resetCaches()
	return ap
}

// Session returns the authentication lifecycle.
func (ap *ApiProvider) Session() *auth.SessionAuth { return ap.sess }

// Connect acquires a credential (headless only; never opens a window) and binds
// the Slack client, which also starts cache warming for the account.
func (ap *ApiProvider) Connect(ctx context.Context) error {
	_, err := ap.sc.connect(ctx)
	return err
}

// Login runs an explicit sign-in (may open a browser window), then binds the
// client so the caches warm for the new account.
func (ap *ApiProvider) Login(ctx context.Context) (*auth.Identity, error) {
	id, err := ap.sess.Login(ctx)
	if err != nil {
		return nil, err
	}
	if err := ap.Connect(ctx); err != nil {
		return nil, err
	}
	return id, nil
}

// Logout signs out, deletes the browser profile, and removes the cached Slack
// data for the account. It is safe to call when already signed out.
func (ap *ApiProvider) Logout(ctx context.Context) (profileDeleted bool, err error) {
	id, ok := ap.sc.identity()
	if !ok {
		if pin := ap.sess.Pin(); pin != nil {
			id, ok = *pin, true
		}
	}
	err = ap.sess.Logout(ctx)
	ap.sc.reset()
	ap.bound.Store(nil)
	ap.resetCaches()
	if ok {
		ap.removeCacheFiles(id)
	}
	return err == nil, err
}

// bindIdentity runs whenever the inner client is (re)built for a different
// account: caches are repartitioned per user and re-warmed.
func (ap *ApiProvider) bindIdentity(id auth.Identity) {
	prev := ap.bound.Swap(&id)
	if prev != nil && *prev == id {
		return
	}
	up, uerr := ap.cachePath(id, "users_cache.json", "SLACK_MCP_USERS_CACHE")
	cp, cerr := ap.cachePath(id, "channels_cache_v2.json", "SLACK_MCP_CHANNELS_CACHE")
	if uerr != nil || cerr != nil {
		// Never fall back to a shared or working-directory path (cache holds PII).
		ap.logger.Warn("cache directory unavailable; running without a disk cache")
		up, cp = "", ""
	}
	ap.pathsMu.Lock()
	ap.usersCachePath, ap.channelsCachePath = up, cp
	ap.pathsMu.Unlock()
	if prev != nil {
		ap.resetCaches() // account switch: drop the previous user's data
	}
	if ap.skipCache.Load() {
		return
	}
	go ap.warmCaches()
}

func (ap *ApiProvider) cachePath(id auth.Identity, name, envKey string) (string, error) {
	if v := os.Getenv(envKey); v != "" {
		return v, nil
	}
	return cachePathFor(id.TeamID, id.UserID, name)
}

func (ap *ApiProvider) warmCaches() {
	ctx, cancel := bg()
	defer cancel()
	if err := ap.RefreshUsers(ctx); err != nil {
		ap.logger.Warn("users cache warm-up failed", zap.Error(err))
	}
	if err := ap.RefreshChannels(ctx); err != nil {
		ap.logger.Warn("channels cache warm-up failed", zap.Error(err))
	}
	if ready, _ := ap.IsReady(); ready {
		ap.logger.Info("Slack MCP Server is fully ready")
	}
}

func (ap *ApiProvider) resetCaches() {
	ap.usersSnapshot.Store(&UsersCache{Users: map[string]slack.User{}, UsersInv: map[string]string{}})
	ap.channelsSnapshot.Store(&ChannelsCache{Channels: map[string]Channel{}, ChannelsInv: map[string]string{}})
	ap.usersReady.Store(false)
	ap.channelsReady.Store(false)
}

func (ap *ApiProvider) removeCacheFiles(id auth.Identity) {
	for _, name := range []string{"users_cache.json", "channels_cache_v2.json"} {
		if p, err := cachePathFor(id.TeamID, id.UserID, name); err == nil {
			_ = os.Remove(p)
		}
	}
	ap.pathsMu.Lock()
	ap.usersCachePath, ap.channelsCachePath = "", ""
	ap.pathsMu.Unlock()
}

func (ap *ApiProvider) usersPath() string {
	ap.pathsMu.RLock()
	defer ap.pathsMu.RUnlock()
	return ap.usersCachePath
}

func (ap *ApiProvider) channelsPath() string {
	ap.pathsMu.RLock()
	defer ap.pathsMu.RUnlock()
	return ap.channelsCachePath
}

// boundIdentity is stored as an atomic pointer on the provider.
type boundIdentity = atomic.Pointer[auth.Identity]
