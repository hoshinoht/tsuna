package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/resume"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// roadmapDeps is a 12-entry cross-phase graph over the synthetic
// roadmap (seedRoadmap): milestone M1 is p2-p5, M2 is p6-p9 and M3 is
// p10-p12, and each milestone waits on the previous one's gate step.
// Steps are numbered s1..s38 across phases (p0: s1-s3, p1: s4-s6, ...).
const roadmapDeps = `[
	{"phaseId":"p3","stepId":"s10","dependsOn":[{"phaseId":"p2","stepId":"s7"},{"phaseId":"p1","stepId":"s4"}]},
	{"phaseId":"p4","stepId":"s13","dependsOn":[{"phaseId":"p3","stepId":"s10"},{"phaseId":"p2","stepId":"s8"}]},
	{"phaseId":"p5","stepId":"s16","dependsOn":[{"phaseId":"p4","stepId":"s13"}]},
	{"phaseId":"p6","stepId":"s19","dependsOn":[{"phaseId":"p5","stepId":"s16"}]},
	{"phaseId":"p7","stepId":"s22","dependsOn":[{"phaseId":"p5","stepId":"s16"}]},
	{"phaseId":"p8","stepId":"s25","dependsOn":[{"phaseId":"p6","stepId":"s19"},{"phaseId":"p7","stepId":"s22"}]},
	{"phaseId":"p9","stepId":"s28","dependsOn":[{"phaseId":"p8","stepId":"s25"}]},
	{"phaseId":"p10","stepId":"s31","dependsOn":[{"phaseId":"p9","stepId":"s28"}]},
	{"phaseId":"p10","stepId":"s32","dependsOn":[{"phaseId":"p5","stepId":"s16"}]},
	{"phaseId":"p11","stepId":"s34","dependsOn":[{"phaseId":"p10","stepId":"s31"}]},
	{"phaseId":"p12","stepId":"s37","dependsOn":[{"phaseId":"p11","stepId":"s34"},{"phaseId":"p10","stepId":"s32"}]},
	{"phaseId":"p12","stepId":"s38","dependsOn":[{"phaseId":"p12","stepId":"s37"}]}]`

// seedGraphRoadmap is the synthetic roadmap plus roadmapDeps and a
// checkpoint refreshed after the dependency write (so it stays fresh).
func seedGraphRoadmap(t *testing.T) (*Engine, testutil.Root) {
	t.Helper()
	e, root := seedRoadmap(t)
	mustMutate(t, e, "workplan_update", `{"id":"roadmap","dependencies":`+roadmapDeps+`}`)
	mustMutate(t, e, "workplan_checkpoint", fmt.Sprintf(`{"id":"roadmap","summary":%q,"nextAction":%q,"phaseId":"p2","stepId":"s7"}`,
		words("Summary", 700), words("Next action", 300)))
	return e, root
}

func strMember(v ojson.Value, keys ...string) string {
	for _, k := range keys {
		v, _ = v.Get(k)
	}
	return v.Str()
}

func numMember(v ojson.Value, keys ...string) int {
	for _, k := range keys {
		v, _ = v.Get(k)
	}
	f, _ := v.Float()
	return int(f)
}

func strs(v ojson.Value) []string {
	var out []string
	for _, x := range v.Elems() {
		out = append(out, x.Str())
	}
	return out
}

