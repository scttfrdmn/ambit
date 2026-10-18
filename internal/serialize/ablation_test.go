package serialize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithoutFailClosed(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "corpus", "policies", "ambit.md"))
	if err != nil {
		t.Fatal(err)
	}
	prose := string(b)
	out, err := WithoutFailClosed(prose)
	if err != nil {
		t.Fatal(err)
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if strings.Contains(norm(out), norm(FailClosed)) || strings.Contains(out, "cannot determine") {
		t.Fatal("sentence still present")
	}
	if !strings.Contains(out, "Any other tool is not covered by this policy and is denied.\n\nA payload is") {
		t.Fatalf("paragraph not well-formed:\n%s", out[:600])
	}
	// Only the sentence and the space before it are gone.
	if got, want := len(prose)-len(out), len(FailClosed)+1; got != want {
		t.Fatalf("removed %d bytes, want %d", got, want)
	}
	if norm(out) != strings.Replace(norm(prose), " "+FailClosed, "", 1) {
		t.Fatal("text other than the sentence changed")
	}
	if _, err := WithoutFailClosed(out); err == nil {
		t.Fatal("want error when the sentence is absent")
	}
	// Wrapped differently, the sentence is still found.
	if s, err := WithoutFailClosed("A.\n" + strings.ReplaceAll(FailClosed, "the unsafe ", "the\n  unsafe ") + " B."); err != nil || s != "A. B." {
		t.Fatalf("got %q, %v", s, err)
	}
}
