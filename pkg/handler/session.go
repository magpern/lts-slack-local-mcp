package handler

import (
	"context"
	"encoding/json"

	"github.com/magpern/lts-slack-local-mcp/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// SessionHandler implements the login and logout tools. They work while the
// server is signed out, which is the only state the server can start in.
type SessionHandler struct {
	provider *provider.ApiProvider
	logger   *zap.Logger
}

func NewSessionHandler(p *provider.ApiProvider, logger *zap.Logger) *SessionHandler {
	return &SessionHandler{provider: p, logger: logger}
}

// LoginHandler signs in through the dedicated browser profile. It is usually
// silent after the first time; the first sign-in opens a browser window.
func (h *SessionHandler) LoginHandler(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := h.provider.Login(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	body, _ := json.Marshal(map[string]any{
		"signed_in": true,
		"workspace": id.Team,
		"user":      id.User,
		"team_id":   id.TeamID,
		"user_id":   id.UserID,
	})
	return mcp.NewToolResultText(string(body)), nil
}

// LogoutHandler forgets the session: it clears the in-memory credential, deletes
// the dedicated browser profile and this account's cached Slack data, and keeps
// the server signed out (also across restarts) until login is called. It does
// not sign you out of Slack in other browsers or revoke the session at Slack.
func (h *SessionHandler) LogoutHandler(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	deleted, err := h.provider.Logout(ctx)
	body, _ := json.Marshal(map[string]any{
		"signed_out":      true,
		"profile_deleted": deleted,
	})
	if err != nil {
		// Signed out regardless; the residual profile will not be reused.
		return mcp.NewToolResultText(string(body) + "\nWarning: the browser profile could not be fully removed (" +
			err.Error() + "). The server stays signed out and will not reuse it; close any browser using it and call login to clean up."), nil
	}
	return mcp.NewToolResultText(string(body)), nil
}
