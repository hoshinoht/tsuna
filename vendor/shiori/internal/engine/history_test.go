package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func loadLog(t *testing.T, e *Engine, id string) *history.Log {
	t.Helper()
	l, err := history.Load(e.absRel(historyRel(id)))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Issues) > 0 {
		t.Fatalf("log issues: %v", l.Issues)
	}
	return l
}

func stateHash(t *testing.T, e *Engine, id string) string {
	t.Helper()
	s, err := e.loadFresh(id)
	if err != nil {
		t.Fatal(err)
	}
	return s.StateHash
}

func mustRun(t *testing.T, e *Engine, tool, in string) ojson.Value {
	t.Helper()
	out, err := runMutation(context.Background(), e, tool, mustJSON(t, in), allowAll{})
	if err != nil {
		t.Fatalf("%s %s: %v", tool, in, err)
	}
	return out.Value
}

// TestChangeLogRecordsEveryWrite: each kind of write appends one entry
// whose hashes chain to the next and match the plan.
func TestChangeLogRecordsEveryWrite(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	e.Source = history.SourceCLI
	id := "full-plan"
	h := func() string { return stateHash(t, e, id) }
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h()+`","updateSteps":[{"phaseId":"phase-b","stepId":"step-b2","status":"in_progress"}],"appendNotes":["n1"]}`)
	mustRun(t, e, "checkpoint", `{"id":"full-plan","expectedHash":"`+h()+`","summary":"s","nextAction":"a","merge":true}`)
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h()+`","recordEvidence":[{"phaseId":"phase-b","stepId":"step-b1","command":"go test","exitCode":0}]}`)
	mustRun(t, e, "reset", `{"id":"full-plan","expectedHash":"`+h()+`","mode":"draft"}`)
	l := loadLog(t, e, id)
	ops := []string{}
	for _, en := range l.Entries {
		ops = append(ops, en.Op)
		if en.Source != history.SourceCLI {
			t.Fatalf("source %q", en.Source)
		}
	}
	if strings.Join(ops, ",") != "update,checkpoint,update,reset:draft" {
		t.Fatalf("ops %v", ops)
	}
	if got := l.Entries[len(l.Entries)-1].After.StateHash; got != h() {
		t.Fatal("last entry is not the current state")
	}
	first := l.Entries[0]
	want := map[string]string{"notes": "appended", "phases/phase-b/steps/step-b2": "changed"}
	for _, c := range first.Changes {
		if want[c.Path] != c.Op {
			t.Fatalf("unexpected change %+v", c)
		}
		delete(want, c.Path)
	}
	if len(want) > 0 || l.Entries[1].Changes[0].Path != "checkpoint" || l.Entries[2].StateChange() {
		t.Fatalf("changes: %+v", l.Entries)
	}
	if _, ok := l.Since(first.Before.StateHash, h()); !ok {
		t.Fatal("the log does not connect the first and the current state")
	}
}

// TestChangeLogFailureKeepsTheWrite: a failed append leaves a gap, not a
// failed write.
func TestChangeLogFailureKeepsTheWrite(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	before := stateHash(t, e, "full-plan")
	data, err := input.ParseMutationInput("update", mustJSON(t, `{"id":"full-plan","expectedHash":"`+before+`","appendNotes":["x"]}`), input.SurfaceCore)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Prepare("update", data)
	if err != nil {
		t.Fatal(err)
	}
	fault := func(point string) error {
		if point == storage.FaultHistory {
			return errors.New("injected")
		}
		return nil
	}
	if _, err := e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: fault}}); err != nil {
		t.Fatal(err)
	}
	if stateHash(t, e, "full-plan") == before {
		t.Fatal("write did not land")
	}
	if _, err := os.Stat(e.absRel(historyRel("full-plan"))); !os.IsNotExist(err) {
		t.Fatal("log written despite the fault")
	}
}

