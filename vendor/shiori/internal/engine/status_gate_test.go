package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func gateErr(t *testing.T, err error, status string, paths ...string) {
	t.Helper()
	var g *StatusGateError
	if !errors.As(err, &g) || g.Status != status {
		t.Fatalf("want status gate for %s, got %v", status, err)
	}
	if ErrorClass(err) != "invalid_structure" {
		t.Fatalf("class %s", ErrorClass(err))
	}
	var got []string
	for _, is := range g.Issues {
		got = append(got, is.PathString())
		if !strings.Contains(err.Error(), is.String()) {
			t.Fatalf("message lacks %q: %s", is.String(), err)
		}
	}
	if strings.Join(got, ",") != strings.Join(paths, ",") {
		t.Fatalf("issue paths %v want %v", got, paths)
	}
}

// TestStatusGate is item D (create/update) and the patch issue list.
func TestStatusGate(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "empty-workspace")
	e := mustEngine(t, root.Path)
	incomplete := `"phases":[{"id":"p","title":"P","steps":[{"id":"a","title":"A","action":"do"},{"id":"b","title":"B"}]}]`
	for _, st := range []string{"in_progress", "review", "completed"} {
		before := testutil.Fingerprint(t, root.Path)
		p, _ := ojson.Parse([]byte(`{"id":"gate-` + strings.ReplaceAll(st, "_", "-") + `","goal":"g","status":"` + st + `",` + incomplete + `}`))
		auth := &countingAuth{}
		_, err := runMutation(context.Background(), e, "workplan_create", p.Value, auth)
		gateErr(t, err, st, "phases.0.steps.0.validation", "phases.0.steps.1.action", "phases.0.steps.1.validation")
		if auth.n != 0 {
			t.Fatal("gate must refuse before authorization")
		}
		if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
			t.Fatalf("refusal wrote: %v", d)
		}
	}
	// No phases at all.
	p, _ := ojson.Parse([]byte(`{"id":"empty-gate","goal":"g","status":"in_progress"}`))
	_, err := runMutation(context.Background(), e, "workplan_create", p.Value, &countingAuth{})
	gateErr(t, err, "in_progress", "phases")
	// draft, blocked and cancelled stay allowed.
	for _, st := range []string{"draft", "blocked", "cancelled"} {
		mustMutate(t, e, "workplan_create", `{"id":"ok-`+st+`","goal":"g","status":"`+st+`",`+incomplete+`}`)
	}
	// Update: gated statuses refused while incomplete, allowed once the
	// same call completes the structure.
	p, _ = ojson.Parse([]byte(`{"id":"ok-draft","status":"review"}`))
	_, err = runMutation(context.Background(), e, "workplan_update", p.Value, &countingAuth{})
	gateErr(t, err, "review", "phases.0.steps.0.validation", "phases.0.steps.1.action", "phases.0.steps.1.validation")
	mustMutate(t, e, "workplan_update", `{"id":"ok-draft","status":"blocked"}`)
	mustMutate(t, e, "workplan_update", `{"id":"ok-draft","appendNotes":["unrelated updates stay allowed"]}`)
	mustMutate(t, e, "workplan_update", `{"id":"ok-draft","status":"in_progress","updateSteps":[
		{"phaseId":"p","stepId":"a","validation":"check a"},{"phaseId":"p","stepId":"b","action":"do b","validation":"check b"}]}`)
	mustMutate(t, e, "workplan_create", `{"id":"ok-valid","goal":"g","status":"in_progress","phases":[{"id":"p","title":"P","steps":[{"id":"a","title":"A","action":"do","validation":"check"}]}]}`)

	// Patch validate returns the issue list.
	id := "ok-blocked"
	s, _ := e.load(id)
	pf := s.Plan.PlanFile
	patch := "*** Begin Patch\n*** Update File: " + pf + "\n@@\n-# " + id + "\n+# " + id + " (edited)\n*** End Patch"
	p, _ = ojson.Parse([]byte(fmt.Sprintf(`{"id":%q,"patchText":%q,"validate":true}`, id, patch)))
	out, err := runMutation(context.Background(), e, "workplan_patch", p.Value, &countingAuth{})
	if err != nil {
		t.Fatal(err)
	}
	val, _ := out.Metadata.Get("validation")
	issues, _ := val.Get("issues")
	cnt, _ := val.Get("issueCount")
	n, _ := cnt.Float()
	if int(n) != 3 || len(issues.Elems()) != 3 || issues.Elems()[0].Str() != "phases.0.steps.0.validation: Required for executable workplans" {
		t.Fatalf("patch validation %s", ojson.Compact(val))
	}
	if w, ok := val.Get("warnings"); !ok || len(w.Elems()) != 1 {
		t.Fatalf("patched Markdown must carry the drift warning: %s", ojson.Compact(val))
	}
}

