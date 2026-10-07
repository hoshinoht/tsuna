package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func tgt(rel string, before, after *string) storage.Target {
	t := storage.Target{Rel: rel, Mode: 0o600}
	if before != nil {
		t.Before, t.BeforeExists = []byte(*before), true
	}
	if after != nil {
		t.After, t.AfterExists = []byte(*after), true
	}
	return t
}

func sp(s string) *string { return &s }

func readRel(t *testing.T, root, rel string) *string {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	s := string(data)
	return &s
}

// Forged, foreign, duplicate, invented-link and third-state
// journals are rejected before authorization and the journal is kept.
func TestRecoveryRejectsForgedJournals(t *testing.T) {
	freezeClock(t)
	const id = "minimal"
	jrel := ".opencode/workplan/minimal.transaction.json"
	cases := []struct {
		name  string
		op    string
		wid   string
		build func(root string) []storage.Target
		want  string
	}{
		{"foreign plan json", "update", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/other.json", nil, sp("{}"))}
		}, "is not an artifact of workplan minimal"},
		{"arbitrary file", "update", id, func(root string) []storage.Target {
			return []storage.Target{tgt("README.md", nil, sp("x"))}
		}, "is not an artifact of workplan minimal"},
		{"escape", "update", id, func(root string) []storage.Target {
			return []storage.Target{tgt("../x.md", nil, sp("x"))}
		}, "not a canonical in-root path"},
		{"workplanId mismatch", "update", "other", func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/minimal.md", readRel(t, root, ".opencode/workplan/minimal.md"), sp("x"))}
		}, "does not match minimal"},
		{"duplicate target", "update", id, func(root string) []storage.Target {
			md := readRel(t, root, ".opencode/workplan/minimal.md")
			return []storage.Target{tgt(".opencode/workplan/minimal.md", md, sp("x")), tgt(".opencode/workplan/minimal.md", md, sp("y"))}
		}, "duplicate target"},
		{"operation grants no authority", "checkpoint", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/minimal.md", readRel(t, root, ".opencode/workplan/minimal.md"), sp("x"))}
		}, "operation checkpoint cannot change markdown"},
		{"unknown operation", "delete-everything", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/minimal.md", readRel(t, root, ".opencode/workplan/minimal.md"), sp("x"))}
		}, "unsupported operation"},
		{"invented markdown link", "update", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/invented.md", nil, sp("x"))}
		}, "is not the plan's linked planFile"},
		{"deleted markdown", "update", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/minimal.md", readRel(t, root, ".opencode/workplan/minimal.md"), nil)}
		}, "would be deleted"},
		{"archive overwrite", "compact:apply", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/archive/minimal/state-x.json", sp("{}"), sp(`{"archiveVersion":1,"workplanId":"minimal"}`))}
		}, "must be a new, complete file"},
		{"foreign archive", "compact:apply", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/archive/minimal/state-x.json", nil, sp(`{"archiveVersion":1,"workplanId":"other"}`))}
		}, "not this plan's complete archive"},
		{"move without new markdown", "update", id, func(root string) []storage.Target {
			js := readRel(t, root, ".opencode/workplan/minimal.json")
			moved := strings.Replace(*js, ".opencode/workplan/minimal.md", ".opencode/workplan/next.md", 1)
			return []storage.Target{tgt(".opencode/workplan/minimal.json", js, &moved)}
		}, "has no Markdown target"},
		{"move overwriting existing markdown", "update", id, func(root string) []storage.Target {
			js := readRel(t, root, ".opencode/workplan/minimal.json")
			moved := strings.Replace(*js, ".opencode/workplan/minimal.md", ".opencode/workplan/next.md", 1)
			return []storage.Target{tgt(".opencode/workplan/minimal.json", js, &moved), tgt(".opencode/workplan/next.md", sp("old"), sp("new"))}
		}, "must be new (absent before)"},
		{"move keeping old markdown as target", "update", id, func(root string) []storage.Target {
			js := readRel(t, root, ".opencode/workplan/minimal.json")
			moved := strings.Replace(*js, ".opencode/workplan/minimal.md", ".opencode/workplan/next.md", 1)
			md := readRel(t, root, ".opencode/workplan/minimal.md")
			return []storage.Target{tgt(".opencode/workplan/minimal.json", js, &moved), tgt(".opencode/workplan/next.md", nil, sp("new")), tgt(".opencode/workplan/minimal.md", md, sp("changed"))}
		}, "keeps its old Markdown"},
		{"third state", "update", id, func(root string) []storage.Target {
			return []storage.Target{tgt(".opencode/workplan/minimal.md", sp("not the current bytes"), sp("also not"))}
		}, "External edit at .opencode/workplan/minimal.md"},
	}
	for _, c := range cases {
		for _, mode := range []string{"resume", "rollback"} {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				root := testutil.NewRoot(t, "minimal-valid")
				in := &storage.Intent{Operation: c.op, WorkplanID: c.wid, TransactionID: "forged-tx", CreatedAt: "2026-01-02T03:04:05.000Z", Targets: c.build(root.Path)}
				if err := os.WriteFile(filepath.Join(root.Path, jrel), storage.EncodeJournal(in), 0o600); err != nil {
					t.Fatal(err)
				}
				before := testutil.Fingerprint(t, root.Path)
				e, _ := New(root.Path)
				auth := &countingAuth{}
				_, err := runMutation(context.Background(), e, "workplan_update", mustJSON(t, `{"id":"minimal","recovery":"`+mode+`"}`), auth)
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("got %v, want %q", err, c.want)
				}
				if !strings.Contains(err.Error(), "journal evidence preserved") {
					t.Fatalf("error does not state that evidence is preserved: %v", err)
				}
				if auth.n != 0 {
					t.Fatal("authorization requested for a rejected journal")
				}
				if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
					t.Fatalf("rejection changed files: %v", d)
				}
			})
		}
	}
}

// Recovery binds to the exact journal: expectedHash must be current.
func TestRecoveryStaleHash(t *testing.T) {
	root := testutil.NewRoot(t, "pending-journal")
	e, _ := New(root.Path)
	auth := &countingAuth{}
	_, err := runMutation(context.Background(), e, "workplan_update", mustJSON(t, `{"id":"tx-plan","recovery":"resume","expectedHash":"`+strings.Repeat("a", 64)+`"}`), auth)
	if err == nil || !strings.HasPrefix(err.Error(), "Stale expectedHash") || auth.n != 0 {
		t.Fatalf("got %v (%d)", err, auth.n)
	}
}
