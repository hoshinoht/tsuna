package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
)

// laneRoot is evidenceRoot plus committed plan files and a dependency
// s2 -> s1, so worktrees start from a clean HEAD.
func laneRoot(t *testing.T) (*Engine, string) {
	t.Helper()
	e, root := evidenceRoot(t, true)
	os.WriteFile(filepath.Join(root, "README"), []byte("r\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "web"), 0o755)
	os.WriteFile(filepath.Join(root, "web", "w.go"), []byte("package w\n"), 0o644)
	if _, err := updateT(t, e, `"dependencies":[{"phaseId":"p1","stepId":"s2","dependsOn":[{"phaseId":"p1","stepId":"s1"}]}]`); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-qm", "plan")
	return e, root
}

func updateT(t *testing.T, e *Engine, members string) (Output, error) {
	t.Helper()
	in, err := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + stateOf(t, e).StateHash + `",` + members + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return runMutation(context.Background(), e, "update", in.Value, allowAll{})
}

func lanesT(t *testing.T, e *Engine, ops string) (Output, error) {
	t.Helper()
	return updateT(t, e, `"lanes":[`+ops+`]`)
}

func mustLanes(t *testing.T, e *Engine, ops string) Output {
	t.Helper()
	out, err := lanesT(t, e, ops)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func doctorLanes(t *testing.T, e *Engine) ojson.Value {
	t.Helper()
	v, err := e.Doctor(input.DoctorInput{})
	if err != nil {
		t.Fatal(err)
	}
	plans, _ := v.Get("plans")
	l, ok := plans.Elems()[0].Get("lanes")
	if !ok {
		t.Fatal("doctor has no lanes member")
	}
	return l
}

const proposeBoth = `{"op":"propose","laneId":"core","steps":[{"phaseId":"p1","stepId":"s1"}],"claims":["src"]},
	{"op":"propose","laneId":"web","steps":[{"phaseId":"p1","stepId":"s2"}],"claims":["web/"]}`

func TestLanesProposeKeepsPlanAndRefusesOverlaps(t *testing.T) {
	e, root := laneRoot(t)
	before := stateOf(t, e)
	mustLanes(t, e, proposeBoth)
	if after := stateOf(t, e); after.StateHash != before.StateHash || string(after.JSON.Bytes) != string(before.JSON.Bytes) {
		t.Fatal("a lanes-only update changed the plan")
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode/workplan/demo.lanes.json")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		`{"op":"propose","laneId":"x","steps":[{"phaseId":"p1","stepId":"s1"}]}`:                        "already belongs to active lane core",
		`{"op":"propose","laneId":"core","steps":[{"phaseId":"p1","stepId":"s2"}]}`:                     "already exists",
		`{"op":"propose","laneId":"Bad_Id","steps":[{"phaseId":"p1","stepId":"s2"}]}`:                   "laneId must be lowercase",
		`{"op":"claims","laneId":"web","add":["src/a.go"]}`:                                             "overlaps src claimed by lane core",
		`{"op":"claims","laneId":"web","add":[".opencode/workplan"]}`:                                   "workplan directory",
		`{"op":"claims","laneId":"web","remove":["docs"]}`:                                              "does not claim docs",
		`{"op":"transition","laneId":"web","state":"running"}`:                                          "cannot move from claimed to running",
		`{"op":"transition","laneId":"web","state":"abandoned","checkout":{"path":"."}}`:                "checkout is recorded when a lane becomes prepared",
		`{"op":"transition","laneId":"web","state":"prepared","checkout":{"path":"."}}`:                 "outside the project root",
		`{"op":"transition","laneId":"web","state":"prepared","checkout":{"path":"/nonexistent/lane"}}`: "does not exist",
		`{"op":"propose","laneId":"y","steps":[]}`:                                                      "at least one step",
		`{"op":"claims","laneId":"web"}`:                                                                "needs add or remove",
		`{"op":"transition","laneId":"web","state":"prepared","claims":["x"]}`:                          "Not allowed for op transition",
	}
	for op, want := range cases {
		if _, err := lanesT(t, e, op); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", op, err, want)
		}
	}
	// A different repository is not a lane checkout.
	other := t.TempDir()
	gitT(t, other, "init", "-q")
	if _, err := lanesT(t, e, `{"op":"transition","laneId":"web","state":"prepared","checkout":{"path":"`+other+`"}}`); err == nil || !strings.Contains(err.Error(), "not a worktree of this repository") {
		t.Fatalf("foreign repository: %v", err)
	}
}

func TestLanesWorktreeLifecycle(t *testing.T) {
	e, root := laneRoot(t)
	mustLanes(t, e, proposeBoth)
	wt := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-core")
	t.Cleanup(func() { os.RemoveAll(wt) })
	gitT(t, root, "worktree", "add", "-q", wt, "-b", "lane/core")
	if _, err := lanesT(t, e, `{"op":"transition","laneId":"core","state":"prepared","checkout":{"path":"`+wt+`","branch":"other"}}`); err == nil || !strings.Contains(err.Error(), `not "other"`) {
		t.Fatalf("wrong branch: %v", err)
	}
	mustLanes(t, e, `{"op":"transition","laneId":"core","state":"prepared","checkout":{"path":"`+wt+`"}},{"op":"transition","laneId":"core","state":"running"}`)

	// Work in the lane, one file outside its claims.
	os.WriteFile(filepath.Join(wt, "src", "a.go"), []byte("package a // lane\n"), 0o644)
	os.WriteFile(filepath.Join(wt, "README"), []byte("lane\n"), 0o644)
	l := doctorLanes(t, e)
	if order, _ := l.Get("mergeOrder"); strings.Join(strs(order), ",") != "core,web" {
		t.Fatalf("merge order %v", strs(order))
	}
	lanesV, _ := l.Get("lanes")
	core := lanesV.Elems()[0]
	co, _ := core.Get("checkout")
	oc, _ := co.Get("outsideClaims")
	if paths, _ := oc.Get("paths"); strings.Join(strs(paths), ",") != "README" {
		t.Fatalf("outside claims %s", ojson.Compact(oc))
	}
	if bb, _ := lanesV.Elems()[1].Get("blockedBy"); strings.Join(strs(bb), ",") != "core" {
		t.Fatalf("blockedBy %s", ojson.Compact(bb))
	}

	// Evidence recorded in the lane is compared with the lane while it is
	// active, and with the project tree once it is merged.
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0,"lane":"core"}`); err != nil {
		t.Fatal(err)
	}
	if got := stepStates(t, e); got["s1"] != "fresh" {
		t.Fatalf("lane evidence %v", got)
	}
	gitT(t, wt, "add", "-A")
	gitT(t, wt, "commit", "-qm", "lane")
	os.WriteFile(filepath.Join(root, "web", "w.go"), []byte("package w // main moved on\n"), 0o644)
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-qm", "main")
	gitT(t, root, "merge", "-q", "--no-edit", "lane/core")
	out := mustLanes(t, e, `{"op":"transition","laneId":"core","state":"review"},{"op":"transition","laneId":"core","state":"integrating"},{"op":"transition","laneId":"core","state":"merged"}`)
	if w, _ := out.Value.Get("warnings"); !strings.Contains(string(ojson.Compact(w)), "recorded merged while step p1/s1 is draft") {
		t.Fatalf("merge warnings %s", ojson.Compact(w))
	}
	if got := stepStates(t, e); got["s1"] != "stale" {
		t.Fatalf("lane evidence after merging into a moved main: %v", got)
	}
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0}`); err != nil {
		t.Fatal(err)
	}
	if got := stepStates(t, e); got["s1"] != "fresh" {
		t.Fatalf("re-run on the combined state: %v", got)
	}
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0,"lane":"core"}`); err == nil {
		t.Fatal("evidence for a merged lane accepted")
	}
	// The merged lane's checkout still exists: cleanup is reported, never done.
	l = doctorLanes(t, e)
	lanesV, _ = l.Get("lanes")
	if iss, _ := lanesV.Elems()[0].Get("issues"); !strings.Contains(string(ojson.Compact(iss)), "cleanup required") {
		t.Fatalf("issues %s", ojson.Compact(iss))
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("the checkout was touched")
	}
	// A step of a merged lane can be proposed again.
	mustLanes(t, e, `{"op":"propose","laneId":"core-2","steps":[{"phaseId":"p1","stepId":"s1"}],"claims":["src"]}`)
}

func TestLanesDirtyBaselineAndUnownedWorktrees(t *testing.T) {
	e, root := laneRoot(t)
	os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a // dirty\n"), 0o644)
	out := mustLanes(t, e, proposeBoth)
	if w, _ := out.Value.Get("warnings"); !strings.Contains(string(ojson.Compact(w)), "1 uncommitted path(s) (for example src/a.go)") {
		t.Fatalf("warnings %s", ojson.Compact(w))
	}
	stray := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-stray")
	t.Cleanup(func() { os.RemoveAll(stray) })
	gitT(t, root, "worktree", "add", "-q", stray)
	l := doctorLanes(t, e)
	un, _ := l.Get("unownedWorktrees")
	if len(un.Elems()) != 1 || !strings.HasSuffix(un.Elems()[0].Str(), "-stray") {
		t.Fatalf("unowned %s", ojson.Compact(un))
	}
	// Inspect and resume show the owning lane.
	v, err := e.Inspect(input.InspectInput{ID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := v.Get("steps")
	ln, _ := steps.Elems()[0].Get("lane")
	if id, _ := ln.Get("laneId"); id.Str() != "core" {
		t.Fatalf("inspect lane %s", ojson.Compact(ln))
	}
	rv, _, err := e.Resume(input.ResumeInput{ID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	rl, _ := rv.Get("lanes")
	if a, _ := rl.Get("active"); a.NumberLiteral() != "2" {
		t.Fatalf("resume lanes %s", ojson.Compact(rl))
	}
}

func TestLanesInvalidSidecarAndRecovery(t *testing.T) {
	e, root := laneRoot(t)
	p := filepath.Join(root, ".opencode/workplan/demo.lanes.json")
	os.WriteFile(p, []byte(`{"schemaVersion":1}`), 0o600)
	if _, err := lanesT(t, e, proposeBoth); err == nil || !strings.Contains(err.Error(), "lanes sidecar at") {
		t.Fatalf("invalid sidecar: %v", err)
	}
	l := doctorLanes(t, e)
	if valid, _ := l.Get("valid"); valid.Bool() {
		t.Fatal("invalid sidecar reported valid")
	}
	os.Remove(p)

	in, _ := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + stateOf(t, e).StateHash + `","lanes":[` + proposeBoth + `]}`))
	data, err := input.ParseMutationInput("update", in.Value, input.SurfaceCore)
	if err != nil {
		t.Fatal(err)
	}
	prep, err := e.Prepare("update", data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Execute(context.Background(), prep, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: func(pt string) error {
		if pt == storage.FaultCleanup {
			return errors.New("injected")
		}
		return nil
	}}})
	var rr *storage.RecoveryRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("want recovery: %v", err)
	}
	rin, _ := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + stateHashFor(t, e, "demo") + `","recovery":"rollback"}`))
	if _, err := runMutation(context.Background(), e, "update", rin.Value, allowAll{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("rollback kept the lanes sidecar")
	}
}

