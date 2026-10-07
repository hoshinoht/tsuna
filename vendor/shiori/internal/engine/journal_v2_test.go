package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// pendingV2 commits an update with a v2 journal and fails it after
// publication, leaving the journal pending.
func pendingV2(t *testing.T, fault string) (testutil.Root, *Engine, string) {
	t.Helper()
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	data, err := input.ParseMutationInput("update", mustJSON(t, `{"id":"full-plan","appendNotes":["v2"]}`), input.SurfaceCore)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Prepare("update", data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: func(pt string) error {
		if pt == fault {
			return errors.New("injected")
		}
		return nil
	}}})
	var rr *storage.RecoveryRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("want recovery required: %v", err)
	}
	return root, e, rr.JournalPath
}

func recoverT(t *testing.T, e *Engine, mode string) error {
	t.Helper()
	in := `{"id":"full-plan","recovery":"` + mode + `","expectedHash":"` + stateHashFor(t, e, "full-plan") + `"}`
	_, err := runMutation(context.Background(), e, "update", mustJSON(t, in), allowAll{})
	return err
}

func TestJournalV2RecordsImagesByReference(t *testing.T) {
	root, e, jp := pendingV2(t, storage.FaultCleanup)
	data, _ := os.ReadFile(jp)
	if strings.Contains(string(data), "Content") || len(data) > 2048 {
		t.Fatalf("v2 journal inlines content (%d bytes):\n%s", len(data), data)
	}
	p, _ := ojson.Parse(data)
	if v, _ := p.Value.Get("schemaVersion"); v.NumberLiteral() != "2" {
		t.Fatalf("schemaVersion %s", v.NumberLiteral())
	}
	targets, _ := p.Value.Get("targets")
	for _, tg := range targets.Elems() {
		b, _ := tg.Get("beforeBackup")
		bh, _ := tg.Get("beforeHash")
		if bh.Kind() == ojson.Null {
			continue
		}
		got, ok := fileSHA(filepath.Join(root.Path, b.Str()))
		if !ok || got != bh.Str() {
			t.Fatalf("backup %s does not hold the before image", b.Str())
		}
	}
	if err := recoverT(t, e, "rollback"); err != nil {
		t.Fatal(err)
	}
	if m := machinery(t, root.Path); len(m) > 0 {
		t.Fatalf("rollback left %v", m)
	}
	plan, _ := os.ReadFile(filepath.Join(root.Path, ".opencode/workplan/full-plan.json"))
	if strings.Contains(string(plan), `"v2"`) {
		t.Fatal("rollback kept the new plan")
	}
}

func TestJournalV2MissingImageRefusesOnlyThatDirection(t *testing.T) {
	// Published: the backups are the only before images.
	root, e, _ := pendingV2(t, storage.FaultCleanup)
	backups, _ := filepath.Glob(filepath.Join(root.Path, ".opencode/workplan/.*.before"))
	if len(backups) == 0 {
		t.Fatal("no backups")
	}
	os.Remove(backups[0])
	if err := recoverT(t, e, "rollback"); err == nil || !strings.Contains(err.Error(), "before image") {
		t.Fatalf("rollback without its image: %v", err)
	}
	if err := recoverT(t, e, "resume"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if m := machinery(t, root.Path); len(m) > 0 {
		t.Fatalf("resume left %v", m)
	}

	// Not yet published: the staged files are the only after images.
	root, e, _ = pendingV2(t, storage.FaultPublish+":0")
	stages, _ := filepath.Glob(filepath.Join(root.Path, ".opencode/workplan/.full-plan.json.*.stage"))
	if len(stages) != 1 {
		t.Fatalf("stages %v", stages)
	}
	os.Remove(stages[0])
	if err := recoverT(t, e, "resume"); err == nil || !strings.Contains(err.Error(), "staged after image") {
		t.Fatalf("resume without its image: %v", err)
	}
	if err := recoverT(t, e, "rollback"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestJournalV2RejectsForeignImageNames(t *testing.T) {
	_, e, jp := pendingV2(t, storage.FaultCleanup)
	data, _ := os.ReadFile(jp)
	os.WriteFile(jp, []byte(strings.Replace(string(data), ".before\"", ".other\"", 1)), 0o600)
	var ji *JournalInvalidError
	if err := recoverT(t, e, "rollback"); !errors.As(err, &ji) {
		t.Fatalf("foreign backup name accepted: %v", err)
	}
}

func TestJournalV2BeforeImageChangedWhileStaging(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	data, _ := input.ParseMutationInput("update", mustJSON(t, `{"id":"full-plan","appendNotes":["x"]}`), input.SurfaceCore)
	p, err := e.Prepare("update", data)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(root.Path, ".opencode/workplan/full-plan.json")
	old := semanticFiles(t, root.Path)
	_, err = e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: func(pt string) error {
		if pt == storage.FaultStage+":0" {
			// An uncooperative editor between the locked check and the link.
			b, _ := os.ReadFile(planPath)
			os.WriteFile(planPath, append(b, ' '), 0o644)
		}
		return nil
	}}})
	var se *storage.StaleError
	if !errors.As(err, &se) {
		t.Fatalf("want stale: %v", err)
	}
	if m := machinery(t, root.Path); len(m) > 0 {
		t.Fatalf("left %v", m)
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".opencode/workplan/full-plan.transaction.json")); !os.IsNotExist(err) {
		t.Fatal("journal published")
	}
	cur := semanticFiles(t, root.Path)
	for k, v := range old {
		if k != ".opencode/workplan/full-plan.json" && cur[k] != v {
			t.Fatalf("%s changed", k)
		}
	}
}
