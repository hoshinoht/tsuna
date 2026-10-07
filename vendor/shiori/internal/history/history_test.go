package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/storage"
)

// appendVia commits a no-op-sized intent whose only effect is the log
// append, the way the engine's writes reach it.
func appendVia(t *testing.T, root string, e Entry, rotateAt int64) error {
	t.Helper()
	rel := ".opencode/workplan/p" + Suffix
	target := ".opencode/workplan/" + e.Tx + ".txt"
	in := &storage.Intent{
		Operation: e.Op, WorkplanID: "p", Root: root, TransactionID: e.Tx, CreatedAt: e.At,
		JournalRel: ".opencode/workplan/p.transaction.json", JournalStage: ".opencode/workplan/.p.transaction." + e.Tx + ".stage",
		Targets: []storage.Target{{Rel: target, Kind: "plan", After: []byte("x"), AfterExists: true, Mode: 0o600, Stage: storage.StagePath(target, e.Tx, 0)}},
		Dirs:    []string{".opencode", ".opencode/workplan"},
		History: &storage.Append{Rel: rel, Payload: Payload(e), RotateAt: rotateAt},
	}
	if rotateAt > 0 {
		in.History.ArchiveRel = ".opencode/workplan/archive/p/history-" + e.Tx + ".jsonl"
	}
	in.Targets[0].Seal()
	res, err := storage.Commit(context.Background(), in, storage.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	return res.HistoryErr
}

func entry(tx, from, to string, ch ...Change) Entry {
	e := Entry{At: "2026-10-07T00:00:00Z", Op: "update", Source: SourceAgent, Tx: tx, After: &Hashes{"p" + to, to}, Changes: ch}
	if from != "" {
		e.Before = &Hashes{"p" + from, from}
	}
	return e
}

func TestChainAppendAndSince(t *testing.T) {
	root := t.TempDir()
	steps := []Entry{
		entry("t1", "", "a", Change{Path: "plan", Op: "added"}),
		entry("t2", "a", "b", Change{Path: "phases/p1/steps/s1", Op: "changed", From: "draft", To: "in_progress"}),
		entry("t3", "b", "b", Change{Path: "evidence", Op: "changed"}),
		entry("t4", "b", "c", Change{Path: "notes", Op: "appended", Count: 2}),
	}
	for _, e := range steps {
		if err := appendVia(t, root, e, 0); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Load(filepath.Join(root, ".opencode/workplan/p"+Suffix))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Issues) > 0 || len(l.Entries) != 4 {
		t.Fatalf("issues %v, %d entries", l.Issues, len(l.Entries))
	}
	for i, e := range l.Entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq %d at %d", e.Seq, i)
		}
	}
	got, ok := l.Since("a", "c")
	if !ok || len(got) != 3 || got[0].Tx != "t2" || got[2].Tx != "t4" {
		t.Fatalf("since a: %v %v", ok, got)
	}
	if _, ok := l.Since("x", "c"); ok {
		t.Fatal("unknown hash connected")
	}
	if _, ok := l.Since("a", "d"); ok {
		t.Fatal("a state the log never reached connected")
	}
	ours := []Change{{Path: "phases/p1/steps/s2", Op: "changed"}, {Path: "notes", Op: "appended", Count: 1}}
	if c, _, bad := Conflict(ours, got); bad {
		t.Fatalf("disjoint writes conflict on %v", c)
	}
	ours = append(ours, Change{Path: "phases/p1", Op: "removed"})
	if c, by, bad := Conflict(ours, got); !bad || c.Path != "phases/p1/steps/s1" || by.Tx != "t2" {
		t.Fatalf("removing the phase of a changed step: %v %v %v", c, by, bad)
	}
}

func TestTornTailAndBreaks(t *testing.T) {
	root := t.TempDir()
	if err := appendVia(t, root, entry("t1", "", "a"), 0); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".opencode/workplan/p"+Suffix)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":2,"prev":"x","v":1,"at`)
	f.Close()
	if l, _ := Load(p); len(l.Entries) != 1 || len(l.Issues) != 1 {
		t.Fatalf("torn tail: %+v", l)
	}
	// The next append cuts the torn line and continues the chain.
	if err := appendVia(t, root, entry("t2", "a", "b"), 0); err != nil {
		t.Fatal(err)
	}
	l, _ := Load(p)
	if len(l.Entries) != 2 || len(l.Issues) != 0 || l.Entries[1].Seq != 2 {
		t.Fatalf("after repair: %+v", l)
	}
	// An edited line breaks the chain; only the part after it is kept.
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), `"t1"`, `"t0"`, 1)), 0o600)
	l, _ = Load(p)
	if len(l.Entries) != 1 || len(l.Issues) != 1 || !strings.Contains(l.Issues[0], "chain break") {
		t.Fatalf("edited line: %+v", l)
	}
}

func TestRotation(t *testing.T) {
	root := t.TempDir()
	for i, tx := range []string{"t1", "t2", "t3"} {
		from := string(rune('a' + i - 1))
		if i == 0 {
			from = ""
		}
		if err := appendVia(t, root, entry(tx, from, string(rune('a'+i))), 1); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, ".opencode/workplan")
	l, _ := Load(filepath.Join(dir, "p"+Suffix))
	if len(l.Entries) != 1 || l.Entries[0].Seq != 3 {
		t.Fatalf("active segment: %+v", l)
	}
	old, err := os.ReadFile(filepath.Join(dir, "archive/p/history-t3.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	a := Parse(old)
	if len(a.Entries) != 1 || a.Entries[0].Seq != 2 || l.Entries[0].Prev != a.Entries[0].Hash() {
		t.Fatal("rotated segments do not chain")
	}
}

func TestDiff(t *testing.T) {
	s := func(id, status string) model.Step { return model.Step{ID: id, Title: id, Status: status} }
	a := &model.Plan{Goal: "g", Status: "in_progress", Notes: []string{"n1"},
		Phases:   []model.Phase{{ID: "p1", Status: "in_progress", Steps: []model.Step{s("s1", "draft"), s("s2", "draft")}}, {ID: "p2", Steps: []model.Step{s("s3", "draft")}}},
		Findings: []model.Finding{{Title: "f"}}}
	b := a.Clone()
	b.Notes = append(b.Notes, "n2")
	b.Phases[0].Steps[1].Status = "completed"
	b.Phases[1].Steps = append(b.Phases[1].Steps, s("s4", "draft"))
	b.Findings = append(b.Findings, model.Finding{Title: "g"})
	b.UpdatedAt = "later"
	got := Diff(a, b)
	want := []Change{
		{Path: "notes", Op: "appended", Count: 1},
		{Path: "findings/1", Op: "added"},
		{Path: "phases/p1/steps/s2", Op: "changed", From: "draft", To: "completed"},
		{Path: "phases/p2/steps/s4", Op: "added"},
	}
	if len(got) != len(want) {
		t.Fatalf("diff %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("change %d: %v, want %v", i, got[i], want[i])
		}
	}
	c := a.Clone()
	c.Phases[0], c.Phases[1] = c.Phases[1], c.Phases[0]
	c.Notes = []string{"other"}
	if got := Diff(a, c); len(got) != 2 || got[0].Path != "notes" || got[0].Op != "changed" || got[1].Path != "phases" {
		t.Fatalf("reorder and rewrite: %v", got)
	}
}
