package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/magpern/lts-slack-local-mcp/pkg/auth"
	"github.com/magpern/lts-slack-local-mcp/pkg/provider/edge"
	slackauth "github.com/rusq/slackdump/v3/auth"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// callTimeout bounds calls made without a caller-supplied context.
const callTimeout = 120 * time.Second

// Slack reports authentication failures as HTTP 200 with {"ok":false,"error":..}.
// These codes mean the credential itself is no longer accepted.
var authErrorCodes = []string{
	"invalid_auth", "not_authed", "token_expired", "token_revoked",
	"account_inactive", "token_not_valid", "no_valid_session",
}

// isAuthError reports whether err means Slack rejected the credential. Permission
// (missing_scope, not_allowed, ...), rate-limit and network errors are NOT auth
// errors: re-acquiring a session cannot fix them.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	var rl *slack.RateLimitedError
	if errors.As(err, &rl) {
		return false
	}
	var se slack.SlackErrorResponse
	if errors.As(err, &se) {
		return containsAuthCode(se.Err)
	}
	return containsAuthCode(err.Error())
}

func containsAuthCode(s string) bool {
	s = strings.ToLower(s)
	for _, c := range authErrorCodes {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

type boundClient struct {
	c   *MCPSlackClient
	gen uint64
	fp  string
	id  auth.Identity
}

// sessionClient implements SlackAPI on top of a SessionAuth. The inner client is
// swapped atomically whenever the credential generation changes. Reads that hit
// an authentication failure are retried once after a re-acquire; mutations are
// NEVER replayed.
type sessionClient struct {
	sess   *auth.SessionAuth
	log    *zap.Logger
	onBind func(auth.Identity)

	buildMu sync.Mutex // serializes inner-client construction
	cur     atomic.Pointer[boundClient]

	// newClient builds the inner client for a credential. Replaceable in tests.
	newClient func(auth.Credential) (*MCPSlackClient, error)
}

func newSessionClient(sess *auth.SessionAuth, log *zap.Logger, onBind func(auth.Identity)) *sessionClient {
	s := &sessionClient{sess: sess, log: log, onBind: onBind}
	s.newClient = s.realClient
	return s
}

// realClient builds the production client, which validates with auth.test.
func (s *sessionClient) realClient(cred auth.Credential) (*MCPSlackClient, error) {
	ap, err := slackauth.NewValueAuth(cred.Token, cred.Cookie)
	if err != nil {
		return nil, fmt.Errorf("invalid credential: %w", err)
	}
	return NewMCPSlackClient(ap, s.log)
}

// connect ensures an inner client exists for the current credential.
// On a bind failure it also returns a descriptor of the credential that failed,
// so an authentication rejection can be scoped to that credential and generation.
func (s *sessionClient) connect(ctx context.Context) (*boundClient, *boundClient, error) {
	cred, id, gen, err := s.sess.Get(ctx)
	if err != nil {
		return nil, nil, err
	}
	b, err := s.bind(cred, id, gen)
	if err != nil {
		return nil, &boundClient{gen: gen, fp: cred.FP(), id: id}, err
	}
	return b, nil, nil
}

func (s *sessionClient) bind(cred auth.Credential, id auth.Identity, gen uint64) (*boundClient, error) {
	if b := s.cur.Load(); b != nil && b.gen >= gen {
		return b, nil // never install an older generation over a newer one
	}
	b, switched, err := s.build(cred, id, gen)
	if err != nil {
		return nil, err
	}
	// onBind runs after the build lock is released: it cancels cache warm-ups,
	// which may themselves be waiting for that lock.
	if switched && s.onBind != nil {
		s.onBind(id)
	}
	return b, nil
}

func (s *sessionClient) build(cred auth.Credential, id auth.Identity, gen uint64) (*boundClient, bool, error) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if b := s.cur.Load(); b != nil && b.gen >= gen {
		return b, false, nil
	}
	c, err := s.newClient(cred)
	if err != nil {
		return nil, false, err
	}
	b := &boundClient{c: c, gen: gen, fp: cred.FP(), id: id}
	prev := s.cur.Swap(b)
	return b, prev == nil || !prev.id.SameAccount(id), nil
}

// reset drops the inner client (used on logout).
func (s *sessionClient) reset() { s.cur.Store(nil) }

// identity returns the bound identity, if any.
func (s *sessionClient) identity() (auth.Identity, bool) {
	if b := s.cur.Load(); b != nil {
		return b.id, true
	}
	return auth.Identity{}, false
}

// do runs fn against the current inner client, handling authentication failure.
func (s *sessionClient) do(ctx context.Context, mutation bool, fn func(*MCPSlackClient) error) error {
	b, failed, err := s.connect(ctx)
	if err != nil && failed != nil && isAuthError(err) {
		// The credential was rejected while building the client.
		return s.recover(ctx, mutation, failed, err, fn)
	}
	if err != nil {
		return err
	}
	err = fn(b.c)
	if err == nil || !isAuthError(err) {
		return err
	}
	return s.recover(ctx, mutation, b, err, fn)
}

func (s *sessionClient) recover(ctx context.Context, mutation bool, failed *boundClient, cause error, fn func(*MCPSlackClient) error) error {
	var gen uint64
	var fp string
	if failed != nil {
		gen, fp = failed.gen, failed.fp
	} else {
		gen = s.sess.Generation()
	}
	s.log.Warn("Slack rejected the credential; re-acquiring",
		zap.String("cred", fp), zap.Uint64("generation", gen), zap.Bool("mutation", mutation))

	cred, id, ngen, rerr := s.sess.HandleAuthFailure(ctx, gen, fp)
	if rerr != nil {
		return rerr
	}
	nb, berr := s.bind(cred, id, ngen)
	if berr != nil {
		return berr
	}
	if mutation && failed != nil && failed.c == nil {
		// Rejected while building the client: the change never reached Slack.
		return fmt.Errorf("Slack rejected the session before the change was sent; the session was refreshed and the change was NOT applied: %w", cause)
	}
	if mutation {
		return fmt.Errorf("Slack rejected the session during this change; the session was refreshed but the change was NOT retried and may or may not have been applied. Check the result before retrying: %w", cause)
	}
	if err := fn(nb.c); err != nil {
		if isAuthError(err) {
			return fmt.Errorf("%w: %v", auth.ErrAuthRejected, err)
		}
		return err
	}
	return nil
}

func bg() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), callTimeout)
}

