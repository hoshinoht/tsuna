package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func mutateErr(t *testing.T, e *Engine, tool, input string) (Output, error, int) {
	t.Helper()
	auth := &countingAuth{}
	out, err := runMutation(context.Background(), e, tool, parseT(t, input), auth)
	return out, err, auth.n
}

// wipeToken runs the read-only wipe preview and returns its token.
func wipeToken(t *testing.T, e *Engine, id, hash, extra string) string {
	t.Helper()
	out, err, n := mutateErr(t, e, "workplan_reset", `{"id":"`+id+`","mode":"wipe","expectedHash":"`+hash+`"`+extra+`}`)
	if err != nil {
		t.Fatalf("wipe preview: %v", err)
	}
	if n != 0 {
		t.Fatalf("wipe preview asked for authorization")
	}
	return strMember(out.Value, "previewToken")
}

func readHashT(t *testing.T, e *Engine, id string) string {
	t.Helper()
	v, err := e.Validate(input.ValidateInput{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	return strMember(v, "stateHash")
}

// seedActiveRoadmap is the synthetic roadmap with statuses in progress,
// the dependency graph, a fresh checkpoint, findings and notes.
func seedActiveRoadmap(t *testing.T) (*Engine, testutil.Root) {
	t.Helper()
	e, root := seedGraphRoadmap(t)
	mustMutate(t, e, "workplan_update", `{"id":"roadmap","status":"in_progress","updatePhases":[{"phaseId":"p2","status":"in_progress"}],
		"updateSteps":[{"phaseId":"p2","stepId":"s7","status":"in_progress"},{"phaseId":"p2","stepId":"s8","status":"review"}],
		"addReviewFindings":[{"severity":"major","title":"Open finding","status":"open"},{"severity":"note","title":"Done finding","status":"resolved"}],
		"appendNotes":["n3 kept"]}`)
	mustMutate(t, e, "workplan_checkpoint", `{"id":"roadmap","summary":"s","nextAction":"n","phaseId":"p2","stepId":"s7"}`)
	return e, root
}

func planFile(t *testing.T, root, id string) *model.Plan {
	t.Helper()
	s, err := snapshot.Load(root, id, snapshot.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return s.Plan
}

// TestDraftResetKeepsStructure: a draft reset changes statuses only.
func TestDraftResetKeepsStructure(t *testing.T) {
	e, root := seedActiveRoadmap(t)
	wp := filepath.Join(root.Path, ".opencode", "workplan")
	before := planFile(t, root.Path, "roadmap")
	depsBefore, _ := os.ReadFile(filepath.Join(wp, "roadmap.dependencies.json"))
	out := mustMutate(t, e, "workplan_reset", `{"id":"roadmap","expectedHash":"`+readHashT(t, e, "roadmap")+`"}`)
	if strMember(out.Value, "mode") != "draft" {
		t.Fatalf("mode %s", strMember(out.Value, "mode"))
	}
	if v, _ := out.Value.Get("checkpointRemoved"); !v.Bool() {
		t.Fatal("checkpointRemoved not reported")
	}
	after := planFile(t, root.Path, "roadmap")
	if after.Status != "draft" || len(after.Phases) != len(before.Phases) || after.StepCount() != before.StepCount() {
		t.Fatalf("structure changed: %d phases, %d steps", len(after.Phases), after.StepCount())
	}
	for i := range after.Phases {
		if after.Phases[i].ID != before.Phases[i].ID || after.Phases[i].Status != "draft" {
			t.Fatalf("phase %d: %s %s", i, after.Phases[i].ID, after.Phases[i].Status)
		}
		for j := range after.Phases[i].Steps {
			st, bst := after.Phases[i].Steps[j], before.Phases[i].Steps[j]
			if st.Status != "draft" || st.ID != bst.ID || *st.Action != *bst.Action || *st.Validation != *bst.Validation {
				t.Fatalf("step %s/%s not kept with status draft", after.Phases[i].ID, st.ID)
			}
		}
	}
	if strings.Join(after.Notes, "|") != strings.Join(before.Notes, "|") || len(after.Findings) != len(before.Findings) ||
		strings.Join(after.Scope, "|") != strings.Join(before.Scope, "|") || strings.Join(after.Constraints, "|") != strings.Join(before.Constraints, "|") {
		t.Fatal("notes/findings/scope/constraints not kept")
	}
	if _, err := os.Stat(filepath.Join(wp, "roadmap.checkpoint.json")); !os.IsNotExist(err) {
		t.Fatal("checkpoint not removed")
	}
	depsAfter, _ := os.ReadFile(filepath.Join(wp, "roadmap.dependencies.json"))
	if string(depsAfter) != string(depsBefore) {
		t.Fatal("dependency sidecar changed")
	}
	v, _ := e.Validate(input.ValidateInput{ID: "roadmap"})
	if valid, _ := v.Get("valid"); !valid.Bool() {
		t.Fatalf("plan invalid after draft reset: %s", ojson.Compact(v))
	}
	if strings.Contains(string(ojson.Compact(v)), "dependencies:") {
		t.Fatalf("dependency sidecar is not consistent: %s", ojson.Compact(v))
	}
	if strMember(v, "stateHash") == "" || !strings.Contains(string(ojson.Compact(v)), `"dependenciesRecorded":true`) {
		t.Fatal("dependencies not recorded")
	}
	// Generated Markdown follows the JSON.
	s, _ := snapshot.Load(root.Path, "roadmap", snapshot.DefaultLimits)
	if gen, _ := e.generatedMarkdown(s); !gen {
		t.Fatal("Markdown not regenerated")
	}

	// Handwritten Markdown is kept unless replaceMarkdown=true.
	md := filepath.Join(wp, "roadmap.md")
	os.WriteFile(md, []byte("# Handwritten roadmap\n"), 0o644)
	mustMutate(t, e, "workplan_update", `{"id":"roadmap","status":"in_progress"}`)
	mustMutate(t, e, "workplan_reset", `{"id":"roadmap"}`)
	if data, _ := os.ReadFile(md); string(data) != "# Handwritten roadmap\n" {
		t.Fatal("handwritten Markdown replaced without replaceMarkdown")
	}
	mustMutate(t, e, "workplan_reset", `{"id":"roadmap","replaceMarkdown":true}`)
	s, _ = snapshot.Load(root.Path, "roadmap", snapshot.DefaultLimits)
	if gen, _ := e.generatedMarkdown(s); !gen {
		t.Fatal("replaceMarkdown did not regenerate")
	}
}

// TestWipePreviewConfirm: preview, exact token and
// confirmation, archive first, sidecars removed in the same transaction.
func TestWipePreviewConfirm(t *testing.T) {
	e, root := seedActiveRoadmap(t)
	wp := filepath.Join(root.Path, ".opencode", "workplan")
	orig := map[string][]byte{}
	for _, n := range []string{"roadmap.json", "roadmap.md", "roadmap.checkpoint.json", "roadmap.dependencies.json"} {
		orig[n], _ = os.ReadFile(filepath.Join(wp, n))
	}
	sh := readHashT(t, e, "roadmap")
	fp := testutil.Fingerprint(t, root.Path)
	pv, err, n := mutateErr(t, e, "workplan_reset", `{"id":"roadmap","mode":"wipe","expectedHash":"`+sh+`"}`)
	if err != nil || n != 0 {
		t.Fatalf("preview: %v (authorizations %d)", err, n)
	}
	if d := testutil.DiffFingerprints(fp, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("preview changed files: %v", d)
	}
	tok := strMember(pv.Value, "previewToken")
	if !strings.HasPrefix(tok, "v1-") || strMember(pv.Value, "confirmationRequiredForApply") != "WIPE_PLAN_CONTENT" {
		t.Fatalf("preview %s", ojson.Compact(pv.Value))
	}
	if numMember(pv.Value, "removals", "phaseCount") != 13 || numMember(pv.Value, "removals", "stepCount") != 38 || numMember(pv.Value, "removals", "noteCount") != 3 {
		t.Fatalf("removals %s", ojson.Compact(pv.Value))
	}
	dels := strs(func() ojson.Value { v, _ := pv.Value.Get("writeIntent"); d, _ := v.Get("deletePaths"); return d }())
	if len(dels) != 2 || !strings.HasSuffix(dels[0], "roadmap.checkpoint.json") || !strings.HasSuffix(dels[1], "roadmap.dependencies.json") {
		t.Fatalf("deletePaths %v", dels)
	}
	base := `{"id":"roadmap","mode":"wipe","expectedHash":"` + sh + `"`
	for _, tc := range []struct{ input, want string }{
		{base + `,"previewToken":"` + tok + `"}`, "Wipe apply requires confirmation=WIPE_PLAN_CONTENT"},
		{base + `,"previewToken":"` + tok + `","confirmation":"yes"}`, "Wipe apply requires confirmation=WIPE_PLAN_CONTENT"},
		{base + `,"confirmation":"WIPE_PLAN_CONTENT"}`, "Wipe apply requires the previewToken"},
		{base + `,"previewToken":"v1-00","confirmation":"WIPE_PLAN_CONTENT"}`, "previewToken does not match"},
		{base + `,"previewToken":"` + tok + `","confirmation":"WIPE_PLAN_CONTENT","preserveNotes":true}`, "previewToken does not match"},
		{`{"id":"roadmap","previewToken":"` + tok + `","confirmation":"WIPE_PLAN_CONTENT"}`, "apply only to mode=wipe"},
	} {
		_, err, n := mutateErr(t, e, "workplan_reset", tc.input)
		if err == nil || !strings.Contains(err.Error(), tc.want) || n != 0 {
			t.Fatalf("%s: err %v (authorizations %d), want %q", tc.input, err, n, tc.want)
		}
	}
	if d := testutil.DiffFingerprints(fp, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("refusals changed files: %v", d)
	}
	// Native surface: the same rules are input errors.
	if _, err := input.ParseMutationInput("reset", parseT(t, `{"id":"roadmap","expectedHash":"`+sh+`","confirmation":"WIPE_PLAN_CONTENT"}`), input.SurfaceNative); err == nil ||
		!strings.Contains(err.Error(), "confirmation: previewToken and confirmation apply only to mode=wipe") {
		t.Fatalf("native refine: %v", err)
	}
	out, err, n := mutateErr(t, e, "workplan_reset", base+`,"previewToken":"`+tok+`","confirmation":"WIPE_PLAN_CONTENT"}`)
	if err != nil || n != 1 {
		t.Fatalf("apply: %v (authorizations %d)", err, n)
	}
	archive := strMember(out.Value, "archivePath")
	st, err := os.Stat(archive)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("archive %s: %v %v", archive, err, st)
	}
	var arch struct {
		ArchiveVersion int    `json:"archiveVersion"`
		WorkplanID     string `json:"workplanId"`
		Operation      string `json:"operation"`
		PreviewToken   string `json:"previewToken"`
		Removed        struct {
			Phases []json.RawMessage `json:"phases"`
			Notes  []string          `json:"notes"`
		} `json:"removed"`
		Source map[string]*string `json:"source"`
	}
	data, _ := os.ReadFile(archive)
	if err := json.Unmarshal(data, &arch); err != nil {
		t.Fatal(err)
	}
	if arch.ArchiveVersion != 1 || arch.WorkplanID != "roadmap" || arch.Operation != "reset:wipe" || arch.PreviewToken != tok || len(arch.Removed.Phases) != 13 || len(arch.Removed.Notes) != 3 {
		t.Fatalf("archive header %+v", arch)
	}
	for key, name := range map[string]string{"workplanJson": "roadmap.json", "linkedMarkdown": "roadmap.md", "checkpoint": "roadmap.checkpoint.json", "dependencies": "roadmap.dependencies.json"} {
		if arch.Source[key] == nil || *arch.Source[key] != string(orig[name]) {
			t.Fatalf("archive source %s is not the complete original", key)
		}
	}
	p := planFile(t, root.Path, "roadmap")
	if len(p.Phases) != 0 || len(p.Findings) != 0 || len(p.Notes) != 0 || p.Status != "draft" || len(p.Scope) != 6 || len(p.Constraints) != 8 {
		t.Fatalf("wiped plan: %d phases %d findings %d notes %s", len(p.Phases), len(p.Findings), len(p.Notes), p.Status)
	}
	for _, n := range []string{"roadmap.checkpoint.json", "roadmap.dependencies.json"} {
		if _, err := os.Stat(filepath.Join(wp, n)); !os.IsNotExist(err) {
			t.Fatalf("%s not removed", n)
		}
	}
	v, _ := e.Validate(input.ValidateInput{ID: "roadmap"})
	if strings.Contains(string(ojson.Compact(v)), "dependencies:") || !strings.Contains(string(ojson.Compact(v)), `"dependenciesRecorded":false`) {
		t.Fatalf("validate after wipe: %s", ojson.Compact(v))
	}
	// The same archive stays; a second wipe of the empty plan is a new token.
	if tok2 := wipeToken(t, e, "roadmap", readHashT(t, e, "roadmap"), ""); tok2 == tok {
		t.Fatal("token did not change with the state")
	}

	// preserveNotes keeps notes; handwritten Markdown needs replaceMarkdown.
	e2, root2 := seedActiveRoadmap(t)
	os.WriteFile(filepath.Join(root2.Path, ".opencode/workplan/roadmap.md"), []byte("# hand\n"), 0o644)
	sh2 := readHashT(t, e2, "roadmap")
	if _, err, _ := mutateErr(t, e2, "workplan_reset", `{"id":"roadmap","mode":"wipe","expectedHash":"`+sh2+`"}`); err == nil || !strings.Contains(err.Error(), "handwritten") {
		t.Fatalf("handwritten wipe preview: %v", err)
	}
	extra := `,"preserveNotes":true,"replaceMarkdown":true`
	tok2 := wipeToken(t, e2, "roadmap", sh2, extra)
	mustMutate(t, e2, "workplan_reset", `{"id":"roadmap","mode":"wipe","expectedHash":"`+sh2+`","previewToken":"`+tok2+`","confirmation":"WIPE_PLAN_CONTENT"`+extra+`}`)
	if p := planFile(t, root2.Path, "roadmap"); len(p.Notes) != 3 || len(p.Phases) != 0 {
		t.Fatalf("preserveNotes: %d notes, %d phases", len(p.Notes), len(p.Phases))
	}
}

// TestWipedNote: doctor explains an empty draft plan left
// by a wipe; validate keeps its issue.
func TestWipedNote(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	sh := stateHashFor(t, e, "full-plan")
	tok := wipeToken(t, e, "full-plan", sh, "")
	out := mustMutate(t, e, "workplan_reset", `{"id":"full-plan","mode":"wipe","expectedHash":"`+sh+`","previewToken":"`+tok+`","confirmation":"WIPE_PLAN_CONTENT"}`)
	arch := e.relPath(strMember(out.Value, "archivePath"))
	id := "full-plan"
	doc, _ := e.Doctor(input.DoctorInput{ID: &id})
	plans, _ := doc.Get("plans")
	w, _ := plans.Elems()[0].Get("warnings")
	if !containsPrefix(strs(w), wipedNeedle+"; the removed content is archived at "+arch+". Add phases") {
		t.Fatalf("warnings %v (archive %s)", strs(w), arch)
	}
	v, _ := e.Validate(input.ValidateInput{ID: "full-plan"})
	is, _ := v.Get("issues")
	if !containsPrefix(strs(is), "phases: At least one phase is required") {
		t.Fatalf("validate issues %v", strs(is))
	}
	// Adding a phase removes the note.
	mustMutate(t, e, "workplan_update", `{"id":"full-plan","addPhases":[{"phase":{"id":"p","title":"P","steps":[{"id":"s","title":"S","action":"a","validation":"v"}]}}]}`)
	doc, _ = e.Doctor(input.DoctorInput{ID: &id})
	plans, _ = doc.Get("plans")
	w, _ = plans.Elems()[0].Get("warnings")
	if containsPrefix(strs(w), wipedNeedle) {
		t.Fatal("note stays after phases were added")
	}
	// An empty draft plan without a wipe archive gets no note.
	root2 := testutil.NewRoot(t, "draft-empty")
	e2 := mustEngine(t, root2.Path)
	doc, _ = e2.Doctor(input.DoctorInput{})
	if strings.Contains(string(ojson.Compact(doc)), wipedNeedle) {
		t.Fatal("note without a wipe archive")
	}
}
