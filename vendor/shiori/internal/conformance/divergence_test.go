package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// Comparators for the approved oracle divergences.
// Each proves that only the approved difference occurs.

// divergences are the read-vector comparators, by name.
var divergences = map[string]func(v *vector, got string) error{
	"legacy-numbers":      legacyNumbers,
	"unordered-list":      unorderedList,
	"limited-doctor":      limitedDoctor,
	"journal-only-doctor": journalOnlyDoctor,
	"backslash-validate":  backslashValidate,
	"compact-preview":     comparePreview,
}

// legacyNumbers: Go preserves unknown number spellings exactly; the
// reference re-serialized them through IEEE doubles. Mapping the three
// exact spellings back to the reference spellings must yield the vector.
func legacyNumbers(v *vector, got string) error {
	for _, r := range [][2]string{{`"bigInt": 12345678901234567890`, `"bigInt": 12345678901234567000`}, {`"float": 1.0`, `"float": 1`}} {
		if !strings.Contains(got, r[0]) {
			return fmt.Errorf("expected exact spelling %s in Go output", r[0])
		}
		got = strings.Replace(got, r[0], r[1], 1)
	}
	if sha(got) != v.Expect.OutputSha256 {
		return fmt.Errorf("output differs beyond number spelling")
	}
	return nil
}

// unorderedList: same members, deterministic UTF-16 order instead of ICU
// localeCompare / readdir order.
func unorderedList(v *vector, got string) error {
	g, err := decodeAny(got)
	if err != nil {
		return err
	}
	w := expectedAny(v)
	gm, wm := g.(map[string]any), w.(map[string]any)
	for _, key := range []string{"workplans", "sidecars"} {
		if key == "workplans" {
			if _, ok := gm[key]; !ok {
				key = "plans"
			}
		}
		gl, _ := gm[key].([]any)
		wl, _ := wm[key].([]any)
		if len(gl) != len(wl) {
			return fmt.Errorf("%s length %d want %d", key, len(gl), len(wl))
		}
		ser := func(l []any) []string {
			out := make([]string, len(l))
			for i, x := range l {
				b, _ := json.Marshal(x)
				out[i] = string(b)
			}
			return out
		}
		gs, ws := ser(gl), ser(wl)
		sort.Strings(gs)
		sort.Strings(ws)
		if !reflect.DeepEqual(gs, ws) {
			return fmt.Errorf("%s members differ\n got %v\nwant %v", key, gs, ws)
		}
		// Canonical ids keep the reference's relative order.
		delete(gm, key)
		delete(wm, key)
	}
	if !jsonEqual(gm, wm) {
		return fmt.Errorf("non-list fields differ")
	}
	return nil
}

// journalOnlyDoctor: Go adds a plan entry for a journal-only plan carrying
// the read-only interrupted-state hash; everything else matches.
func journalOnlyDoctor(v *vector, got string) error {
	g, err := decodeAny(got)
	if err != nil {
		return err
	}
	gm := g.(map[string]any)
	plans := gm["plans"].([]any)
	if len(plans) != 1 {
		return fmt.Errorf("want one journal-only entry, got %d", len(plans))
	}
	p := plans[0].(map[string]any)
	if p["id"] != "tx-new" || p["recoveryRequired"] != true || p["valid"] != false {
		return fmt.Errorf("unexpected entry %v", p)
	}
	if sh, _ := p["stateHash"].(string); len(sh) != 64 {
		return fmt.Errorf("missing interrupted stateHash")
	}
	gm["plans"] = []any{}
	gm["planCount"] = json.Number("0")
	gm["returnedPlans"] = json.Number("0")
	wm := expectedAny(v).(map[string]any)
	sortSidecars(gm)
	sortSidecars(wm)
	if !jsonEqual(gm, wm) {
		return fmt.Errorf("fields other than the journal-only entry differ")
	}
	return nil
}

