package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// TestMarkdownDrift is item C.
func TestMarkdownDrift(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	v, _ := e.Validate(input.ValidateInput{ID: "minimal"})
	if _, ok := v.Get("warnings"); ok {
		t.Fatal("generated Markdown must not warn")
	}
	md := filepath.Join(root.Path, ".opencode", "workplan", "minimal.md")
	data, _ := os.ReadFile(md)
	os.WriteFile(md, append(data, []byte("\nHand-written note.\n")...), 0o600)
	before := testutil.Fingerprint(t, root.Path)
	v, _ = e.Validate(input.ValidateInput{ID: "minimal"})
	valid, _ := v.Get("valid")
	w, ok := v.Get("warnings")
	if !valid.Bool() || !ok || len(w.Elems()) != 1 || !strings.Contains(w.Elems()[0].Str(), ".opencode/workplan/minimal.md") {
		t.Fatalf("validate: valid %v warnings %s", valid.Bool(), ojson.Compact(w))
	}
	id := "minimal"
	d, _ := e.Doctor(input.DoctorInput{ID: &id})
	plans, _ := d.Get("plans")
	pw, ok := plans.Elems()[0].Get("warnings")
	pv, _ := plans.Elems()[0].Get("valid")
	if !ok || len(pw.Elems()) != 1 || !pv.Bool() {
		t.Fatalf("doctor plan warnings %s valid %v", ojson.Compact(pw), pv.Bool())
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("diagnostics wrote: %v", d)
	}
}

