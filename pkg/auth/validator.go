package auth

import (
	"context"
	"fmt"

	"github.com/magpern/lts-slack-local-mcp/pkg/transport"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// SlackValidator validates a credential by calling Slack auth.test with the
// token and the d cookie. It fails closed.
type SlackValidator struct {
	log *zap.Logger
}

// NewSlackValidator returns a network-backed Validator.
func NewSlackValidator(log *zap.Logger) *SlackValidator {
	if log == nil {
		log = zap.NewNop()
	}
	return &SlackValidator{log: log}
}

// Identify runs auth.test for the credential and returns its identity.
func (v *SlackValidator) Identify(ctx context.Context, c Credential) (*Identity, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("empty token")
	}
	httpClient := transport.ProvideHTTPClient(c.Cookies(), c.UserAgent, v.log)
	client := slack.New(c.Token, slack.OptionHTTPClient(httpClient))
	resp, err := client.AuthTestContext(ctx)
	if err != nil {
		return nil, err
	}
	if resp.TeamID == "" || resp.UserID == "" {
		return nil, fmt.Errorf("auth.test returned no identity")
	}
	return &Identity{
		TeamID:       resp.TeamID,
		UserID:       resp.UserID,
		Team:         resp.Team,
		User:         resp.User,
		EnterpriseID: resp.EnterpriseID,
	}, nil
}