// ---- SlackAPI implementation (reads) ----

func (s *sessionClient) AuthTest() (*slack.AuthTestResponse, error) {
	ctx, cancel := bg()
	defer cancel()
	return s.AuthTestContext(ctx)
}

func (s *sessionClient) AuthTestContext(ctx context.Context) (r *slack.AuthTestResponse, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.AuthTestContext(ctx); return })
	return
}

func (s *sessionClient) GetUsersContext(ctx context.Context, o ...slack.GetUsersOption) (r []slack.User, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.GetUsersContext(ctx, o...); return })
	return
}

func (s *sessionClient) GetUsersInfo(users ...string) (r *[]slack.User, err error) {
	ctx, cancel := bg()
	defer cancel()
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.GetUsersInfo(users...); return })
	return
}

func (s *sessionClient) GetConversationHistoryContext(ctx context.Context, p *slack.GetConversationHistoryParameters) (r *slack.GetConversationHistoryResponse, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.GetConversationHistoryContext(ctx, p); return })
	return
}

func (s *sessionClient) GetConversationRepliesContext(ctx context.Context, p *slack.GetConversationRepliesParameters) (m []slack.Message, more bool, cur string, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { m, more, cur, e = c.GetConversationRepliesContext(ctx, p); return })
	return
}

func (s *sessionClient) SearchContext(ctx context.Context, q string, p slack.SearchParameters) (m *slack.SearchMessages, f *slack.SearchFiles, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { m, f, e = c.SearchContext(ctx, q, p); return })
	return
}

func (s *sessionClient) GetFileInfoContext(ctx context.Context, id string, count, page int) (f *slack.File, cm []slack.Comment, pg *slack.Paging, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { f, cm, pg, e = c.GetFileInfoContext(ctx, id, count, page); return })
	return
}

func (s *sessionClient) GetFileContext(ctx context.Context, url string, w io.Writer) error {
	return s.do(ctx, false, func(c *MCPSlackClient) error { return c.GetFileContext(ctx, url, w) })
}

func (s *sessionClient) GetConversationInfoContext(ctx context.Context, in *slack.GetConversationInfoInput) (r *slack.Channel, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.GetConversationInfoContext(ctx, in); return })
	return
}

func (s *sessionClient) GetConversationsContext(ctx context.Context, p *slack.GetConversationsParameters) (r []slack.Channel, cur string, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, cur, e = c.GetConversationsContext(ctx, p); return })
	return
}

func (s *sessionClient) GetConversationsForUserContext(ctx context.Context, p *slack.GetConversationsForUserParameters) (r []slack.Channel, cur string, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, cur, e = c.GetConversationsForUserContext(ctx, p); return })
	return
}

func (s *sessionClient) ClientUserBoot(ctx context.Context) (r *edge.ClientUserBootResponse, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.ClientUserBoot(ctx); return })
	return
}

func (s *sessionClient) UsersSearch(ctx context.Context, q string, n int) (r []slack.User, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.UsersSearch(ctx, q, n); return })
	return
}

func (s *sessionClient) ClientCounts(ctx context.Context) (r edge.ClientCountsResponse, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.ClientCounts(ctx); return })
	return
}

func (s *sessionClient) GetMutedChannels(ctx context.Context) (r map[string]bool, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.GetMutedChannels(ctx); return })
	return
}

func (s *sessionClient) SavedList(ctx context.Context, filter string, limit int, cursor string) (r edge.SavedListResponse, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.SavedList(ctx, filter, limit, cursor); return })
	return
}

func (s *sessionClient) GetUserGroupsContext(ctx context.Context, o ...slack.GetUserGroupsOption) (r []slack.UserGroup, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.GetUserGroupsContext(ctx, o...); return })
	return
}

func (s *sessionClient) GetUserGroupMembersContext(ctx context.Context, g string, o ...slack.GetUserGroupMembersOption) (r []string, err error) {
	err = s.do(ctx, false, func(c *MCPSlackClient) (e error) { r, e = c.GetUserGroupMembersContext(ctx, g, o...); return })
	return
}