func containsPrefix(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// checkReadyOrder asserts the page order: ready active work first by
// non-increasing unblocks, then blocked work; blockedBy within the cap.
func checkReadyOrder(t *testing.T, label string, v ojson.Value, listCap int) {
	t.Helper()
	items, _ := v.Get("page")
	items, _ = items.Get("items")
	blocked := false
	last := -1
	for i, it := range items.Elems() {
		if strMember(it, "kind") != "active-work" {
			continue
		}
		switch strMember(it, "readiness") {
		case "ready":
			u := numMember(it, "unblocks")
			if blocked || (last >= 0 && u > last) {
				t.Fatalf("%s: item %d breaks the ready ranking", label, i)
			}
			last = u
		case "blocked":
			blocked = true
			bb, _ := it.Get("blockedBy")
			if n := len(bb.Elems()); n == 0 || n > listCap {
				t.Fatalf("%s: item %d blockedBy %d entries (cap %d)", label, i, n, listCap)
			}
		default:
			t.Fatalf("%s: item %d has no readiness", label, i)
		}
	}
}

// TestResumeReadiness: readiness and ranking on the 12-entry cross-phase
// graph.
func TestResumeReadiness(t *testing.T) {
	e, root := seedGraphRoadmap(t)
	before := testutil.Fingerprint(t, root.Path)
	max, limit := 64000, 100
	v, text, err := e.Resume(input.ResumeInput{ID: "roadmap", MaxChars: &max, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := v.Get("checkpoint")
	cur, _ = cur.Get("current")
	if strMember(cur, "stepId") != "s7" || strMember(cur, "readiness") != "ready" || numMember(cur, "unblocks") != 12 {
		t.Fatalf("current %s", ojson.Compact(cur))
	}
	items, _ := v.Get("page")
	items, _ = items.Get("items")
	var order []string
	for _, it := range items.Elems() {
		if strMember(it, "kind") == "active-work" {
			order = append(order, strMember(it, "stepId")+":"+strMember(it, "readiness"))
		}
	}
	// s8 unblocks 11 open steps; the other ready steps unblock none and
	// keep plan order; blocked steps follow in plan order.
	want := "s8:ready,s9:ready,s11:ready,s12:ready,s14:ready,s15:ready,s17:ready,s18:ready,s20:ready,s21:ready,s23:ready,s24:ready," +
		"s26:ready,s27:ready,s29:ready,s30:ready,s33:ready,s35:ready,s36:ready," +
		"s10:blocked,s13:blocked,s16:blocked,s19:blocked,s22:blocked,s25:blocked,s28:blocked,s31:blocked,s32:blocked,s34:blocked,s37:blocked,s38:blocked"
	if strings.Join(order, ",") != want {
		t.Fatalf("order\n got %s\nwant %s", strings.Join(order, ","), want)
	}
	if numMember(items.Elems()[0], "unblocks") != 11 {
		t.Fatalf("s8 unblocks %d", numMember(items.Elems()[0], "unblocks"))
	}
	// s10 is blocked by the current step and nothing else: the completed
	// p1/s4 is met.
	for _, it := range items.Elems() {
		if strMember(it, "stepId") == "s13" {
			bb, _ := it.Get("blockedBy")
			if got := string(ojson.Compact(bb)); got != `[{"phaseId":"p3","stepId":"s10","status":"draft"},{"phaseId":"p2","stepId":"s8","status":"draft"}]` {
				t.Fatalf("s13 blockedBy %s", got)
			}
		}
		if strMember(it, "stepId") == "s10" {
			bb, _ := it.Get("blockedBy")
			if len(bb.Elems()) != 1 || strMember(bb.Elems()[0], "stepId") != "s7" {
				t.Fatalf("s10 blockedBy %s", ojson.Compact(bb))
			}
		}
	}
	checkReadyOrder(t, "64000", v, 8)
	if ojson.UTF16Len(text) > max {
		t.Fatal("over budget")
	}
	// The graph changes the order only: every open step but the current
	// one is on the page exactly once, and the totals count them.
	s, err := e.load("roadmap")
	if err != nil {
		t.Fatal(err)
	}
	open := map[string]bool{}
	for _, ph := range s.Plan.Phases {
		for _, st := range ph.Steps {
			if isActive(st.Status) {
				open[st.ID] = true
			}
		}
	}
	onPage := map[string]bool{}
	for _, it := range items.Elems() {
		if id := strMember(it, "stepId"); strMember(it, "kind") == "active-work" {
			if onPage[id] || !open[id] || id == "s7" {
				t.Fatalf("page item %s is not an open step listed once", id)
			}
			onPage[id] = true
		}
	}
	if len(onPage) != len(open)-1 || numMember(v, "counts", "activeWorkTotal") != len(open) ||
		numMember(v, "page", "total") != numMember(v, "page", "returned") {
		t.Fatalf("totals: %d on the page, %d open, activeWorkTotal %d, page %d of %d", len(onPage), len(open),
			numMember(v, "counts", "activeWorkTotal"), numMember(v, "page", "returned"), numMember(v, "page", "total"))
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("resume wrote: %v", d)
	}
}

// TestResumeGraphBudgets pages the graph roadmap at a range of budgets:
// every page fits, keeps the readability invariants and the ready-first
// order, and the default budget still returns a useful page.
func TestResumeGraphBudgets(t *testing.T) {
	e, _ := seedGraphRoadmap(t)
	for _, b := range []int{64000, 20000, 12000, 8000, 6000, 4096} {
		for _, limit := range []int{7, 20} {
			b, limit := b, limit
			in := input.ResumeInput{ID: "roadmap", MaxChars: &b, Limit: &limit}
			seen, pages, first := 0, 0, 0
			for page := 0; page < 200; page++ {
				v, text, err := e.Resume(in)
				if err != nil {
					t.Fatalf("budget %d: %v", b, err)
				}
				if ojson.UTF16Len(text) > b {
					t.Fatalf("budget %d over", b)
				}
				label := fmt.Sprintf("budget %d limit %d page %d", b, limit, page)
				if r := pageReturned(v); r > 1 {
					checkReadable(t, label, v)
				}
				checkTargetOrder(t, label, v, b, limit)
				checkReadyOrder(t, label, v, resume.ListCap(b))
				if page == 0 {
					first = pageReturned(v)
				}
				seen += pageReturned(v)
				pages++
				next, _ := v.Get("page")
				next, _ = next.Get("nextCursor")
				if next.Kind() != ojson.String {
					if seen != numMember(v, "page", "total") {
						t.Fatalf("%s: paged %d of %d", label, seen, numMember(v, "page", "total"))
					}
					break
				}
				c := next.Str()
				in.Cursor = &c
			}
			t.Logf("budget %5d limit %2d: first page %d items, %d pages", b, limit, first, pages)
			if b >= 12000 && first < 4 {
				t.Fatalf("budget %d: first page only %d items", b, first)
			}
		}
	}
}

// TestOrderWarnings: out-of-order status changes succeed with a
// warning; validate and doctor report the violation without failing.
func TestOrderWarnings(t *testing.T) {
	e, root := seedGraphRoadmap(t)
	p, _ := ojson.Parse([]byte(`{"id":"roadmap","updateSteps":[{"phaseId":"p4","stepId":"s13","status":"completed"}]}`))
	auth := &countingAuth{}
	out, err := runMutation(context.Background(), e, "workplan_update", p.Value, auth)
	if err != nil || auth.n != 1 {
		t.Fatalf("order checks must never refuse: %v (auth %d)", err, auth.n)
	}
	w, _ := out.Value.Get("warnings")
	if ws := strs(w); len(ws) != 1 || !strings.Contains(ws[0], "Order warning: step p4/s13 was set to completed while its prerequisites are not completed: p3/s10 (draft), p2/s8 (draft)") {
		t.Fatalf("update warnings %v", ws)
	}
	s, _ := e.load("roadmap")
	if s.Plan.Phases[4].Steps[0].Status != "completed" {
		t.Fatal("status not applied")
	}
	v, _ := e.Validate(input.ValidateInput{ID: "roadmap"})
	valid, _ := v.Get("valid")
	vw, _ := v.Get("warnings")
	if !valid.Bool() || !containsPrefix(strs(vw), "dependencies: Order warning: step p4/s13 is completed but its prerequisites are not completed: p3/s10 (draft), p2/s8 (draft)") {
		t.Fatalf("validate valid=%v warnings %v", valid.Bool(), strs(vw))
	}
	id := "roadmap"
	d, _ := e.Doctor(input.DoctorInput{ID: &id})
	pl, _ := d.Get("plans")
	pw, _ := pl.Elems()[0].Get("warnings")
	pv, _ := pl.Elems()[0].Get("valid")
	pi, _ := pl.Elems()[0].Get("issues")
	// Order warnings never change valid: it is false here only because
	// the checkpoint went stale with the update.
	if pv.Bool() != (len(pi.Elems()) == 0) || containsPrefix(strs(pi), "Order warning") || !containsPrefix(strs(pw), "Order warning: step p4/s13") {
		t.Fatalf("doctor valid=%v issues %v warnings %v", pv.Bool(), strs(pi), strs(pw))
	}
	// review warns too.
	out = mustMutate(t, e, "workplan_update", `{"id":"roadmap","updateSteps":[{"phaseId":"p6","stepId":"s19","status":"review"}]}`)
	w, _ = out.Value.Get("warnings")
	if ws := strs(w); len(ws) != 1 || !strings.Contains(ws[0], "step p6/s19 was set to review while its prerequisites are not completed: p5/s16 (draft)") {
		t.Fatalf("review warnings %v", ws)
	}
	v, _ = e.Validate(input.ValidateInput{ID: "roadmap"})
	vw, _ = v.Get("warnings")
	if !containsPrefix(strs(vw), "Order warning: step p6/s19 is review") {
		t.Fatalf("validate review %v", strs(vw))
	}
	mustMutate(t, e, "workplan_update", `{"id":"roadmap","updateSteps":[{"phaseId":"p6","stepId":"s19","status":"blocked"}]}`)
	// in_progress warns too; a step whose prerequisites are met does not.
	out = mustMutate(t, e, "workplan_update", `{"id":"roadmap","updateSteps":[{"phaseId":"p3","stepId":"s10","status":"in_progress"},{"phaseId":"p2","stepId":"s9","status":"in_progress"}]}`)
	w, _ = out.Value.Get("warnings")
	if ws := strs(w); len(ws) != 1 || !strings.Contains(ws[0], "step p3/s10 was set to in_progress") {
		t.Fatalf("in_progress warnings %v", ws)
	}
	// Setting the prerequisites complete clears nothing retroactively in
	// the update result, but validate no longer reports s10.
	out = mustMutate(t, e, "workplan_update", `{"id":"roadmap","updateSteps":[{"phaseId":"p2","stepId":"s7","status":"completed"}]}`)
	if _, ok := out.Value.Get("warnings"); ok {
		t.Fatal("completing a ready step must not warn")
	}
	v, _ = e.Validate(input.ValidateInput{ID: "roadmap"})
	vw, _ = v.Get("warnings")
	if containsPrefix(strs(vw), "step p3/s10 is") {
		t.Fatalf("met prerequisites still reported: %v", strs(vw))
	}
	_ = root
}

// TestCancelledPrerequisite: steps behind a cancelled prerequisite are
// flagged, including through archived terminal summaries.
func TestCancelledPrerequisite(t *testing.T) {
	e, root := seedGraphRoadmap(t)
	out := mustMutate(t, e, "workplan_update", `{"id":"roadmap","updateSteps":[{"phaseId":"p5","stepId":"s16","status":"cancelled"}]}`)
	w, _ := out.Value.Get("warnings")
	if ws := strs(w); len(ws) != 1 || !strings.Contains(ws[0], "Step p5/s16 was cancelled; its dependents are now blocked by a cancelled prerequisite: p6/s19, p7/s22, p10/s32") {
		t.Fatalf("cancel warnings %v", ws)
	}
	v, _ := e.Validate(input.ValidateInput{ID: "roadmap"})
	vw := strs(func() ojson.Value { x, _ := v.Get("warnings"); return x }())
	for _, d := range []string{"p6/s19", "p7/s22", "p10/s32"} {
		if !containsPrefix(vw, "dependencies: Step "+d+" is blocked by cancelled prerequisite p5/s16") {
			t.Fatalf("validate lacks %s: %v", d, vw)
		}
	}
	if valid, _ := v.Get("valid"); !valid.Bool() {
		t.Fatal("cancelled prerequisites must not invalidate the plan")
	}
	max, limit := 64000, 100
	r, _, err := e.Resume(input.ResumeInput{ID: "roadmap", MaxChars: &max, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	var sw []string
	for _, x := range strs(func() ojson.Value { x, _ := r.Get("safety"); x, _ = x.Get("unverifiedWarnings"); return x }()) {
		if strings.HasPrefix(x, "Dependency order warning: ") {
			sw = append(sw, x)
		}
	}
	if len(sw) != 3 || !strings.HasPrefix(sw[0], "Dependency order warning: Step p6/s19 is blocked by cancelled prerequisite p5/s16") {
		t.Fatalf("resume warnings %v", sw)
	}
	items, _ := r.Get("page")
	items, _ = items.Get("items")
	for _, it := range items.Elems() {
		if strMember(it, "stepId") == "s22" {
			bb, _ := it.Get("blockedBy")
			if string(ojson.Compact(bb)) != `[{"phaseId":"p5","stepId":"s16","status":"cancelled"}]` {
				t.Fatalf("s22 blockedBy %s", ojson.Compact(bb))
			}
		}
	}

	// Archived prerequisites resolve through terminalSummaries: a
	// cancelled summary blocks, a completed one is met.
	small := testutil.NewRoot(t, "empty-workspace")
	se := mustEngine(t, small.Path)
	mustMutate(t, se, "workplan_create", `{"id":"arch","goal":"g","phases":[{"id":"b","title":"B","steps":[{"id":"b1","title":"B1"},{"id":"b2","title":"B2"}]}]}`)
	dep := `{"schemaVersion":1,"id":"arch","updatedAt":"2026-01-01T00:00:00.000Z","dependencies":[
		{"phaseId":"b","stepId":"b1","dependsOn":[{"phaseId":"a","stepId":"a1"}]},
		{"phaseId":"b","stepId":"b2","dependsOn":[{"phaseId":"a","stepId":"a2"}]}],
		"terminalSummaries":[{"phaseId":"a","stepId":"a1","title":"A1","status":"cancelled"},{"phaseId":"a","stepId":"a2","title":"A2","status":"completed"}]}`
	if err := os.WriteFile(filepath.Join(small.Path, ".opencode", "workplan", "arch.dependencies.json"), []byte(dep), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ = se.Validate(input.ValidateInput{ID: "arch"})
	vw = strs(func() ojson.Value { x, _ := v.Get("warnings"); return x }())
	if len(vw) != 1 || !strings.HasPrefix(vw[0], "dependencies: Step b/b1 is blocked by cancelled prerequisite a/a1") {
		t.Fatalf("terminal summary warnings %v", vw)
	}
	r, _, _ = se.Resume(input.ResumeInput{ID: "arch", MaxChars: &max, Limit: &limit})
	cur, _ := r.Get("checkpoint")
	cur, _ = cur.Get("current")
	if strMember(cur, "stepId") != "b1" || strMember(cur, "readiness") != "blocked" {
		t.Fatalf("current %s", ojson.Compact(cur))
	}
	items, _ = r.Get("page")
	items, _ = items.Get("items")
	if first := items.Elems()[0]; strMember(first, "stepId") != "b2" || strMember(first, "readiness") != "ready" {
		t.Fatalf("archived completed prerequisite must be met: %s", ojson.Compact(first))
	}
	_ = root
}

// TestPhaseReplacement: replacing phases re-validates the stored
// sidecar in the same prepared write.
func TestPhaseReplacement(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "empty-workspace")
	e := mustEngine(t, root.Path)
	mustMutate(t, e, "workplan_create", `{"id":"rep","goal":"g","phases":[
		{"id":"a","title":"A","steps":[{"id":"a1","title":"A1"},{"id":"a2","title":"A2"}]},
		{"id":"b","title":"B","steps":[{"id":"b1","title":"B1"}]}]}`)
	mustMutate(t, e, "workplan_update", `{"id":"rep","dependencies":[{"phaseId":"b","stepId":"b1","dependsOn":[{"phaseId":"a","stepId":"a2"}]}]}`)
	before := testutil.Fingerprint(t, root.Path)
	for _, in := range []struct{ input, want string }{
		{`{"id":"rep","phases":[{"id":"a","title":"A","steps":[{"id":"a1","title":"A1"}]},{"id":"b","title":"B","steps":[{"id":"b1","title":"B1"}]}]}`,
			"Invalid dependency metadata: dependencies.0.dependsOn.0: Step a/a2 does not exist; the phase replacement would leave these dependency links dangling. Replace the dependencies in the same update."},
		{`{"id":"rep","phases":[{"id":"a","title":"A","steps":[{"id":"a1","title":"A1"},{"id":"a2","title":"A2"}]}]}`,
			"Invalid dependency metadata: dependencies.0: Source step b/b1 does not exist; the phase replacement would leave these dependency links dangling. Replace the dependencies in the same update."},
	} {
		p, _ := ojson.Parse([]byte(in.input))
		auth := &countingAuth{}
		_, err := runMutation(context.Background(), e, "workplan_update", p.Value, auth)
		if err == nil || err.Error() != in.want || ErrorClass(err) != "invalid_structure" {
			t.Fatalf("got %v (class %s)\nwant %s", err, ErrorClass(err), in.want)
		}
		if auth.n != 0 {
			t.Fatal("refusal must come before authorization")
		}
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("refusal wrote: %v", d)
	}
	// Keeping every linked step, or replacing the dependencies in the same
	// call, is accepted.
	mustMutate(t, e, "workplan_update", `{"id":"rep","phases":[{"id":"a","title":"A2 first","steps":[{"id":"a2","title":"A2"},{"id":"a1","title":"A1"}]},{"id":"b","title":"B","steps":[{"id":"b1","title":"B1"}]}]}`)
	mustMutate(t, e, "workplan_update", `{"id":"rep","phases":[{"id":"a","title":"A","steps":[{"id":"a1","title":"A1"}]}],"dependencies":[]}`)
}

// TestDependencyWrites: empty entries are refused, backward links
// warn, compaction preview lists dependents of archived steps, and
// inspect shows prerequisites and dependents.
func TestDependencyWrites(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "empty-workspace")
	e := mustEngine(t, root.Path)
	mustMutate(t, e, "workplan_create", `{"id":"dw","goal":"g","phases":[
		{"id":"a","title":"A","status":"completed","steps":[{"id":"a1","title":"A1","action":"x","validation":"y","status":"completed"},{"id":"a2","title":"A2","action":"x","validation":"y","status":"completed"}]},
		{"id":"b","title":"B","steps":[{"id":"b1","title":"B1"},{"id":"b2","title":"B2"}]}]}`)
	p, _ := ojson.Parse([]byte(`{"id":"dw","dependencies":[{"phaseId":"b","stepId":"b1","dependsOn":[]}]}`))
	auth := &countingAuth{}
	_, err := runMutation(context.Background(), e, "workplan_update", p.Value, auth)
	if err == nil || err.Error() != "Invalid dependency metadata: dependencies.0.dependsOn: Dependency entry must list at least one prerequisite" || auth.n != 0 || ErrorClass(err) != "invalid_structure" {
		t.Fatalf("empty dependsOn: %v (auth %d)", err, auth.n)
	}
	// Existing dependency refusals (dangling target, cycle) share the
	// class; their text is unchanged.
	for _, in := range []string{
		`{"id":"dw","dependencies":[{"phaseId":"b","stepId":"b1","dependsOn":[{"phaseId":"ghost","stepId":"x"}]}]}`,
		`{"id":"dw","dependencies":[{"phaseId":"b","stepId":"b1","dependsOn":[{"phaseId":"b","stepId":"b2"}]},{"phaseId":"b","stepId":"b2","dependsOn":[{"phaseId":"b","stepId":"b1"}]}]}`,
	} {
		p, _ := ojson.Parse([]byte(in))
		_, err := runMutation(context.Background(), e, "workplan_update", p.Value, &countingAuth{})
		if err == nil || !strings.HasPrefix(err.Error(), "Invalid dependency metadata: dependencies") || ErrorClass(err) != "invalid_structure" {
			t.Fatalf("dependency refusal %v (class %s)", err, ErrorClass(err))
		}
	}
	out := mustMutate(t, e, "workplan_update", `{"id":"dw","dependencies":[
		{"phaseId":"b","stepId":"b1","dependsOn":[{"phaseId":"b","stepId":"b2"},{"phaseId":"a","stepId":"a2"}]}]}`)
	w, _ := out.Value.Get("warnings")
	if ws := strs(w); len(ws) != 1 || ws[0] != "dependencies.0.dependsOn.0: Backward link: b/b1 depends on b/b2, which comes later in plan order." {
		t.Fatalf("backward warnings %v", ws)
	}
	v, _ := e.Validate(input.ValidateInput{ID: "dw"})
	if vw, _ := v.Get("warnings"); !containsPrefix(strs(vw), "Backward link: b/b1 depends on b/b2") {
		t.Fatalf("validate %v", strs(vw))
	}
	// Inspect: prerequisites (with status) and dependents per step.
	ins, err := e.Inspect(input.InspectInput{ID: "dw"})
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := ins.Get("steps")
	byID := map[string]ojson.Value{}
	for _, st := range steps.Elems() {
		byID[strMember(st, "id")] = st
	}
	pre, _ := byID["b1"].Get("prerequisites")
	if string(ojson.Compact(pre)) != `[{"phaseId":"b","stepId":"b2","status":"draft"},{"phaseId":"a","stepId":"a2","status":"completed"}]` {
		t.Fatalf("b1 prerequisites %s", ojson.Compact(pre))
	}
	dep, _ := byID["a2"].Get("dependents")
	if string(ojson.Compact(dep)) != `[{"phaseId":"b","stepId":"b1"}]` {
		t.Fatalf("a2 dependents %s", ojson.Compact(dep))
	}
	if _, ok := byID["a2"].Get("readiness"); ok {
		t.Fatal("closed steps carry no readiness")
	}
	if strMember(byID["b1"], "readiness") != "blocked" || numMember(byID["b2"], "unblocks") != 1 {
		t.Fatalf("b1/b2 %s %s", ojson.Compact(byID["b1"]), ojson.Compact(byID["b2"]))
	}
	cp, _ := ins.Get("criticalPath")
	if numMember(cp, "length") != 2 {
		t.Fatalf("criticalPath %s", ojson.Compact(cp))
	}
	// Compaction preview: archiving phase a lists b/b1 behind a/a2.
	pv, err := e.CompactPreview(parseT(t, `{"id":"dw","archiveReason":"tidy","completedPhaseIds":["a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	ap, ok := pv.Get("archivedPrerequisites")
	if !ok || string(ojson.Compact(ap)) != `[{"phaseId":"a","stepId":"a2","status":"completed","dependents":[{"phaseId":"b","stepId":"b1"}]}]` {
		t.Fatalf("archivedPrerequisites %s", ojson.Compact(ap))
	}
	// archivedPrerequisites is the only addition: when no remaining step
	// depends on the archived phase, the same preview has every other
	// member in the same order.
	mustMutate(t, e, "workplan_update", `{"id":"dw","dependencies":[{"phaseId":"b","stepId":"b1","dependsOn":[{"phaseId":"b","stepId":"b2"}]}]}`)
	plain, err := e.CompactPreview(parseT(t, `{"id":"dw","archiveReason":"tidy","completedPhaseIds":["a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	keys := func(v ojson.Value) string {
		var out []string
		for _, m := range v.Members() {
			out = append(out, m.Key)
		}
		return strings.Join(out, ",")
	}
	if testutil.Has(plain, "archivedPrerequisites") || keys(plain) != keys(testutil.Without(pv, "archivedPrerequisites")) {
		t.Fatalf("preview members %s, without dependents %s", keys(pv), keys(plain))
	}
}

func parseT(t *testing.T, s string) ojson.Value {
	t.Helper()
	p, err := ojson.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return p.Value
}

// TestCriticalPath on the roadmap graph: inspect and doctor
// show the same advisory path; slack is zero on it.
func TestCriticalPath(t *testing.T) {
	e, root := seedGraphRoadmap(t)
	before := testutil.Fingerprint(t, root.Path)
	limit := 500
	ins, err := e.Inspect(input.InspectInput{ID: "roadmap", Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	cp, _ := ins.Get("criticalPath")
	var path []string
	ps, _ := cp.Get("steps")
	for _, s := range ps.Elems() {
		path = append(path, strMember(s, "stepId"))
	}
	if strings.Join(path, ",") != "s7,s10,s13,s16,s19,s25,s28,s31,s34,s37,s38" || numMember(cp, "length") != 11 {
		t.Fatalf("critical path %v", path)
	}
	if _, ok := cp.Get("estimate"); ok {
		t.Fatal("no estimates: no estimate member")
	}
	id := "roadmap"
	d, _ := e.Doctor(input.DoctorInput{ID: &id})
	pl, _ := d.Get("plans")
	dcp, _ := pl.Elems()[0].Get("criticalPath")
	if string(ojson.Compact(dcp)) != string(ojson.Compact(cp)) {
		t.Fatal("doctor and inspect disagree")
	}
	steps, _ := ins.Get("steps")
	slack := map[string]string{}
	for _, st := range steps.Elems() {
		if x, ok := st.Get("slack"); ok {
			slack[strMember(st, "id")] = x.NumberLiteral()
		}
	}
	for id, want := range map[string]string{"s7": "0", "s22": "0", "s32": "4", "s9": "10", "s8": "1"} {
		if slack[id] != want {
			t.Fatalf("slack %s = %s want %s", id, slack[id], want)
		}
	}
	// No sidecar: no graph members anywhere.
	plain := testutil.NewRoot(t, "minimal-valid")
	pe := mustEngine(t, plain.Path)
	pi, _ := pe.Inspect(input.InspectInput{ID: "minimal"})
	if _, ok := pi.Get("criticalPath"); ok {
		t.Fatal("criticalPath without a sidecar")
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("reads wrote: %v", d)
	}
}

// TestResumeCriticalPath: resume carries the compact critical path only
// with a valid sidecar and a path of two or more open steps; otherwise its
// bytes equal the packet without it.
func TestResumeCriticalPath(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	v, _, err := e.Resume(input.ResumeInput{ID: "full-plan"})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := v.Get("criticalPath")
	if !ok {
		t.Fatal("no criticalPath")
	}
	insp, _ := e.Inspect(input.InspectInput{ID: "full-plan"})
	full, _ := insp.Get("criticalPath")
	steps, _ := full.Get("steps")
	l, _ := c.Get("length")
	fl, _ := full.Get("length")
	if l.NumberLiteral() != fl.NumberLiteral() || strMember(c, "nextStep", "stepId") != strMember(steps.Elems()[0], "stepId") {
		t.Fatalf("resume %s, inspect %s", ojson.Compact(c), ojson.Compact(full))
	}
	if len(c.Members()) != 2 {
		t.Fatalf("resume critical path must stay compact: %s", ojson.Compact(c))
	}
	// Without a sidecar the packet is byte-identical to the packet
	// without the critical path and the stale diagnostic.
	for _, fx := range []string{"minimal-valid", "large-paging", "resume-stress"} {
		r := testutil.NewRoot(t, fx)
		e := mustEngine(t, r.Path)
		ls, _ := e.List(input.ListInput{})
		ws, _ := ls.Get("workplans")
		for _, w := range ws.Elems() {
			id := strMember(w, "id")
			for _, max := range []int{4096, 12000} {
				in := input.ResumeInput{ID: id, MaxChars: &max}
				_, a, err := e.Resume(in)
				_, b := resumeEdited(t, e, in, withoutAdvisory)
				if err != nil || a != b {
					t.Fatalf("%s/%s at %d differs without a sidecar", fx, id, max)
				}
			}
		}
	}
}

// withoutAdvisory is the packet model without the critical path and the
// stale checkpoint diagnostic.
func withoutAdvisory(m *resume.Packet) {
	m.Critical = nil
	if m.Freshness == FreshnessStale {
		m.Diagnostic = nil
	}
}
