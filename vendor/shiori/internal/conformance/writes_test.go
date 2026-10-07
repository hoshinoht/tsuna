package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// Write-path differences: whole-second timestamps and the finding
// rendering of generated Markdown, status-only draft reset, title-slug
// ids, the missing-spec refusal and staging cleanup on recovery.

var (
	hex64       = regexp.MustCompile(`[0-9a-f]{64}`)
	frozenWhole = frozen.Format("2006-01-02T15:04:05Z")
	frozenMilli = frozen.Format("2006-01-02T15:04:05.000Z")
)

// tsNorm maps the whole-second timestamp back to the reference's
// millisecond form and every SHA-256 value to a placeholder (hashes bind
// the changed bytes).
func tsNorm(s string) string {
	return hex64.ReplaceAllString(strings.ReplaceAll(s, frozenWhole, frozenMilli), "<sha256>")
}

// checkChangedFiles compares the changed files with the oracle's under
// the documented write changes: a file equal to the oracle's bytes, or
// equal after tsNorm (whole-second-timestamps), or generated Markdown
// that is the current rendering of the written plan where the oracle file
// is the reference rendering of the oracle plan, the plans equal after
// tsNorm (finding-rendering). A file only one side changed is compared
// with the other side's unchanged copy (for example a plan whose stored
// updatedAt already equals the frozen clock in the millisecond form).
func checkChangedFiles(t *testing.T, v *mutationVector, root testutil.Root, x testutil.Expectation, before, after map[string]string, want map[string]changedFile) int {
	t.Helper()
	fixture := testutil.Testdata("fixtures", v.Fixture)
	paths := map[string]bool{}
	for k := range want {
		paths[k] = true
	}
	for k, b := range before {
		if a, ok := after[k]; !ok || a != b {
			paths[k] = true
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			paths[k] = true
		}
	}
	var oracleRoot string
	diffs, mdDiffs := 0, 0
	for _, rel := range sortedKeys(paths) {
		var oracle []byte
		oracleExists := true
		if w, ok := want[rel]; ok {
			if w.Deleted {
				oracleExists = false
			} else if a, ok := after[rel]; ok && a == w.SHA256 {
				continue
			} else {
				oracle = afterBytes(t, v, rel)
			}
		} else {
			var err error
			if oracle, err = os.ReadFile(filepath.Join(fixture, filepath.FromSlash(rel))); err != nil {
				oracleExists = false
			}
		}
		got, err := os.ReadFile(filepath.Join(root.Path, filepath.FromSlash(rel)))
		gotExists := err == nil
		if gotExists != oracleExists {
			t.Fatalf("%s: exists %v, oracle %v", rel, gotExists, oracleExists)
		}
		if !gotExists || bytes.Equal(got, oracle) {
			continue
		}
		diffs++
		if strings.HasSuffix(rel, ".md") {
			if !x.Has("finding-rendering") {
				t.Fatalf("%s differs from the oracle but the entry lists no finding-rendering", rel)
			}
			if oracleRoot == "" {
				oracleRoot = oracleAfterRoot(t, v, want)
			}
			checkRenderedMarkdown(t, root.Path, oracleRoot, rel, got, oracle, nil)
			mdDiffs++
			continue
		}
		if !x.Has("whole-second-timestamps") {
			t.Fatalf("%s differs from the oracle but the entry lists no whole-second-timestamps", rel)
		}
		if tsNorm(string(got)) != tsNorm(string(oracle)) {
			t.Fatalf("%s differs beyond the timestamp and hashes\n%s", rel, firstDiff(tsNorm(string(got)), tsNorm(string(oracle))))
		}
	}
	if x.Has("finding-rendering") && mdDiffs == 0 {
		t.Fatal("listed with finding-rendering but no generated Markdown differs")
	}
	return diffs
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// oracleAfterRoot materializes the oracle's workspace after the vector:
// the fixture with the recorded after-images applied.
func oracleAfterRoot(t *testing.T, v *mutationVector, want map[string]changedFile) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := testutil.CopyTree(testutil.Testdata("fixtures", v.Fixture), dir); err != nil {
		t.Fatal(err)
	}
	for rel, w := range want {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if w.Deleted {
			os.Remove(p)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, afterBytes(t, v, rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// planLinking loads the plan whose planFile is rel.
func planLinking(t *testing.T, root, rel string) *model.Plan {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(root, ".opencode/workplan/*.json"))
	for _, m := range matches {
		base := filepath.Base(m)
		if strings.Count(base, ".") != 1 {
			continue
		}
		s, err := snapshot.Load(root, strings.TrimSuffix(base, ".json"), snapshot.DefaultLimits)
		if err == nil && s.Plan.PlanFile == rel {
			return s.Plan
		}
	}
	t.Fatalf("no plan links %s", rel)
	return nil
}

// checkRenderedMarkdown: the written Markdown is the current rendering of
// the written plan, the oracle's is the reference rendering of the oracle
// plan, and the two plans are equal after tsNorm (and subst, when set,
// applied to the written plan).
func checkRenderedMarkdown(t *testing.T, root, oracleRoot, rel string, got, oracle []byte, subst func(string) string) {
	t.Helper()
	pGot, pOracle := planLinking(t, root, rel), planLinking(t, oracleRoot, rel)
	stored := string(pGot.EncodeStored())
	if subst != nil {
		stored = subst(stored)
	}
	if tsNorm(stored) != tsNorm(string(pOracle.EncodeStored())) {
		t.Fatalf("plans linking %s differ beyond the timestamp", rel)
	}
	cur, err := model.RenderMarkdown(pGot)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := model.RenderMarkdownLegacy(pOracle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, cur) || !bytes.Equal(oracle, legacy) {
		t.Fatalf("%s: the Markdown is not the current rendering (or the oracle's not the reference rendering)", rel)
	}
}

// outputExcept compares a mutation output with the oracle output after
// tsNorm and deleting the named workplan-summary and top-level members.
func outputExcept(t *testing.T, v *mutationVector, got string, summary []string, top []string) {
	t.Helper()
	g, err := decodeAny(tsNorm(got))
	if err != nil {
		t.Fatal(err)
	}
	var w any
	d := json.NewDecoder(strings.NewReader(tsNorm(string(v.Expect.Output))))
	d.UseNumber()
	d.Decode(&w)
	gm, wm := g.(map[string]any), w.(map[string]any)
	for _, m := range []map[string]any{gm, wm} {
		for _, k := range top {
			delete(m, k)
		}
		if s, ok := m["workplan"].(map[string]any); ok {
			for _, k := range summary {
				delete(s, k)
			}
		}
	}
	if !reflect.DeepEqual(gm, wm) {
		gb, _ := json.Marshal(gm)
		wb, _ := json.Marshal(wm)
		t.Fatalf("output differs beyond the documented members\n got %s\nwant %s", gb, wb)
	}
}

func runVectorMutation(t *testing.T, v *mutationVector, e *engine.Engine) (engine.Output, error, int) {
	in, err := ojson.Parse(v.Call.Input)
	if err != nil {
		t.Fatal(err)
	}
	auth := &countingAuth{}
	out, runErr := runMutation(context.Background(), e, v.Call.Tool, in.Value, auth)
	return out, runErr, auth.n
}

// checkDraftReset: a draft reset keeps phases/steps/findings/notes and
// resets every status to draft; the output differs
// only in the kept counts, the hashes and the timestamp.
func checkDraftReset(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation) {
	dir := filepath.Join(root.Path, ".opencode", "workplan")
	id := inputID(v.Call.Input)
	beforeDoc := decodeJSONFile(t, filepath.Join(dir, id+".json"))
	beforeFiles := snapshotFiles(t, root.Path)
	out, err, n := runVectorMutation(t, v, e)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("authorizations %d, want 1", n)
	}
	outputExcept(t, v, root.Normalize(out.String()), []string{"phaseCount", "stepCount", "openFindingCount"}, []string{"planHash", "stateHash", "checkpointRemoved"})
	// Restate the expected JSON: the before document with every status
	// set to draft and updatedAt at the frozen clock.
	wantDoc := beforeDoc
	wantDoc["status"] = "draft"
	wantDoc["updatedAt"] = frozenWhole
	phases := asList(wantDoc["phases"])
	steps, open := 0, 0
	for _, ph := range phases {
		pm := ph.(map[string]any)
		pm["status"] = "draft"
		for _, st := range asList(pm["steps"]) {
			st.(map[string]any)["status"] = "draft"
			steps++
		}
	}
	for _, f := range asList(wantDoc["reviewFindings"]) {
		if f.(map[string]any)["status"] != "resolved" {
			open++
		}
	}
	afterDoc := decodeJSONFile(t, filepath.Join(dir, id+".json"))
	if !reflect.DeepEqual(afterDoc, wantDoc) {
		t.Fatalf("draft reset JSON is not the status-only reset of the stored plan")
	}
	wp, _ := out.Value.Get("workplan")
	for k, n := range map[string]int{"phaseCount": len(phases), "stepCount": steps, "openFindingCount": open} {
		if x, _ := wp.Get(k); x.NumberLiteral() != fmt.Sprint(n) {
			t.Fatalf("%s %s, restated %d", k, x.NumberLiteral(), n)
		}
	}
	hashesMatchDisk(t, root.Path, id, out)
	// The Markdown was generated, so it is the current rendering of the
	// written plan; the checkpoint (if any) is removed; nothing else
	// changes.
	s, err := snapshot.Load(root.Path, id, snapshot.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if cur, err := model.RenderMarkdown(s.Plan); err != nil || !bytes.Equal(cur, s.Markdown.Bytes) {
		t.Fatalf("Markdown is not the current rendering of the reset plan: %v", err)
	}
	afterFiles := snapshotFiles(t, root.Path)
	allowed := map[string]bool{".opencode/workplan/" + id + ".json": true, s.Plan.PlanFile: true, ".opencode/workplan/" + id + ".checkpoint.json": true}
	for k, b := range beforeFiles {
		if a, ok := afterFiles[k]; (!ok || a != b) && !allowed[k] {
			t.Fatalf("unexpected change %s", k)
		}
	}
	for k := range afterFiles {
		if _, ok := beforeFiles[k]; !ok {
			t.Fatalf("unexpected new file %s", k)
		}
	}
	if _, ok := afterFiles[".opencode/workplan/"+id+".checkpoint.json"]; ok {
		t.Fatal("checkpoint not removed")
	}
	testutil.CheckPin(t, v.ID, x, sha(root.Normalize(out.String())), ojson.UTF16Len(out.String()), root.SameLength())
}

// seededIDs are the generated ids the oracle's seeded random source
// produced; only the id format is contractual.
var seededIDs = map[string][2]string{
	"mutations/create-generated-ids": {"phase-ember-path-597056", "step-river-path-987091"},
}

// checkTitleSlugIDs: generated ids are title slugs; the plan and Markdown
// equal the oracle's after substituting the ids and the timestamp, the
// output differs only in the hashes.
func checkTitleSlugIDs(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation) {
	out, err, n := runVectorMutation(t, v, e)
	if err != nil || n != 1 {
		t.Fatalf("err %v, authorizations %d", err, n)
	}
	outputExcept(t, v, root.Normalize(out.String()), nil, []string{"planHash", "stateHash"})
	ids := seededIDs[v.ID]
	subst := strings.NewReplacer(ids[0], "p", ids[1], "s")
	for rel, cf := range map[string]string{".opencode/workplan/gen.json": "gen.json", ".opencode/workplan/gen.md": "gen.md"} {
		oracle, err := os.ReadFile(testutil.Testdata("vectors", "mutations", "create-generated-ids.after", ".opencode", "workplan", cf))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root.Path, rel))
		if err != nil {
			t.Fatal(err)
		}
		if tsNorm(string(got)) != tsNorm(subst.Replace(string(oracle))) {
			t.Fatalf("%s differs from the oracle beyond the generated ids and the timestamp", rel)
		}
	}
	hashesMatchDisk(t, root.Path, "gen", out)
	testutil.CheckPin(t, v.ID, x, sha(root.Normalize(out.String())), ojson.UTF16Len(out.String()), root.SameLength())
}

// checkMissingSpecRefused: a specFiles link to a missing file is refused
// before authorization; with the file present the
// result is the oracle's up to the documented write changes.
func checkMissingSpecRefused(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation) {
	before := testutil.Fingerprint(t, root.Path)
	_, err, n := runVectorMutation(t, v, e)
	want := "specFiles.0: Linked spec file does not exist: docs/x.md. Create the file first, or leave it out of the list."
	if err == nil || err.Error() != want || n != 0 {
		t.Fatalf("err %v (authorizations %d), want %q before authorization", err, n, want)
	}
	if engine.ErrorClass(err) != "invalid_input" {
		t.Fatalf("class %s", engine.ErrorClass(err))
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("refusal changed files: %v", d)
	}
	testutil.CheckPin(t, v.ID, x, sha(want), ojson.UTF16Len(want), true)
	// With the spec present, every changed file is the oracle's.
	if err := os.MkdirAll(filepath.Join(root.Path, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.Path, "docs", "x.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := snapshotFiles(t, root.Path)
	out, err, n := runVectorMutation(t, v, e)
	if err != nil || n != 1 {
		t.Fatalf("with the spec present: err %v, authorizations %d", err, n)
	}
	outputExcept(t, v, root.Normalize(out.String()), nil, []string{"planHash", "stateHash"})
	checkChangedFiles(t, v, root, x, b, snapshotFiles(t, root.Path), v.ChangedFiles)
}

// checkRecoveryStaging: recovery also removes the interrupted
// transaction's leftover staging file; every other
// change is the oracle's.
func checkRecoveryStaging(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation) {
	jpath := filepath.Join(root.Path, ".opencode/workplan/tx-plan.transaction.json")
	var j struct {
		TransactionID string `json:"transactionId"`
	}
	data, err := os.ReadFile(jpath)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(data, &j)
	matches, _ := filepath.Glob(filepath.Join(root.Path, ".opencode/workplan", "*."+j.TransactionID+".*.stage"))
	if len(matches) == 0 {
		t.Fatal("fixture has no leftover staging file of the interrupted transaction")
	}
	want := map[string]changedFile{}
	for k, c := range v.ChangedFiles {
		want[k] = c
	}
	for _, m := range matches {
		rel, _ := filepath.Rel(root.Path, m)
		want[filepath.ToSlash(rel)] = changedFile{Deleted: true}
	}
	checkMutationVector(t, v, root, e, testutil.Expectation{}, false, want)
}

// guidanceCompat proves that an error text differs from the oracle's only
// by the approved expectedHash guidance: each suffix
// directly follows its reference sentence, removing every suffix gives the
// oracle text byte-for-byte, and the guidance echoes no hash.
func guidanceCompat(got, want string) error {
	var n int
	if strings.Contains(got, nativeHashGuidance) {
		if strings.Count(got, "require the current stateHash"+nativeHashGuidance) != strings.Count(got, nativeHashGuidance) {
			return errors.New("native guidance does not follow its reference sentence")
		}
		n++
	}
	if strings.Contains(got, staleHashGuidance) {
		if !strings.HasSuffix(got, "Reread the plan and recompute the mutation."+staleHashGuidance) {
			return errors.New("stale guidance does not follow its reference sentence")
		}
		n++
	}
	if n == 0 {
		return errors.New("no guidance present")
	}
	if stripped := strings.ReplaceAll(strings.ReplaceAll(got, nativeHashGuidance, ""), staleHashGuidance, ""); stripped != want {
		return fmt.Errorf("text without the guidance differs from the oracle: %q", stripped)
	}
	for _, h := range []string{nativeHashGuidance, staleHashGuidance} {
		if hex64.MatchString(h) {
			return fmt.Errorf("guidance echoes a hash: %q", h)
		}
	}
	return nil
}

// The guidance texts, restated.
const (
	nativeHashGuidance = " — pass expectedHash set to the stateHash from your last successful write, or re-read with workplan_resume or workplan_inspect first"
	staleHashGuidance  = " Use workplan_resume or workplan_inspect for that re-read before retrying, so the retry is based on the current plan."
)
