package operations

import (
	"strings"
	"testing"
)

func TestResolveRoute(t *testing.T) {
	create, ok := OperationByID("createKey")
	if !ok {
		t.Fatal("createKey missing from catalog")
	}
	id, _, _, ok := ResolveRoute(create.Method, create.Path)
	if !ok || id != "createKey" {
		t.Fatalf("createKey resolved to %q %v", id, ok)
	}
	// Literal-versus-parameter precedence: every literal catalog path must
	// resolve to itself, never to a parameterised sibling.
	for _, op := range catalog {
		if strings.Contains(op.Path, "{") {
			continue
		}
		if got, _, _, ok := ResolveRoute(op.Method, op.Path); !ok || got != op.ID {
			t.Fatalf("%s %s resolved to %q", op.Method, op.Path, got)
		}
	}
	for _, p := range []string{"/no-such-route", "/applications/../x", "/applications/a%2f", strings.Repeat("/a", 65), ""} {
		if _, _, _, ok := ResolveRoute("GET", p); ok {
			t.Fatalf("%q resolved", p)
		}
	}
	// An unescaped slash inside one segment never matches a parameter, and
	// segment bounds still hold.
	if _, _, _, ok := ResolveSegments("GET", []string{"applications", "a/b"}); ok {
		t.Fatal("segment with a slash resolved")
	}
	if _, _, _, ok := ResolveSegments("GET", []string{strings.Repeat("a", 4097)}); ok {
		t.Fatal("over-long path resolved")
	}
	if id, _, _, ok := ResolveSegments(create.Method, strings.Split(strings.TrimPrefix(create.Path, "/"), "/")); !ok || id != "createKey" {
		t.Fatalf("segments resolved to %q", id)
	}
	if _, ok := OperationByID("nope"); ok {
		t.Fatal("unknown id found")
	}
}
