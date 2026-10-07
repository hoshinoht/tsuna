package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
)

func TestPlanLinksAndPortfolio(t *testing.T) {
	freezeClock(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e, _ := New(root)
	for _, id := range []string{"roadmap", "migration", "docs"} {
		mustRun(t, e, "create", `{"id":"`+id+`","goal":"g","phases":[{"id":"p","title":"P","steps":[{"id":"s","title":"S"}]}]}`)
	}
	if _, ok, _ := e.Portfolio(); ok {
		t.Fatal("portfolio without links")
	}
	before := stateHash(t, e, "migration")
	out := mustRun(t, e, "update", `{"id":"migration","expectedHash":"`+before+`","planLinks":[{"planId":"roadmap","relation":"blockedBy"},{"planId":"docs","relation":"related"},{"planId":"later","relation":"blocks"}]}`)
	if w, _ := out.Get("warnings"); !strings.Contains(string(ojson.Compact(w)), "plan later does not exist") {
		t.Fatalf("warnings %s", ojson.Compact(w))
	}
	if stateHash(t, e, "migration") != before {
		t.Fatal("a links-only update changed the plan state")
	}
	for in, want := range map[string]string{
		`[{"planId":"migration","relation":"blocks"}]`:                                  "cannot link to itself",
		`[{"planId":"docs","relation":"blocks"},{"planId":"docs","relation":"blocks"}]`: "duplicate blocks link",
		`[{"planId":"docs","relation":"precedes"}]`:                                     "relation",
	} {
		_, err := runMutation(context.Background(), e, "update", mustJSON(t, `{"id":"migration","expectedHash":"`+before+`","planLinks":`+in+`}`), allowAll{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
	pf, ok, err := e.Portfolio()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	got := string(ojson.Compact(pf))
	for _, want := range []string{`"blocks":[{"from":"roadmap","to":"migration"}]`, `"related":[["docs","migration"]]`,
		`{"id":"migration","title":"","status":"draft","stepsCompleted":0,"steps":1,"blockedBy":["roadmap"],"waitingOn":["roadmap"]}`,
		`"issues":["migration: links to missing plan later"]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("portfolio %s lacks %s", got, want)
		}
	}
	v, _, err := e.Resume(input.ResumeInput{ID: "migration"})
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := v.Get("waitingOnPlans"); !ok || string(ojson.Compact(w)) != `["roadmap"]` {
		t.Fatalf("resume waitingOnPlans %s", ojson.Compact(w))
	}
	d, _ := e.Doctor(input.DoctorInput{})
	if _, ok := d.Get("portfolio"); !ok {
		t.Fatal("doctor has no portfolio")
	}
	// Finishing the blocker releases the wait; a reverse link is a cycle.
	mustRun(t, e, "update", `{"id":"roadmap","expectedHash":"`+stateHash(t, e, "roadmap")+`","planLinks":[{"planId":"migration","relation":"blockedBy"}]}`)
	pf, _, _ = e.Portfolio()
	if c, ok := pf.Get("cycle"); !ok || len(c.Elems()) != 3 {
		t.Fatalf("cycle %s", ojson.Compact(pf))
	}
	mustRun(t, e, "update", `{"id":"roadmap","expectedHash":"`+stateHash(t, e, "roadmap")+`","planLinks":[]}`)
	pj := filepath.Join(root, ".opencode/workplan/roadmap.json")
	b, _ := os.ReadFile(pj)
	os.WriteFile(pj, []byte(strings.Replace(string(b), `"status": "draft",
  "createdAt"`, `"status": "completed",
  "createdAt"`, 1)), 0o644)
	if w := e.waitingOnPlans("migration"); len(w) != 0 {
		t.Fatalf("still waiting on a completed plan: %v", w)
	}
}
