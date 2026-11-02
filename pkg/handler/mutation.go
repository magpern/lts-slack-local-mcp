package handler

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
)

// Mutation authorization.
//
// A tool that changes workspace state (or exports data to disk) executes only
// when BOTH hold:
//
//	(a) its dedicated environment opt-in is set, and
//	(b) the tool is permitted under any configured tool allow-list.
//
// Selecting a tool through the allow-list never authorizes it on its own, and
// the opt-in never overrides an allow-list that excludes the tool. The check
// runs in the handler, at the top, before any Slack call, so it does not depend
// on how or whether the tool was registered. With no policy installed every
// guarded tool is denied (fail closed).

// Tool names of the guarded tools (kept here because the server package imports
// this one).
const (
	toolAddMessage         = "conversations_add_message"
	toolReactionsAdd       = "reactions_add"
	toolReactionsRemove    = "reactions_remove"
	toolAttachmentGetData  = "attachment_get_data"
	toolConversationsMark  = "conversations_mark"
	toolConversationsLeave = "conversations_leave"
	toolConversationsJoin  = "conversations_join"
	toolUsergroupsCreate   = "usergroups_create"
	toolUsergroupsUpdate   = "usergroups_update"
	toolUsergroupsUsersUpd = "usergroups_users_update"
	toolUsergroupsMe       = "usergroups_me" // write actions only (join, leave)
	toolSavedUpdate        = "saved_update"
	toolSavedClear         = "saved_clear_completed"
)

type guard struct {
	env         string // dedicated opt-in variable
	channelList bool   // value may be a channel allow/deny list rather than a plain flag
}

var guards = map[string]guard{
	toolAddMessage:         {"SLACK_MCP_ADD_MESSAGE_TOOL", true},
	toolReactionsAdd:       {"SLACK_MCP_REACTION_TOOL", true},
	toolReactionsRemove:    {"SLACK_MCP_REACTION_TOOL", true},
	toolAttachmentGetData:  {"SLACK_MCP_ATTACHMENT_TOOL", false},
	toolConversationsMark:  {"SLACK_MCP_MARK_TOOL", false},
	toolConversationsLeave: {"SLACK_MCP_LEAVE_TOOL", false},
	toolConversationsJoin:  {"SLACK_MCP_JOIN_TOOL", false},
	toolUsergroupsCreate:   {"SLACK_MCP_USERGROUPS_WRITE_TOOL", false},
	toolUsergroupsUpdate:   {"SLACK_MCP_USERGROUPS_WRITE_TOOL", false},
	toolUsergroupsUsersUpd: {"SLACK_MCP_USERGROUPS_WRITE_TOOL", false},
	toolUsergroupsMe:       {"SLACK_MCP_USERGROUPS_WRITE_TOOL", false},
	toolSavedUpdate:        {"SLACK_MCP_SAVED_WRITE_TOOL", false},
	toolSavedClear:         {"SLACK_MCP_SAVED_WRITE_TOOL", false},
}

// Policy is the authorization input shared by all handlers.
type Policy struct {
	// EnabledTools is the tool allow-list; empty means no allow-list restriction.
	EnabledTools []string
	// Getenv reads the opt-in variables; nil means os.Getenv.
	Getenv func(string) string
}

var policy atomic.Pointer[Policy]

// SetPolicy installs the policy. Until it is called every guarded tool is denied.
func SetPolicy(p Policy) {
	cp := p
	cp.EnabledTools = slices.Clone(p.EnabledTools)
	policy.Store(&cp)
}

// OptInEnvVar returns the dedicated opt-in variable for a guarded tool. The
// server uses it to decide whether to list the tool when no allow-list is set.
func OptInEnvVar(tool string) (string, bool) {
	g, ok := guards[tool]
	return g.env, ok
}

func optedIn(v string, channelList bool) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "", "0", "false", "no", "off":
		return false
	}
	if channelList {
		return true // "true", "1", or a channel allow/deny list
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// authorizeMutation returns the opt-in value (a channel policy for the tools that
// take one) when the tool may run, or an error that says why not.
func authorizeMutation(tool string) (string, error) {
	g, ok := guards[tool]
	if !ok {
		return "", fmt.Errorf("internal error: %q is not a guarded tool; refusing", tool)
	}
	p := policy.Load()
	if p == nil {
		return "", fmt.Errorf("the %s tool is disabled: no authorization policy is installed", tool)
	}
	if len(p.EnabledTools) > 0 && !slices.Contains(p.EnabledTools, tool) {
		return "", fmt.Errorf("the %s tool is not in the enabled tools list", tool)
	}
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	v := strings.TrimSpace(getenv(g.env))
	if !optedIn(v, g.channelList) {
		return "", fmt.Errorf(
			"the %s tool is disabled by default. Selecting it is not enough: set the %s environment variable to true to allow it",
			tool, g.env)
	}
	return v, nil
}