// TestRebase: a stale update applies over newer writes to other elements
// and is refused over a write to the same element or across a gap.
func TestRebase(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	id := "full-plan"
	h0 := stateHash(t, e, id)
	// Agent A: step-b2 starts, a note.
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h0+`","updateSteps":[{"phaseId":"phase-b","stepId":"step-b2","status":"in_progress"}],"appendNotes":["from A"]}`)
	// Agent B still holds h0.
	stale := `{"id":"full-plan","expectedHash":"` + h0 + `","updateSteps":[{"phaseId":"phase-c","stepId":"step-c1","title":"renamed by B"}],"appendNotes":["from B"]}`
	if _, err := runMutation(context.Background(), e, "update", mustJSON(t, stale), allowAll{}); err == nil || strings.Contains(err.Error(), "Not rebased") {
		t.Fatalf("without rebase: %v", err)
	}
	out := mustRun(t, e, "update", strings.Replace(stale, `"id":`, `"rebase":true,"id":`, 1))
	rb, ok := out.Get("rebased")
	if !ok {
		t.Fatal("no rebased member")
	}
	if from, _ := rb.Get("fromHash"); from.Str() != h0 {
		t.Fatal("fromHash")
	}
	s, _ := e.loadFresh(id)
	st := s.Plan.Phases[1].Steps[1]
	if st.Status != "in_progress" || s.Plan.Phases[2].Steps[0].Title != "renamed by B" {
		t.Fatal("rebased write lost a change")
	}
	if n := s.Plan.Notes; n[len(n)-2] != "from A" || n[len(n)-1] != "from B" {
		t.Fatalf("notes %v", n)
	}
	// A conflicting stale write is refused and names the element.
	conflict := `{"rebase":true,"id":"full-plan","expectedHash":"` + h0 + `","updateSteps":[{"phaseId":"phase-b","stepId":"step-b2","status":"review"}]}`
	_, err := runMutation(context.Background(), e, "update", mustJSON(t, conflict), allowAll{})
	var stale2 *StaleHashError
	if !errors.As(err, &stale2) || !strings.Contains(err.Error(), "phases/phase-b/steps/step-b2 was also changed by update") {
		t.Fatalf("conflict: %v", err)
	}
	// An edit outside Shiori leaves a gap the rebase does not cross.
	pj := filepath.Join(root.Path, ".opencode/workplan/full-plan.json")
	b, _ := os.ReadFile(pj)
	os.WriteFile(pj, []byte(strings.Replace(string(b), `"goal": "`, `"goal": "edited `, 1)), 0o644)
	_, err = runMutation(context.Background(), e, "update", mustJSON(t, strings.Replace(stale, `"id":`, `"rebase":true,"id":`, 1)), allowAll{})
	if err == nil || !strings.Contains(err.Error(), "does not connect") {
		t.Fatalf("across a gap: %v", err)
	}
	// The operator default turns it on without the member.
	e.Rebase = true
	h2 := stateHash(t, e, id)
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h2+`","appendNotes":["C"]}`)
	if out := mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h2+`","appendNotes":["D"]}`); !has(out, "rebased") {
		t.Fatal("operator default not applied")
	}
	if _, err := runMutation(context.Background(), e, "update", mustJSON(t, `{"rebase":false,"id":"full-plan","expectedHash":"`+h2+`","appendNotes":["E"]}`), allowAll{}); err == nil {
		t.Fatal("rebase:false still rebased")
	}
}

// TestChangeLogRotation: a log at the rotation size names its archive in
// the intent and moves there before the append.
func TestChangeLogRotation(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	h := stateHash(t, e, "full-plan")
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h+`","appendNotes":["a"]}`)
	logPath := e.absRel(historyRel("full-plan"))
	line, _ := os.ReadFile(logPath)
	pad := strings.Repeat(" ", history.RotateAt) + "\n"
	os.WriteFile(logPath, append([]byte(pad), line...), 0o600)
	data, _ := input.ParseMutationInput("update", mustJSON(t, `{"id":"full-plan","expectedHash":"`+stateHash(t, e, "full-plan")+`","appendNotes":["b"]}`), input.SurfaceCore)
	p, err := e.Prepare("update", data)
	if err != nil {
		t.Fatal(err)
	}
	arch := p.Intent.Resources().Archive
	if len(arch) != 1 || !strings.Contains(arch[0], "/archive/full-plan/history-") {
		t.Fatalf("archive resources %v", arch)
	}
	if _, err := e.Execute(context.Background(), p, allowAll{}, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(arch[0]); err != nil {
		t.Fatal(err)
	}
	l := loadLog(t, e, "full-plan")
	if len(l.Entries) != 1 || l.Entries[0].Seq != 2 || l.Entries[0].Prev != storage.LineHash(line[:len(line)-1]) {
		t.Fatalf("new segment %+v", l.Entries)
	}
}

// TestSinceCheckpointAndStalled: resume summarizes the writes after the
// last checkpoint; doctor reports a step in progress for days without
// evidence.
func TestSinceCheckpointAndStalled(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	id := "full-plan"
	h := func() string { return stateHash(t, e, id) }
	mustRun(t, e, "checkpoint", `{"id":"full-plan","expectedHash":"`+h()+`","summary":"s","nextAction":"a","merge":true}`)
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h()+`","updateSteps":[{"phaseId":"phase-b","stepId":"step-b2","status":"in_progress"}],"appendNotes":["x","y"]}`)
	v, _, err := e.Resume(input.ResumeInput{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	since, ok := v.Get("sinceCheckpoint")
	if !ok {
		t.Fatal("no sinceCheckpoint")
	}
	if w, _ := since.Get("writes"); w.NumberLiteral() != "1" {
		t.Fatalf("writes %s", w.NumberLiteral())
	}
	if n, _ := since.Get("notesAppended"); n.NumberLiteral() != "2" {
		t.Fatal("notes")
	}
	steps, _ := since.Get("steps")
	if len(steps.Elems()) != 1 {
		t.Fatalf("steps %s", ojson.Compact(steps))
	}
	stalled := func() int {
		d, err := e.Doctor(input.DoctorInput{ID: &id})
		if err != nil {
			t.Fatal(err)
		}
		plans, _ := d.Get("plans")
		hv, ok := plans.Elems()[0].Get("history")
		if !ok {
			t.Fatal("no history member")
		}
		st, _ := hv.Get("stalledSteps")
		return len(st.Elems())
	}
	if stalled() != 0 {
		t.Fatal("stalled right away")
	}
	old := Clock
	Clock = func() time.Time { return old().Add(StallAfter + time.Hour) }
	defer func() { Clock = old }()
	if stalled() != 1 {
		t.Fatal("step-b2 not reported after StallAfter")
	}
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h()+`","recordEvidence":[{"phaseId":"phase-b","stepId":"step-b2","command":"go test","exitCode":0}]}`)
	if stalled() != 0 {
		t.Fatal("still stalled after new evidence")
	}
}