func sortSidecars(m map[string]any) {
	l, _ := m["sidecars"].([]any)
	sort.Slice(l, func(i, j int) bool {
		return l[i].(map[string]any)["name"].(string) < l[j].(map[string]any)["name"].(string)
	})
}

// limitedDoctor: with UTF-16 order the first page of a limited doctor
// call holds the first plans/sidecars in that order. Counts and every
// other field must match the reference exactly.
func limitedDoctor(v *vector, got string) error {
	g, err := decodeAny(got)
	if err != nil {
		return err
	}
	gm := g.(map[string]any)
	wm := expectedAny(v).(map[string]any)
	ids := func(l []any, key string) []string {
		out := []string{}
		for _, x := range l {
			out = append(out, x.(map[string]any)[key].(string))
		}
		return out
	}
	gotPlans := ids(gm["plans"].([]any), "id")
	if want := []string{"Bad Name", "UPPER"}; !reflect.DeepEqual(gotPlans, want) {
		return fmt.Errorf("first page %v want %v (UTF-16 order)", gotPlans, want)
	}
	gotSide := ids(gm["sidecars"].([]any), "name")
	if want := []string{".a-plan.json.0000.0.stage", ".workspace-mutation.lock"}; !reflect.DeepEqual(gotSide, want) {
		return fmt.Errorf("first sidecars %v want %v", gotSide, want)
	}
	// Both first-page plans are invalid in the Go order, so the summary
	// issue appears; the lock issue is unchanged.
	for _, k := range []string{"plans", "sidecars", "issues"} {
		delete(gm, k)
		delete(wm, k)
	}
	if !jsonEqual(gm, wm) {
		return fmt.Errorf("counts or other fields differ")
	}
	return nil
}

// backslashValidate: Go diagnoses the ambiguous backslash spec path.
func backslashValidate(v *vector, got string) error {
	g, err := decodeAny(got)
	if err != nil {
		return err
	}
	gm := g.(map[string]any)
	issues := gm["issues"].([]any)
	if len(issues) != 1 || !strings.Contains(issues[0].(string), `docs/quote"back\slash.md`) {
		return fmt.Errorf("expected the backslash issue, got %v", issues)
	}
	gm["issues"] = []any{}
	gm["issueCount"] = json.Number("0")
	gm["valid"] = true
	if !jsonEqual(gm, expectedAny(v)) {
		return fmt.Errorf("fields other than the backslash issue differ")
	}
	return nil
}

// comparePreview checks a compact preview against the oracle after
// substituting the token-derived values (token, archive name, staging
// transaction id) and the removals digest. The lock-protocol auxiliary
// paths in writeIntent.resources and the journal v2 backup links in
// stagingPaths/resources are Shiori's own protocol: every other resource
// is compared exactly.
func comparePreview(v *vector, got string) error {
	g, err := ojson.Parse([]byte(got))
	if err != nil {
		return err
	}
	w, err := ojson.Parse(v.Expect.Output)
	if err != nil {
		return err
	}
	gt, _ := g.Value.Get("previewToken")
	wt, _ := w.Value.Get("previewToken")
	gr, _ := g.Value.Get("removals")
	wr, _ := w.Value.Get("removals")
	gd, _ := gr.Get("digest")
	wd, _ := wr.Get("digest")
	gh, wh := strings.TrimPrefix(gt.Str(), "v1-"), strings.TrimPrefix(wt.Str(), "v1-")
	sub := strings.NewReplacer(gt.Str(), wt.Str(), gh[:16], wh[:16], gh[:12], wh[:12], gd.Str(), wd.Str())
	norm, _ := ojson.Parse([]byte(sub.Replace(got)))
	strip := func(v ojson.Value) (ojson.Value, []string) {
		var out []ojson.Member
		var res []string
		for _, m := range v.Members() {
			if m.Key == "writeIntent" {
				var wi []ojson.Member
				for _, x := range m.Value.Members() {
					if x.Key == "resources" {
						for _, r := range x.Value.Elems() {
							res = append(res, r.Str())
						}
						continue
					}
					if x.Key == "stagingPaths" {
						var keep []ojson.Value
						for _, r := range x.Value.Elems() {
							if !strings.HasSuffix(r.Str(), ".before") {
								keep = append(keep, r)
							}
						}
						x.Value = ojson.ArrayValue(keep)
					}
					wi = append(wi, x)
				}
				m.Value = ojson.ObjectValue(wi)
			}
			out = append(out, m)
		}
		return ojson.ObjectValue(out), res
	}
	a, ares := strip(norm.Value)
	b, bres := strip(w.Value)
	if x, y := string(ojson.Pretty(a)), string(ojson.Pretty(b)); x != y {
		return fmt.Errorf("preview differs:\n%s", firstDiff(x, y))
	}
	isLockAux := func(p string) bool { return strings.Contains(p, ".lock.") || strings.HasSuffix(p, ".before") }
	have := map[string]bool{}
	for _, r := range ares {
		have[r] = true
	}
	for _, r := range bres {
		if !isLockAux(r) && !have[r] {
			return fmt.Errorf("resource missing: %s", r)
		}
	}
	want := map[string]bool{}
	for _, r := range bres {
		want[r] = true
	}
	for _, r := range ares {
		if !isLockAux(r) && !want[r] {
			return fmt.Errorf("extra resource: %s", r)
		}
	}
	return nil
}