// TestLanesChangeOverlaps: a file both active lanes change is reported,
// even outside both claims.
func TestLanesChangeOverlaps(t *testing.T) {
	e, root := laneRoot(t)
	mustLanes(t, e, proposeBoth)
	for _, id := range []string{"core", "web"} {
		wt := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-"+id)
		t.Cleanup(func() { os.RemoveAll(wt) })
		gitT(t, root, "worktree", "add", "-q", wt, "-b", "lane/"+id)
		mustLanes(t, e, `{"op":"transition","laneId":"`+id+`","state":"prepared","checkout":{"path":"`+wt+`"}}`)
		os.WriteFile(filepath.Join(wt, "README"), []byte(id+"\n"), 0o644)
	}
	ov, ok := doctorLanes(t, e).Get("changeOverlaps")
	if !ok || string(ojson.Compact(ov)) != `[{"path":"README","lanes":["core","web"]}]` {
		t.Fatalf("overlaps %s", ojson.Compact(ov))
	}
}

func TestPlanQuality(t *testing.T) {
	v := func(s string) *string { return &s }
	p := &model.Plan{Phases: []model.Phase{{ID: "p", Steps: []model.Step{
		{ID: "a", Status: "draft"},
		{ID: "b", Status: "in_progress", Validation: v("Looks right to a reviewer")},
		{ID: "c", Status: "draft", Validation: v("go test ./...")},
		{ID: "d", Status: "draft", Validation: v("Run `make check` and read the output")},
		{ID: "e", Status: "completed"},
	}}}}
	q, ok := planQuality(p)
	if !ok {
		t.Fatal("nothing reported")
	}
	got := string(ojson.Compact(q))
	for _, want := range []string{`"stepsWithoutValidation":{"count":1,"steps":[{"phaseId":"p","stepId":"a"}]}`, `"stepsWithoutCommand":{"count":1,"steps":[{"phaseId":"p","stepId":"b"}]}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s lacks %s", got, want)
		}
	}
	done := &model.Plan{Phases: []model.Phase{{ID: "p", Steps: []model.Step{{ID: "a", Status: "completed"}}}},
		Findings: []model.Finding{{Severity: "blocker", Title: "x"}}}
	if q, ok := planQuality(done); !ok || !strings.Contains(string(ojson.Compact(q)), `"highFindingsWithoutOpenWork":1`) {
		t.Fatal("open blocker with nothing open not reported")
	}
}
