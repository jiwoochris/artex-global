package agent

import (
	"context"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func writeTodos(t *testing.T, s *actool.TodoStore, input string) {
	t.Helper()
	if _, err := s.Tool().Call(context.Background(), []byte(input), nil); err != nil {
		t.Fatalf("TodoWrite: %v", err)
	}
}

func TestUnchangedTodoGuard(t *testing.T) {
	const list = `{"todos":[{"content":"dispatch auth sweep","status":"pending"},{"content":"verify XSS","status":"completed"}]}`
	s := actool.NewTodoStore()
	g := unchangedTodoGuard{todos: s}
	ctx := context.Background()

	if block, _, _ := g.PreToolUse(ctx, "TodoWrite", []byte(list)); block {
		t.Fatal("first write of a new list was blocked")
	}
	writeTodos(t, s, list)

	block, msg, _ := g.PreToolUse(ctx, "TodoWrite", []byte(list))
	if !block || msg != unchangedTodoMessage {
		t.Fatalf("rewrite of the identical list: block=%v msg=%q, want blocked with the end-of-round message", block, msg)
	}

	changed := `{"todos":[{"content":"dispatch auth sweep","status":"in_progress"},{"content":"verify XSS","status":"completed"}]}`
	if block, _, _ := g.PreToolUse(ctx, "TodoWrite", []byte(changed)); block {
		t.Fatal("a status change was blocked")
	}

	if block, _, _ := g.PreToolUse(ctx, "add_intent", []byte(list)); block {
		t.Fatal("a tool other than TodoWrite was blocked")
	}
}