// TestDoctorStrays is item F.
func TestDoctorStrays(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	dir := filepath.Join(root.Path, ".opencode", "workplan")
	for name, body := range map[string]string{
		"ghost.checkpoint.json":   "{}",
		"ghost.dependencies.json": "{}",
		"old-change.patch":        "*** Begin Patch\n",
		"scratch.md":              "# notes\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := mustEngine(t, root.Path)
	before := testutil.Fingerprint(t, root.Path)
	d, err := e.Doctor(input.DoctorInput{})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := d.Get("strayArtifacts")
	var got []string
	for _, x := range st.Elems() {
		n, _ := x.Get("name")
		k, _ := x.Get("kind")
		got = append(got, n.Str()+":"+k.Str())
	}
	want := "ghost.checkpoint.json:orphaned-sidecar,ghost.dependencies.json:orphaned-sidecar,old-change.patch:unclassified,scratch.md:unclassified"
	if strings.Join(got, ",") != want {
		t.Fatalf("strays %v", got)
	}
	w, _ := d.Get("warnings")
	if len(w.Elems()) != 4 || !strings.Contains(w.Elems()[0].Str(), "archive/") {
		t.Fatalf("warnings %s", ojson.Compact(w))
	}
	plans, _ := d.Get("plans")
	if v, _ := plans.Elems()[0].Get("valid"); !v.Bool() {
		t.Fatal("strays must not invalidate plans")
	}
	one := 1
	d, _ = e.Doctor(input.DoctorInput{Limit: &one})
	st, _ = d.Get("strayArtifacts")
	om, _ := d.Get("omittedStrayArtifacts")
	cnt, _ := d.Get("strayArtifactCount")
	if n, _ := om.Float(); len(st.Elems()) != 1 || n != 3 {
		t.Fatalf("limit: listed %d omitted %v", len(st.Elems()), n)
	}
	if n, _ := cnt.Float(); n != 4 {
		t.Fatalf("count %v", n)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("doctor moved or changed files: %v", d)
	}
	// A clean root reports no stray members at all.
	clean := testutil.NewRoot(t, "minimal-valid")
	d, _ = mustEngine(t, clean.Path).Doctor(input.DoctorInput{})
	for _, k := range []string{"strayArtifacts", "strayArtifactCount", "warnings"} {
		if _, ok := d.Get(k); ok {
			t.Fatalf("clean root has %s", k)
		}
	}
}

// TestStaleMarkdownCopy: the old <id>.md left by a planFile
// move is reported by doctor; nothing is moved or deleted.
func TestStaleMarkdownCopy(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	mustMutate(t, e, "workplan_update", `{"id":"minimal","planFile":".opencode/workplan/docs/minimal-plan.md"}`)
	os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/orphan-notes.md"), []byte("# notes\n"), 0o644)
	fp := testutil.Fingerprint(t, root.Path)
	doc, _ := e.Doctor(input.DoctorInput{})
	if d := testutil.DiffFingerprints(fp, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("doctor changed files: %v", d)
	}
	strays, _ := doc.Get("strayArtifacts")
	kinds := map[string]string{}
	for _, s := range strays.Elems() {
		kinds[strMember(s, "name")] = strMember(s, "kind")
	}
	if kinds["minimal.md"] != "stale-markdown" || kinds["orphan-notes.md"] != "unclassified" || len(kinds) != 2 {
		t.Fatalf("strays %v", kinds)
	}
	w, _ := doc.Get("warnings")
	if !containsPrefix(strs(w), "Stale Markdown copy .opencode/workplan/minimal.md: plan minimal now links .opencode/workplan/docs/minimal-plan.md") {
		t.Fatalf("warnings %v", strs(w))
	}
}

// TestStepMarkers: handwritten Markdown without step markers
// warns (never fails, never rewritten).
func TestStepMarkers(t *testing.T) {
	e, root := seedRoadmap(t)
	md := filepath.Join(root.Path, ".opencode/workplan/roadmap.md")
	v, _ := e.Validate(input.ValidateInput{ID: "roadmap"})
	if strings.Contains(string(ojson.Compact(v)), markerNeedle) {
		t.Fatal("generated Markdown warned")
	}
	data, _ := os.ReadFile(md)
	hand := strings.Replace(string(data), "<!-- workplan-step-id: s7 -->", "", 1)
	hand = strings.Replace(hand, "<!-- workplan-step-id: s9 -->", "", 1)
	os.WriteFile(md, []byte(hand), 0o644)
	v, _ = e.Validate(input.ValidateInput{ID: "roadmap"})
	w, _ := v.Get("warnings")
	if valid, _ := v.Get("valid"); !valid.Bool() || !containsPrefix(strs(w), "has no step marker (<!-- workplan-step-id: <stepId> -->) for 2 of 38 steps: p2/s7, p2/s9.") {
		t.Fatalf("validate: %s", ojson.Compact(v))
	}
	if got, _ := os.ReadFile(md); string(got) != hand {
		t.Fatal("Markdown rewritten")
	}
	// Doctor and patch validation carry the same warning.
	doc, _ := e.Doctor(input.DoctorInput{})
	if !strings.Contains(string(ojson.Compact(doc)), "for 2 of 38 steps") {
		t.Fatal("doctor lacks the marker warning")
	}
	// More than ten missing are counted, not listed.
	os.WriteFile(md, []byte("# roadmap\n"), 0o644)
	v, _ = e.Validate(input.ValidateInput{ID: "roadmap"})
	if !strings.Contains(string(ojson.Compact(v)), "for 38 of 38 steps: p0/s1") || !strings.Contains(string(ojson.Compact(v)), " and 28 more.") {
		t.Fatalf("validate: %s", ojson.Compact(v))
	}
}

// TestLegacyGeneratedMarkdown: Markdown generated with the
// old finding rendering ("title(status)", as the reference and earlier
// stages wrote it) is still generated: no drift warning, and writes
// refresh it to the new rendering instead of refusing it as handwritten.
func TestLegacyGeneratedMarkdown(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	mdPath := filepath.Join(root.Path, ".opencode/workplan/full-plan.md")
	old, _ := os.ReadFile(mdPath)
	if !strings.Contains(string(old), "] Blocker finding(open)") {
		t.Fatal("fixture Markdown is not in the old rendering")
	}
	s, _ := snapshot.Load(root.Path, "full-plan", snapshot.DefaultLimits)
	if gen, err := e.generatedMarkdown(s); err != nil || !gen {
		t.Fatalf("old rendering not generated: %v %v", gen, err)
	}
	if gen, present, err := e.markdownGenerated("full-plan"); err != nil || !gen || !present {
		t.Fatal("MarkdownGenerated misclassifies the old rendering")
	}
	v, _ := e.Validate(input.ValidateInput{ID: "full-plan"})
	if testutil.Has(v, "warnings") {
		t.Fatalf("drift warning on the old rendering: %s", ojson.Compact(v))
	}
	// An update refreshes it to the new rendering.
	mustMutate(t, e, "workplan_update", `{"id":"full-plan","appendNotes":["n"]}`)
	now, _ := os.ReadFile(mdPath)
	if !strings.Contains(string(now), "] Blocker finding (open)— why") || strings.Contains(string(now), "finding(open)") {
		t.Fatalf("Markdown not refreshed to the new rendering:\n%s", now)
	}
	p := planFile(t, root.Path, "full-plan")
	if cur, _ := model.RenderMarkdown(p); string(cur) != string(now) {
		t.Fatal("refreshed Markdown is not the current rendering")
	}
	// markdown-only reset of old-rendering Markdown refreshes it too.
	legacy, _ := model.RenderMarkdownLegacy(p)
	os.WriteFile(mdPath, legacy, 0o644)
	mustMutate(t, e, "workplan_reset", `{"id":"full-plan","mode":"markdown-only"}`)
	if now, _ := os.ReadFile(mdPath); !strings.Contains(string(now), "finding (open)") {
		t.Fatal("markdown-only did not refresh the old rendering")
	}
	// Real hand edits are still handwritten.
	os.WriteFile(mdPath, append(legacy, []byte("\nhand edit\n")...), 0o644)
	_, err, n := mutateErr(t, e, "workplan_reset", `{"id":"full-plan","mode":"markdown-only"}`)
	if err == nil || !strings.HasPrefix(err.Error(), "Refusing to replace handwritten") || n != 0 {
		t.Fatalf("hand-edited Markdown: %v", err)
	}
	v, _ = e.Validate(input.ValidateInput{ID: "full-plan"})
	if w, _ := v.Get("warnings"); !containsPrefix(strs(w), "planFile: Linked Markdown is not the generated rendering") {
		t.Fatal("no drift warning on hand-edited Markdown")
	}
}

// TestGeneratedClassification checks markdown/generated-classification:
// stored Markdown is "generated" iff it byte-equals the rendering of the
// normalized stored JSON.
func TestGeneratedClassification(t *testing.T) {
	var doc struct {
		Rows []struct {
			Fixture   string `json:"fixture"`
			PlanID    string `json:"planId"`
			Present   *bool  `json:"markdownPresent"`
			Generated *bool  `json:"generated"`
			Error     string `json:"error"`
		} `json:"rows"`
	}
	testutil.ReadJSON(t, testutil.Testdata("vectors", "markdown", "generated-classification.json"), &doc)
	for _, row := range doc.Rows {
		t.Run(row.Fixture+"/"+row.PlanID, func(t *testing.T) {
			root := testutil.NewRoot(t, row.Fixture)
			e, err := New(root.Path)
			if err != nil {
				t.Fatal(err)
			}
			id, err := normalizeRequested(row.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			gen, present, err := e.markdownGenerated(id)
			if row.Error != "" {
				if err == nil {
					t.Fatalf("expected error %q", row.Error)
				}
				rowErr, _ := testutil.AdaptCaseLookup(row.Fixture, row.Error)
				got, want := normEngineText(root.Normalize(err.Error())), normEngineText(rowErr)
				if got != want {
					t.Fatalf("error %q want %q", got, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if present != *row.Present || (row.Generated != nil && gen != *row.Generated) {
				t.Fatalf("present=%v generated=%v want %v/%v", present, gen, *row.Present, row.Generated)
			}
		})
	}
}

// jscDetail matches engine-specific JavaScriptCore parse text and the Go
// parser's own detail; both become one placeholder so the stable prefix
// still compares byte-exactly.
var jscDetail = regexp.MustCompile(`JSON [Pp]arse error: [^"\\]*`)

func normEngineText(s string) string {
	return jscDetail.ReplaceAllString(s, "JSON parse error: <engine detail>")
}
