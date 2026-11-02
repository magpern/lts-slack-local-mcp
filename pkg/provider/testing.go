package provider

import (
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// NewWithClient returns a ready provider around a caller-supplied client, with
// empty caches and no session lifecycle. It exists so other packages' tests can
// drive handlers against a fake Slack API; production code uses NewLocal.
func NewWithClient(client SlackAPI, logger *zap.Logger) *ApiProvider {
	ap := &ApiProvider{transport: "stdio", client: client, logger: logger}
	ap.usersSnapshot.Store(&UsersCache{Users: map[string]slack.User{}, UsersInv: map[string]string{}})
	ap.channelsSnapshot.Store(&ChannelsCache{Channels: map[string]Channel{}, ChannelsInv: map[string]string{}})
	ap.usersReady.Store(true)
	ap.channelsReady.Store(true)
	return ap
}
