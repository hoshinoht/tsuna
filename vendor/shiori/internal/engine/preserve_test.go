package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/testutil"
)

// Unknown members keep their values, nesting and exact number
// spelling across an unrelated write; known members keep schema order and
// the absent legacy specFiles key stays absent.
func TestUnknownMetadataSurvivesUpdate(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "legacy-fields")
	p := filepath.Join(root.Path, ".opencode/workplan/legacy-plan.json")
	data, _ := os.ReadFile(p)
	fixed := strings.Replace(string(data), "  \"kind\": \"ignored-duplicate\",\n", "", 1)
	if err := os.WriteFile(p, []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _ := New(root.Path)
	if _, err := runMutation(context.Background(), e, "workplan_update", mustJSON(t, `{"id":"legacy-plan","appendNotes":["touch"]}`), allowAll{}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	for _, want := range []string{`"bigInt": 12345678901234567890`, `"float": 1.0`, `"exp": 1e+21`, `"estimate": 1.5`, `"custom": null`, `"legacyRef": 7`, `"tags": [`} {
		if !strings.Contains(string(after), want) {
			t.Fatalf("lost %s:\n%s", want, after)
		}
	}
	if strings.Contains(string(after), `"specFiles"`) {
		t.Fatal("legacy specFiles key was added by an unrelated write")
	}
	if !strings.Contains(string(after), "\"touch\"") {
		t.Fatal("update not applied")
	}
}

// New links containing a backslash are refused with a field path.
func TestBackslashLinksRefused(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e, _ := New(root.Path)
	for _, in := range []string{
		`{"id":"n","goal":"g","specFiles":["docs\\a.md"]}`,
		`{"id":"n","goal":"g","planFile":".opencode/workplan/a\\b.md"}`,
	} {
		_, err := runMutation(context.Background(), e, "workplan_create", mustJSON(t, in), allowAll{})
		if err == nil || !strings.Contains(err.Error(), "Linked path contains a backslash") {
			t.Fatalf("%s: %v", in, err)
		}
	}
	_, err := runMutation(context.Background(), e, "workplan_update", mustJSON(t, `{"id":"minimal","addSpecFiles":["x\\y.md"]}`), allowAll{})
	if err == nil || !strings.HasPrefix(err.Error(), "addSpecFiles.0: Linked path contains a backslash") {
		t.Fatalf("update: %v", err)
	}
}

// Writers never follow or replace symlinks.
func TestSymlinkTargetsRefused(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root.Path, ".opencode/workplan/link")); err != nil {
		t.Fatal(err)
	}
	e, _ := New(root.Path)
	_, err := runMutation(context.Background(), e, "workplan_update", mustJSON(t, `{"id":"minimal","planFile":".opencode/workplan/link/x.md"}`), allowAll{})
	if err == nil {
		t.Fatal("move through a symlinked directory was accepted")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("wrote outside the root")
	}
}
