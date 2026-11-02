package handler

import (
	"context"
	"sync"
	"testing"

	"github.com/magpern/lts-slack-local-mcp/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// recordingSlack is a SlackAPI whose only implemented methods are the mutating
// ones (which record the call) and the few reads a permitted flow needs. Any
// other method is the nil embedded interface and panics: a Slack call made
// BEFORE authorization would therefore fail the test loudly.
type recordingSlack struct {
	provider.SlackAPI
	mu       sync.Mutex
	mutating []string
}

func (r *recordingSlack) rec(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutating = append(r.mutating, name)
}

func (r *recordingSlack) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.mutating...)
}

func (r *recordingSlack) PostMessageContext(context.Context, string, ...slack.MsgOption) (string, string, error) {
	r.rec("PostMessage")
	return "C1", "1.1", nil
}
func (r *recordingSlack) MarkConversationContext(context.Context, string, string) error {
	r.rec("MarkConversation")
	return nil
}
func (r *recordingSlack) AddReactionContext(context.Context, string, slack.ItemRef) error {
	r.rec("AddReaction")
	return nil
}
func (r *recordingSlack) RemoveReactionContext(context.Context, string, slack.ItemRef) error {
	r.rec("RemoveReaction")
	return nil
}
func (r *recordingSlack) LeaveConversationContext(context.Context, string) (bool, error) {
	r.rec("LeaveConversation")
	return false, nil
}
func (r *recordingSlack) JoinConversationContext(context.Context, string) (*slack.Channel, string, []string, error) {
	r.rec("JoinConversation")
	return &slack.Channel{}, "", nil, nil
}
func (r *recordingSlack) SavedUpdate(context.Context, string, string, string, string, int64) error {
	r.rec("SavedUpdate")
	return nil
}
func (r *recordingSlack) SavedClearCompleted(context.Context) error {
	r.rec("SavedClearCompleted")
	return nil
}
func (r *recordingSlack) CreateUserGroupContext(context.Context, slack.UserGroup, ...slack.CreateUserGroupOption) (slack.UserGroup, error) {
	r.rec("CreateUserGroup")
	return slack.UserGroup{}, nil
}
func (r *recordingSlack) UpdateUserGroupContext(context.Context, string, ...slack.UpdateUserGroupsOption) (slack.UserGroup, error) {
	r.rec("UpdateUserGroup")
	return slack.UserGroup{}, nil
}
func (r *recordingSlack) UpdateUserGroupMembersContext(context.Context, string, string, ...slack.UpdateUserGroupMembersOption) (slack.UserGroup, error) {
	r.rec("UpdateUserGroupMembers")
	return slack.UserGroup{}, nil
}

// Reads that a *permitted* flow may need.
func (r *recordingSlack) AuthTest() (*slack.AuthTestResponse, error) {
	return &slack.AuthTestResponse{UserID: "U1", User: "u", TeamID: "T1"}, nil
}
func (r *recordingSlack) AuthTestContext(context.Context) (*slack.AuthTestResponse, error) {
	return r.AuthTest()
}
func (r *recordingSlack) GetUserGroupMembersContext(context.Context, string, ...slack.GetUserGroupMembersOption) ([]string, error) {
	return []string{"U2"}, nil
}
func (r *recordingSlack) GetUserGroupsContext(context.Context, ...slack.GetUserGroupsOption) ([]slack.UserGroup, error) {
	return nil, nil
}

type mutCase struct {
	name string // guarded tool name
	call func(h *testHandlers) error
}

type testHandlers struct {
	conv *ConversationsHandler
	ug   *UsergroupsHandler
	sv   *SavedHandler
}

func req(args map[string]any) mcp.CallToolRequest {
	r := mcp.CallToolRequest{}
	r.Params.Arguments = args
	return r
}

func newTestHandlers(rec *recordingSlack) *testHandlers {
	p := provider.NewWithClient(rec, zap.NewNop())
	conv := NewConversationsHandler(p, zap.NewNop())
	return &testHandlers{conv: conv, ug: NewUsergroupsHandler(p, zap.NewNop()), sv: NewSavedHandler(p, zap.NewNop(), conv)}
}