// ---- mutation vectors ----

// refusal: the same refusal as the reference (no prompt, no write) with
// the documented message.
func checkRefusal(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation) {
	in, _ := ojson.Parse(v.Call.Input)
	before := testutil.Fingerprint(t, root.Path)
	auth := &countingAuth{}
	_, err := runMutation(context.Background(), e, v.Call.Tool, in.Value, auth)
	if err == nil || root.Normalize(err.Error()) != x.Message {
		t.Fatalf("error %v, want %q", err, x.Message)
	}
	if auth.n != 0 {
		t.Fatalf("authorizations %d, want 0", auth.n)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("refusal changed files: %v", d)
	}
}

// checkPrecreateResume: a journal-only plan is recoverable with the
// doctor's interrupted-state hash; resume publishes the journal's
// after-images and removes the journal (reference: "Workplan file not
// found").
func checkPrecreateResume(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, _ testutil.Expectation) {
	doc, err := e.Doctor(input.DoctorInput{})
	if err != nil {
		t.Fatal(err)
	}
	var sh string
	plans, _ := doc.Get("plans")
	for _, p := range plans.Elems() {
		if id, _ := p.Get("id"); id.Str() == "tx-new" {
			h, _ := p.Get("stateHash")
			sh = h.Str()
		}
	}
	if sh == "" {
		t.Fatal("doctor gave no interrupted-state hash")
	}
	in, _ := ojson.Parse([]byte(`{"id":"tx-new","recovery":"resume","expectedHash":"` + sh + `"}`))
	jbytes, err := os.ReadFile(filepath.Join(root.Path, ".opencode/workplan/tx-new.transaction.json"))
	if err != nil {
		t.Fatal(err)
	}
	auth := &countingAuth{}
	out, err := runMutation(context.Background(), e, "workplan_update", in.Value, auth)
	if err != nil {
		t.Fatal(err)
	}
	if auth.n != 1 {
		t.Fatalf("authorizations %d", auth.n)
	}
	parsed, _ := ojson.Parse(jbytes)
	targets, _ := parsed.Value.Get("targets")
	for _, tg := range targets.Elems() {
		p, _ := tg.Get("path")
		h, _ := tg.Get("afterHash")
		data, err := os.ReadFile(filepath.Join(root.Path, p.Str()))
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != h.Str() {
			t.Fatalf("%s not at its after image", p.Str())
		}
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".opencode/workplan/tx-new.transaction.json")); !os.IsNotExist(err) {
		t.Fatal("journal not removed")
	}
	s, err := snapshot.Load(root.Path, "tx-new", snapshot.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if ph, _ := out.Value.Get("planHash"); ph.Str() != s.PlanHash {
		t.Fatalf("planHash %s want %s", ph.Str(), s.PlanHash)
	}
	if st, _ := out.Value.Get("stateHash"); st.Str() != s.StateHash {
		t.Fatalf("stateHash mismatch")
	}
}

