package provider

import (
	"context"

	"github.com/slack-go/slack"
)

// Every SlackAPI method that changes workspace state lives here and is passed
// mutation=true to sessionClient.do, which guarantees it is never replayed after
// an authentication failure. Authorization to call these at all is enforced in
// the handlers (see pkg/handler/mutation.go); this file only guarantees
// at-most-once delivery.

func (s *sessionClient) PostMessageContext(ctx context.Context, channel string, o ...slack.MsgOption) (ch, ts string, err error) {
	err = s.do(ctx, true, func(c *MCPSlackClient) (e error) { ch, ts, e = c.PostMessageContext(ctx, channel, o...); return })
	return
}

func (s *sessionClient) MarkConversationContext(ctx context.Context, channel, ts string) error {
	return s.do(ctx, true, func(c *MCPSlackClient) error { return c.MarkConversationContext(ctx, channel, ts) })
}

func (s *sessionClient) AddReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return s.do(ctx, true, func(c *MCPSlackClient) error { return c.AddReactionContext(ctx, name, item) })
}

func (s *sessionClient) RemoveReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return s.do(ctx, true, func(c *MCPSlackClient) error { return c.RemoveReactionContext(ctx, name, item) })
}

func (s *sessionClient) LeaveConversationContext(ctx context.Context, channelID string) (r bool, err error) {
	err = s.do(ctx, true, func(c *MCPSlackClient) (e error) { r, e = c.LeaveConversationContext(ctx, channelID); return })
	return
}

func (s *sessionClient) JoinConversationContext(ctx context.Context, channelID string) (ch *slack.Channel, warn string, resp []string, err error) {
	err = s.do(ctx, true, func(c *MCPSlackClient) (e error) {
		ch, warn, resp, e = c.JoinConversationContext(ctx, channelID)
		return
	})
	return
}

func (s *sessionClient) SavedUpdate(ctx context.Context, itemType, itemID, ts, mark string, dateDue int64) error {
	return s.do(ctx, true, func(c *MCPSlackClient) error { return c.SavedUpdate(ctx, itemType, itemID, ts, mark, dateDue) })
}

func (s *sessionClient) SavedClearCompleted(ctx context.Context) error {
	return s.do(ctx, true, func(c *MCPSlackClient) error { return c.SavedClearCompleted(ctx) })
}

func (s *sessionClient) CreateUserGroupContext(ctx context.Context, g slack.UserGroup, o ...slack.CreateUserGroupOption) (r slack.UserGroup, err error) {
	err = s.do(ctx, true, func(c *MCPSlackClient) (e error) { r, e = c.CreateUserGroupContext(ctx, g, o...); return })
	return
}

func (s *sessionClient) UpdateUserGroupContext(ctx context.Context, id string, o ...slack.UpdateUserGroupsOption) (r slack.UserGroup, err error) {
	err = s.do(ctx, true, func(c *MCPSlackClient) (e error) { r, e = c.UpdateUserGroupContext(ctx, id, o...); return })
	return
}

func (s *sessionClient) UpdateUserGroupMembersContext(ctx context.Context, g, members string, o ...slack.UpdateUserGroupMembersOption) (r slack.UserGroup, err error) {
	err = s.do(ctx, true, func(c *MCPSlackClient) (e error) {
		r, e = c.UpdateUserGroupMembersContext(ctx, g, members, o...)
		return
	})
	return
}