// One case per entry in the mutation inventory. Arguments are valid enough that
// a permitted call would reach Slack, so a denial is attributable to the gate.
var inventory = []mutCase{
	{toolAddMessage, func(h *testHandlers) error {
		_, err := h.conv.ConversationsAddMessageHandler(context.Background(), req(map[string]any{"channel_id": "C1", "payload": "hi"}))
		return err
	}},
	{toolReactionsAdd, func(h *testHandlers) error {
		_, err := h.conv.ReactionsAddHandler(context.Background(), req(map[string]any{"channel_id": "C1", "timestamp": "1.1", "emoji": "tada"}))
		return err
	}},
	{toolReactionsRemove, func(h *testHandlers) error {
		_, err := h.conv.ReactionsRemoveHandler(context.Background(), req(map[string]any{"channel_id": "C1", "timestamp": "1.1", "emoji": "tada"}))
		return err
	}},
	{toolConversationsMark, func(h *testHandlers) error {
		_, err := h.conv.ConversationsMarkHandler(context.Background(), req(map[string]any{"channel_id": "C1", "ts": "1.1"}))
		return err
	}},
	{toolConversationsJoin, func(h *testHandlers) error {
		_, err := h.conv.ConversationsJoinHandler(context.Background(), req(map[string]any{"channel_id": "C1"}))
		return err
	}},
	{toolConversationsLeave, func(h *testHandlers) error {
		_, err := h.conv.ConversationsLeaveHandler(context.Background(), req(map[string]any{"channel_id": "C1"}))
		return err
	}},
	{toolUsergroupsCreate, func(h *testHandlers) error {
		_, err := h.ug.UsergroupsCreateHandler(context.Background(), req(map[string]any{"name": "g", "handle": "g"}))
		return err
	}},
	{toolUsergroupsUpdate, func(h *testHandlers) error {
		_, err := h.ug.UsergroupsUpdateHandler(context.Background(), req(map[string]any{"usergroup_id": "S1", "name": "n"}))
		return err
	}},
	{toolUsergroupsUsersUpd, func(h *testHandlers) error {
		_, err := h.ug.UsergroupsUsersUpdateHandler(context.Background(), req(map[string]any{"usergroup_id": "S1", "users": "U1,U2"}))
		return err
	}},
	{toolUsergroupsMe, func(h *testHandlers) error {
		_, err := h.ug.UsergroupsMeHandler(context.Background(), req(map[string]any{"action": "join", "usergroup_id": "S1"}))
		return err
	}},
	{toolSavedUpdate, func(h *testHandlers) error {
		_, err := h.sv.SavedUpdateHandler(context.Background(), req(map[string]any{"item_id": "C1", "ts": "1.1", "mark": "completed"}))
		return err
	}},
	{toolSavedClear, func(h *testHandlers) error {
		_, err := h.sv.SavedClearCompletedHandler(context.Background(), req(nil))
		return err
	}},
}