// checkCompactApply: the preview token (and so the archive name and
// transaction id) no longer depends on the absolute root, and the removals
// digest is Shiori-defined; everything else is byte-identical after
// substituting token, archive name and digest, and mapping the
// whole-second timestamp back to milliseconds.
func checkCompactApply(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation) {
	in, _ := ojson.Parse(v.Call.Input)
	var previewMembers []ojson.Member
	for _, m := range in.Value.Members() {
		switch m.Key {
		case "mode", "previewToken", "confirmation", "expectedHash":
		default:
			previewMembers = append(previewMembers, m)
		}
	}
	data, err := input.ParseMutationInput("compact", ojson.ObjectValue(previewMembers), input.SurfaceCore)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := e.CompactPreview(data)
	if err != nil {
		t.Fatal(err)
	}
	tokV, _ := pv.Get("previewToken")
	tok := tokV.Str()
	digV, _ := pv.Get("removals")
	dig, _ := digV.Get("digest")
	var applyMembers []ojson.Member
	for _, m := range in.Value.Members() {
		if m.Key == "previewToken" {
			m.Value = ojson.StringValue(tok)
		}
		applyMembers = append(applyMembers, m)
	}
	before := snapshotFiles(t, root.Path)
	auth := &countingAuth{}
	out, err := runMutation(context.Background(), e, "workplan_compact", ojson.ObjectValue(applyMembers), auth)
	if err != nil {
		t.Fatal(err)
	}
	if auth.n != 1 {
		t.Fatalf("authorizations %d", auth.n)
	}
	wantTokV, _ := in.Value.Get("previewToken")
	wantTok := wantTokV.Str()
	archive, _ := os.ReadFile(testutil.Testdata("vectors", "mutations", "compact-apply-valid.after", ".opencode/workplan/archive/big-plan/state-ef23629bf762-6de192567356.json"))
	ap, _ := ojson.Parse(archive)
	rem, _ := ap.Value.Get("removed")
	wantDig, _ := rem.Get("digest")
	sub := strings.NewReplacer(tok, wantTok, strings.TrimPrefix(tok, "v1-")[:12], strings.TrimPrefix(wantTok, "v1-")[:12], dig.Str(), wantDig.Str())
	after := snapshotFiles(t, root.Path)
	// Content digests of substituted files also appear inside the refreshed
	// checkpoint (manifest and planHash); map Go's digests to the vector's.
	pairs := []string{tok, wantTok, strings.TrimPrefix(tok, "v1-")[:12], strings.TrimPrefix(wantTok, "v1-")[:12], dig.Str(), wantDig.Str()}
	for rel, h := range after {
		if before[rel] == h || strings.HasSuffix(rel, ".checkpoint.json") {
			continue
		}
		if want, ok := v.ChangedFiles[sub.Replace(rel)]; ok {
			pairs = append(pairs, h, want.SHA256)
		}
	}
	postS, err := snapshot.Load(root.Path, "big-plan", snapshot.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	wantCP, _ := os.ReadFile(testutil.Testdata("vectors", "mutations", "compact-apply-valid.after", ".opencode/workplan/big-plan.checkpoint.json"))
	wcp, _ := ojson.Parse(wantCP)
	wph, _ := wcp.Value.Get("planHash")
	pairs = append(pairs, postS.PlanHash, wph.Str())
	sub = strings.NewReplacer(pairs...)
	changed := 0
	for rel, h := range after {
		if before[rel] == h {
			continue
		}
		changed++
		data, _ := os.ReadFile(filepath.Join(root.Path, rel))
		norm := strings.ReplaceAll(sub.Replace(string(data)), frozenWhole, frozenMilli)
		key := sub.Replace(rel)
		want, ok := v.ChangedFiles[key]
		if !ok {
			t.Fatalf("unexpected change %s", rel)
		}
		if sum := sha256.Sum256([]byte(norm)); hex.EncodeToString(sum[:]) != want.SHA256 {
			if strings.HasSuffix(rel, ".md") && x.Has("finding-rendering") {
				// Generated Markdown: the current rendering of the written
				// plan, where the oracle's is the reference rendering.
				checkRenderedMarkdown(t, root.Path, oracleAfterRoot(t, v, v.ChangedFiles), rel, data, afterBytes(t, v, key), sub.Replace)
				continue
			}
			t.Fatalf("%s differs after token/digest substitution", rel)
		}
	}
	if changed != len(v.ChangedFiles) {
		t.Fatalf("changed %d files, want %d", changed, len(v.ChangedFiles))
	}
	// Output: identical after substitution, except the hashes, which must
	// be the hashes of the files Go actually wrote.
	s, err := snapshot.Load(root.Path, "big-plan", snapshot.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(strings.ReplaceAll(sub.Replace(root.Normalize(out.String())), s.StateHash, "STATE"), frozenWhole, frozenMilli)
	p, _ := ojson.Parse(v.Expect.Output)
	sh, _ := p.Value.Get("stateHash")
	want := strings.ReplaceAll(string(ojson.Pretty(p.Value)), sh.Str(), "STATE")
	if got != want {
		t.Fatalf("output mismatch\n%s", firstDiff(got, want))
	}
	testutil.CheckPin(t, v.ID, x, sha(root.Normalize(out.String())), ojson.UTF16Len(out.String()), root.SameLength())
}

// checkPatchValidate: patch validate returns the issue list
// (metadata.validation.issues) and the non-failing Markdown drift
// warning; removing them gives the oracle metadata, and the output text
// and files are the oracle's.
func checkPatchValidate(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, _ testutil.Expectation) {
	in, _ := ojson.Parse(v.Call.Input)
	before := snapshotFiles(t, root.Path)
	auth := &countingAuth{}
	out, err := runMutation(context.Background(), e, v.Call.Tool, in.Value, auth)
	if err != nil {
		t.Fatal(err)
	}
	if auth.n != len(v.Prompts) {
		t.Fatalf("authorizations %d want %d", auth.n, len(v.Prompts))
	}
	if got := root.Normalize(out.String()); got != *v.Expect.OutputText {
		t.Fatalf("output %q", got)
	}
	val, _ := out.Metadata.Get("validation")
	issues, _ := val.Get("issues")
	if issues.Kind() != ojson.Array || len(issues.Elems()) != 0 {
		t.Fatalf("validation.issues = %s, want []", ojson.Compact(issues))
	}
	warnings, _ := val.Get("warnings")
	if len(warnings.Elems()) != 1 || !strings.HasPrefix(warnings.Elems()[0].Str(), "planFile: Linked Markdown is not the generated rendering of the plan JSON (handwritten or edited): .opencode/workplan/minimal.md.") {
		t.Fatalf("validation.warnings = %s", ojson.Compact(warnings))
	}
	var ms []ojson.Member
	for _, m := range out.Metadata.Members() {
		if m.Key == "validation" {
			valid, _ := val.Get("valid")
			count, _ := val.Get("issueCount")
			m.Value = ojson.NewObject(2).Set("valid", valid).Set("issueCount", count).Value()
		}
		ms = append(ms, m)
	}
	p, _ := ojson.Parse(v.Expect.Metadata)
	if g, w := string(ojson.Compact(ojson.ObjectValue(ms))), string(ojson.Compact(p.Value)); g != w {
		t.Fatalf("metadata without the issue list and warning\n got %s\nwant %s", g, w)
	}
	checkChanged(t, v.ChangedFiles, before, snapshotFiles(t, root.Path))
}
