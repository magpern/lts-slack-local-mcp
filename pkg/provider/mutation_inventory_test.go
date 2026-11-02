package provider

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"
)

// Every SlackAPI method must be explicitly classified as mutating or read-only.
// A new method fails this test until someone decides, so it can never silently
// default to "read" and become eligible for automatic replay after re-login.
var mutatingMethods = map[string]bool{
	"PostMessageContext":            true,
	"MarkConversationContext":       true,
	"AddReactionContext":            true,
	"RemoveReactionContext":         true,
	"LeaveConversationContext":      true,
	"JoinConversationContext":       true,
	"SavedUpdate":                   true,
	"SavedClearCompleted":           true,
	"CreateUserGroupContext":        true,
	"UpdateUserGroupContext":        true,
	"UpdateUserGroupMembersContext": true,
}

var readMethods = map[string]bool{
	"AuthTest": true, "AuthTestContext": true, "GetUsersContext": true, "GetUsersInfo": true,
	"GetConversationHistoryContext": true, "GetConversationRepliesContext": true, "SearchContext": true,
	"GetFileInfoContext": true, "GetFileContext": true, "GetConversationInfoContext": true,
	"GetConversationsContext": true, "GetConversationsForUserContext": true,
	"ClientUserBoot": true, "UsersSearch": true, "ClientCounts": true, "GetMutedChannels": true,
	"SavedList": true, "GetUserGroupsContext": true, "GetUserGroupMembersContext": true,
}

func TestUnitEverySlackAPIMethodIsClassified(t *testing.T) {
	typ := reflect.TypeOf((*SlackAPI)(nil)).Elem()
	var unclassified []string
	for i := 0; i < typ.NumMethod(); i++ {
		n := typ.Method(i).Name
		if mutatingMethods[n] == readMethods[n] { // neither, or (wrongly) both
			unclassified = append(unclassified, n)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("classify these SlackAPI methods as mutating or read-only: %v", unclassified)
	}
	for n := range mutatingMethods {
		if _, ok := typ.MethodByName(n); !ok {
			t.Errorf("mutatingMethods lists %s, which is not a SlackAPI method", n)
		}
	}
}

// Every sessionClient method must pass mutation=true to do() exactly when it is
// classified as mutating. Checked on the source so a wrong flag cannot hide.
func TestUnitSessionClientPassesMutationFlag(t *testing.T) {
	fset := token.NewFileSet()
	got := map[string]bool{} // method -> mutation flag literal
	for _, file := range []string{"session_client.go", "session_mutations.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if !mutatingMethods[name] && !readMethods[name] {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := ce.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "do" || len(ce.Args) < 3 {
					return true
				}
				if id, ok := ce.Args[1].(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
					got[name] = id.Name == "true"
				}
				return true
			})
		}
	}
	for n := range mutatingMethods {
		if v, ok := got[n]; !ok || !v {
			t.Errorf("%s is mutating but is not routed through do(..., true, ...) (found=%v flag=%v)", n, ok, v)
		}
	}
	for n := range readMethods {
		if n == "AuthTest" { // delegates to AuthTestContext
			continue
		}
		if v, ok := got[n]; !ok || v {
			t.Errorf("%s is read-only but is not routed through do(..., false, ...) (found=%v flag=%v)", n, ok, v)
		}
	}
}