func envFrom(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Acceptance 1: with the default configuration, the whole inventory makes zero
// mutating Slack calls and every tool reports it is disabled.
func TestUnitDefaultConfigMakesZeroMutatingCalls(t *testing.T) {
	SetPolicy(Policy{Getenv: envFrom(nil)})
	rec := &recordingSlack{}
	h := newTestHandlers(rec)
	for _, c := range inventory {
		if err := c.call(h); err == nil {
			t.Errorf("%s: expected a denial under the default configuration", c.name)
		}
	}
	if got := rec.calls(); len(got) != 0 {
		t.Fatalf("default configuration made mutating calls: %v", got)
	}
}

// Acceptance 2: selecting a write tool through the allow-list is not enough.
func TestUnitAllowListSelectionWithoutOptInIsDenied(t *testing.T) {
	var all []string
	for _, c := range inventory {
		all = append(all, c.name)
	}
	SetPolicy(Policy{EnabledTools: all, Getenv: envFrom(nil)}) // every tool listed, no opt-in
	rec := &recordingSlack{}
	h := newTestHandlers(rec)
	for _, c := range inventory {
		if err := c.call(h); err == nil {
			t.Errorf("%s: listed in the allow-list but without its opt-in must be denied", c.name)
		}
	}
	if got := rec.calls(); len(got) != 0 {
		t.Fatalf("allow-list selection alone made mutating calls: %v", got)
	}
}

// The opt-in must not override an allow-list that excludes the tool.
func TestUnitOptInDoesNotOverrideAllowList(t *testing.T) {
	env := map[string]string{
		"SLACK_MCP_ADD_MESSAGE_TOOL": "true", "SLACK_MCP_REACTION_TOOL": "true", "SLACK_MCP_MARK_TOOL": "true",
		"SLACK_MCP_JOIN_TOOL": "true", "SLACK_MCP_LEAVE_TOOL": "true",
		"SLACK_MCP_USERGROUPS_WRITE_TOOL": "true", "SLACK_MCP_SAVED_WRITE_TOOL": "true",
	}
	SetPolicy(Policy{EnabledTools: []string{"channels_list"}, Getenv: envFrom(env)}) // excludes every write tool
	rec := &recordingSlack{}
	h := newTestHandlers(rec)
	for _, c := range inventory {
		if err := c.call(h); err == nil {
			t.Errorf("%s: opt-in set but the tool is not in the allow-list; must be denied", c.name)
		}
	}
	if got := rec.calls(); len(got) != 0 {
		t.Fatalf("opt-in overrode the allow-list: %v", got)
	}
}

// Positive control: both conditions satisfied lets each tool reach Slack, so the
// gate is not simply "always deny".
func TestUnitOptInAndPermittedAllowsEachTool(t *testing.T) {
	env := map[string]string{
		"SLACK_MCP_ADD_MESSAGE_TOOL": "true", "SLACK_MCP_REACTION_TOOL": "true", "SLACK_MCP_MARK_TOOL": "true",
		"SLACK_MCP_JOIN_TOOL": "true", "SLACK_MCP_LEAVE_TOOL": "true",
		"SLACK_MCP_USERGROUPS_WRITE_TOOL": "true", "SLACK_MCP_SAVED_WRITE_TOOL": "true",
	}
	SetPolicy(Policy{Getenv: envFrom(env)}) // no allow-list
	for _, c := range inventory {
		rec := &recordingSlack{}
		h := newTestHandlers(rec)
		err := c.call(h)
		if err != nil {
			t.Errorf("%s: should be allowed with opt-in and no allow-list, got %v", c.name, err)
			continue
		}
		if len(rec.calls()) == 0 {
			t.Errorf("%s: authorized but made no Slack call", c.name)
		}
	}
}

// usergroups_me is a mixed tool: the read action stays available by default.
func TestUnitUsergroupsMeListIsReadOnlyAndAllowed(t *testing.T) {
	SetPolicy(Policy{Getenv: envFrom(nil)})
	rec := &recordingSlack{}
	h := newTestHandlers(rec)
	if _, err := h.ug.UsergroupsMeHandler(context.Background(), req(map[string]any{"action": "list"})); err != nil {
		t.Fatalf("list must be allowed without any opt-in: %v", err)
	}
	if len(rec.calls()) != 0 {
		t.Fatalf("list made mutating calls: %v", rec.calls())
	}
	for _, a := range []string{"join", "leave"} {
		if _, err := h.ug.UsergroupsMeHandler(context.Background(), req(map[string]any{"action": a, "usergroup_id": "S1"})); err == nil {
			t.Errorf("action %q must be denied by default", a)
		}
	}
	if len(rec.calls()) != 0 {
		t.Fatalf("denied actions made mutating calls: %v", rec.calls())
	}
}

// Fail closed: no policy installed means every guarded tool is denied.
func TestUnitNoPolicyDeniesEverything(t *testing.T) {
	policy.Store(nil)
	t.Cleanup(func() { SetPolicy(Policy{Getenv: envFrom(nil)}) })
	rec := &recordingSlack{}
	h := newTestHandlers(rec)
	for _, c := range inventory {
		if err := c.call(h); err == nil {
			t.Errorf("%s: must be denied when no policy is installed", c.name)
		}
	}
	if len(rec.calls()) != 0 {
		t.Fatalf("calls made without a policy: %v", rec.calls())
	}
}

func TestUnitOptedIn(t *testing.T) {
	cases := []struct {
		v    string
		list bool
		want bool
	}{
		{"", true, false}, {"false", true, false}, {"0", true, false}, {"off", true, false},
		{"true", true, true}, {"1", true, true}, {"C123,D456", true, true}, {"!C123", true, true},
		{"", false, false}, {"false", false, false}, {"maybe", false, false}, {"C123", false, false},
		{"true", false, true}, {"1", false, true}, {"yes", false, true}, {"TRUE", false, true},
	}
	for _, c := range cases {
		if got := optedIn(c.v, c.list); got != c.want {
			t.Errorf("optedIn(%q, list=%v) = %v, want %v", c.v, c.list, got, c.want)
		}
	}
}