// TestStepStatusGate: a step cannot be set to in_progress,
// review or completed while it lacks its own required structure; the
// refusal comes before authorization with field paths and class
// invalid_structure. draft/blocked/cancelled stay allowed, completing the
// fields in the same call is accepted, and unchanged gated steps are not
// re-checked.
func TestStepStatusGate(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	// A step without action/validation (allowed while draft).
	mustMutate(t, e, "workplan_update", `{"id":"minimal","addSteps":[{"phaseId":"phase-one","step":{"id":"bare","title":"Bare step"}}]}`)
	for _, st := range []string{"in_progress", "review", "completed"} {
		before := testutil.Fingerprint(t, root.Path)
		_, err, n := mutateErr(t, e, "workplan_update", `{"id":"minimal","updateSteps":[{"phaseId":"phase-one","stepId":"bare","status":"`+st+`"}]}`)
		var gate *StepStatusGateError
		if !errors.As(err, &gate) || n != 0 {
			t.Fatalf("%s: err %v (authorizations %d), want a step gate refusal before authorization", st, err, n)
		}
		if ErrorClass(err) != "invalid_structure" {
			t.Fatalf("class %s", ErrorClass(err))
		}
		var paths []string
		for _, is := range gate.StructuredIssues() {
			paths = append(paths, is.PathString())
		}
		if strings.Join(paths, ",") != "phases.0.steps.1.action,phases.0.steps.1.validation" {
			t.Fatalf("issue paths %v", paths)
		}
		if !strings.Contains(err.Error(), "phase-one/bare -> "+st) {
			t.Fatalf("message %q", err.Error())
		}
		if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
			t.Fatalf("refusal wrote: %v", d)
		}
	}
	for _, st := range []string{"blocked", "cancelled", "draft"} {
		mustMutate(t, e, "workplan_update", `{"id":"minimal","updateSteps":[{"phaseId":"phase-one","stepId":"bare","status":"`+st+`"}]}`)
	}
	// Completing the fields in the same call is accepted.
	mustMutate(t, e, "workplan_update", `{"id":"minimal","updateSteps":[{"phaseId":"phase-one","stepId":"bare","status":"in_progress","action":"Do it","validation":"Check it"}]}`)
	// A new step or phase with a gated status is checked too.
	for _, in := range []string{
		`{"id":"minimal","addSteps":[{"phaseId":"phase-one","step":{"id":"new","title":"New","status":"in_progress","action":"a"}}]}`,
		`{"id":"minimal","addPhases":[{"phase":{"id":"p2","title":"P2","steps":[{"id":"s","title":"S","status":"completed","validation":"v"}]}}]}`,
		`{"id":"minimal","phases":[{"id":"phase-one","title":"Phase one","steps":[{"id":"step-one","title":"Step one","status":"review"}]}]}`,
	} {
		_, err, n := mutateErr(t, e, "workplan_update", in)
		var gate *StepStatusGateError
		if !errors.As(err, &gate) || n != 0 {
			t.Fatalf("%s: err %v (authorizations %d)", in, err, n)
		}
	}
	// A gated step whose status does not change is not re-checked: an
	// existing in-progress step without structure stays editable.
	p := planFile(t, root.Path, "minimal")
	p.Phases[0].Steps[0].Action = nil
	p.Phases[0].Steps[0].Status = "in_progress"
	os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/minimal.json"), p.EncodeStored(), 0o644)
	mustMutate(t, e, "workplan_update", `{"id":"minimal","appendNotes":["still editable"],"updateSteps":[{"phaseId":"phase-one","stepId":"step-one","title":"Renamed"}]}`)
	// Create applies the same gate to the steps it creates.
	before := testutil.Fingerprint(t, root.Path)
	_, err, n := mutateErr(t, e, "workplan_create", `{"id":"made","goal":"g","phases":[{"id":"p","title":"P","steps":[{"id":"ok","title":"OK","status":"completed","action":"a","validation":"v"},{"id":"s","title":"S","status":"review","action":"a"}]}]}`)
	var cgate *StepStatusGateError
	if !errors.As(err, &cgate) || n != 0 || ErrorClass(err) != "invalid_structure" || len(cgate.Issues) != 1 || cgate.Issues[0].PathString() != "phases.0.steps.1.validation" || !strings.Contains(err.Error(), "p/s -> review") {
		t.Fatalf("create step gate: %v (authorizations %d)", err, n)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("create refusal wrote: %v", d)
	}
	mustMutate(t, e, "workplan_create", `{"id":"made","goal":"g","phases":[{"id":"p","title":"P","steps":[{"id":"s","title":"S","status":"blocked"},{"id":"t","title":"T","status":"in_progress","action":"a","validation":"v"}]}]}`)
}
