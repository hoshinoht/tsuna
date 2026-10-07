package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// Outcome of one vector.
type outcome int

const (
	outPass outcome = iota
	outDifference
	outDeferred
	outFail
)

type tally struct {
	mu     sync.Mutex
	counts map[string]map[outcome]int
	notes  []string
}

func (t *tally) add(cat string, o outcome, note string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[string]map[outcome]int{}
	}
	if t.counts[cat] == nil {
		t.counts[cat] = map[outcome]int{}
	}
	t.counts[cat][o]++
	if note != "" {
		t.notes = append(t.notes, note)
	}
}

// TestCorpusParity runs every tools/resume/paging vector against the
// engine at a root of the generation root's length, checks the read-only
// gate (no byte, mode or mtime change anywhere in the root), and compares
// the output byte-exactly with the oracle, or, for a listed vector, with
// the oracle through the comparators of its documented differences.
func TestCorpusParity(t *testing.T) {
	var tl tally
	for _, f := range loadVectors(t, "tools", "resume", "paging") {
		var v vector
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		adaptCaseLookup(&v)
		cat := strings.SplitN(v.ID, "/", 2)[0]
		t.Run(v.ID, func(t *testing.T) {
			recorded := false
			defer func() {
				if !recorded {
					tl.add(cat, outFail, v.ID+": FAIL")
				}
			}()
			o, note := runVector(t, &v)
			tl.add(cat, o, note)
			recorded = true
		})
	}
	names := []string{"pass", "documented-difference", "deferred", "fail"}
	cats := make([]string, 0, len(tl.counts))
	for c := range tl.counts {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	for _, c := range cats {
		var parts []string
		for o, n := range names {
			parts = append(parts, fmt.Sprintf("%s=%d", n, tl.counts[c][outcome(o)]))
		}
		t.Logf("parity %-8s %s", c, strings.Join(parts, " "))
	}
	sort.Strings(tl.notes)
	for _, n := range tl.notes {
		t.Log(n)
	}
	testutil.WritePins(t, "tools/", "resume/", "paging/")
}

func runVector(t *testing.T, v *vector) (outcome, string) {
	root := testutil.NewRoot(t, v.Fixture)
	if !root.SameLength() {
		t.Logf("root length differs from generation root; budget-sensitive bytes may differ")
	}
	e := mustEngine(t, root.Path)
	in, err := ojson.Parse(v.Call.Input)
	if err != nil {
		t.Fatal(err)
	}
	before := testutil.Fingerprint(t, root.Path)
	text, runErr := runTool(e, v.Call.Tool, in.Value)
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("read path modified the workspace: %v", d)
	}
	if runErr == errDeferred {
		return outDeferred, v.ID + ": " + v.Call.Tool + " is not a read tool"
	}
	if dir := os.Getenv("SHIORI_VECTOR_DUMP"); dir != "" {
		name := strings.NewReplacer("/", "__", " ", "_").Replace(v.ID)
		body := root.Normalize(text)
		if runErr != nil {
			body = "ERROR: " + root.Normalize(runErr.Error())
		}
		os.WriteFile(filepath.Join(dir, name+".out"), []byte(body), 0o644)
	}
	x, listed := testutil.Expected(t)[v.ID]
	if !listed {
		judgeOracle(t, v, root, text, runErr)
		return outPass, ""
	}
	if runErr != nil {
		t.Fatalf("listed in %s but failed: %v", testutil.ExpectedFile, runErr)
	}
	if v.caseAdapted && strings.Contains(v.ID, "/UPPER--") {
		// The plan is never opened, so it has no raw hashes.
		x.Compare = slices.DeleteFunc(slices.Clone(x.Compare), func(c string) bool { return c == "raw-hashes" })
	}
	judgeListed(t, v, root, e, in.Value, text, x)
	switch {
	case !x.Pinned():
	case testutil.CaseLookupMissing(v.Fixture, text):
		// The pin records APFS output, where the plan was read.
		t.Logf("pin recorded on a case-insensitive filesystem; not comparable here")
	default:
		budgeted := v.Call.Tool == "workplan_resume"
		if !budgeted || root.SameLength() {
			testutil.CheckPin(t, v.ID, x, sha(root.Normalize(text)), ojson.UTF16Len(text), root.SameLength())
		}
	}
	return outDifference, v.ID + ": " + x.Reason + " (" + x.Contracts + ")"
}

// adaptCaseLookup rewrites an APFS-only oracle for a case-sensitive
// filesystem and recomputes its hash.
func adaptCaseLookup(v *vector) {
	msg, a := testutil.AdaptCaseLookup(v.Fixture, v.Expect.Message)
	out, b := testutil.AdaptCaseLookup(v.Fixture, string(v.Expect.Output))
	c := false
	if v.Expect.OutputText != nil {
		var txt string
		txt, c = testutil.AdaptCaseLookup(v.Fixture, *v.Expect.OutputText)
		v.Expect.OutputText = &txt
	}
	if !a && !b && !c {
		return
	}
	v.caseAdapted = true
	v.Expect.Message, v.Expect.Output = msg, json.RawMessage(out)
	if v.Expect.Kind != "error" {
		v.Expect.OutputSha256 = sha(oracleText(v))
	}
}

// readLayers are the documented read-path differences, in the order their
// inverses peel them off the Go output (latest change first). Each
// inverse proves its change is present and returns the output without
// it; the comparators restate the additions from the fixture's raw bytes.
var readLayers = []struct {
	name    string
	inverse func(t *testing.T, v *vector, root testutil.Root, e *engine.Engine, input ojson.Value, text string) string
}{
	{"withheld-fields", withheldInverse},
	{"compaction-advice", adviceInverse},
	{"stale-diagnostic", staleDiagnosticInverse},
	{"resume-critical-path", resumeCriticalPathInverse},
	{"recovered-planfile", recoveredPlanFileInverse},
	{"wiped-note", wipedNoteInverse},
	{"raw-hashes", rawHashesInverse},
	{"stale-markdown", staleMarkdownInverse},
	{"step-markers", stepMarkersInverse},
	{"stale-detail", staleDetailInverse},
	{"list-issues", listIssuesInverse},
	{"graph-inspect", graphInspectInverse},
	{"graph-warnings", graphWarningsInverse},
}

// baseComparators judge the output left after the inverses against the
// oracle: design changes compared field by field (resume budgeting,
// filtered read, additive warnings, graph readiness) and the approved
// oracle divergences.
var baseComparators = map[string]bool{
	"resume-budget": true, "read-slice": true, "additive-warnings": true, "graph-resume": true,
	"legacy-numbers": true, "unordered-list": true, "limited-doctor": true, "journal-only-doctor": true,
	"backslash-validate": true, "compact-preview": true,
}

// unpinned are the comparators that prove a difference without pinning
// the output: the oracle divergences, and design changes whose comparator
// restates the whole result. Every other comparator's vector is pinned.
var unpinned = map[string]bool{
	"legacy-numbers": true, "unordered-list": true, "limited-doctor": true, "journal-only-doctor": true,
	"backslash-validate": true, "compact-preview": true, "compact-apply": true, "id-refusal": true,
	"duplicate-members-refusal": true, "unchanged-markdown": true, "precreate-resume": true,
	"precreate-journal-refusal": true, "nested-unknown-key": true, "missing-specfiles-renders": true,
	"patch-validate": true, "recovery-staging": true, "wipe-mode-enum": true,
}

// judgeListed peels the documented differences off the output and judges
// what is left against the oracle.
func judgeListed(t *testing.T, v *vector, root testutil.Root, e *engine.Engine, input ojson.Value, text string, x testutil.Expectation) {
	t.Helper()
	known := map[string]bool{}
	for _, l := range readLayers {
		known[l.name] = true
	}
	for _, c := range x.Compare {
		if !known[c] && !baseComparators[c] {
			t.Fatalf("comparator %q does not apply to read vectors", c)
		}
	}
	cur := text
	for _, l := range readLayers {
		if !x.Has(l.name) {
			continue
		}
		next := l.inverse(t, v, root, e, input, cur)
		if next == cur {
			t.Fatalf("listed with %s but the output does not carry it", l.name)
		}
		cur = next
	}
	judgeBase(t, v, root, cur, x)
}

// judgeBase compares an output (after the inverses) with the oracle
// through the base comparators the entry lists, or byte-exactly.
func judgeBase(t *testing.T, v *vector, root testutil.Root, text string, x testutil.Expectation) {
	t.Helper()
	got := root.Normalize(text)
	switch {
	case x.Has("graph-resume"):
		// The graph-off packet is the oracle packet itself: readiness and
		// ranking are compared field by field against it.
		checkResumeInvariants(t, v, text)
		if x.Has("resume-budget") {
			t.Fatal("graph-resume is compared against the oracle packet and cannot be combined with resume-budget")
		}
		oracle := oracleText(v)
		if normEngineText(got) == normEngineText(oracle) {
			t.Fatal("listed with graph-resume but identical to the oracle")
		}
		if err := graphResumeCompat(restateGraph(t, root.Path, inputID(v.Call.Input)),
			toGenRoot(root, text), strings.ReplaceAll(oracle, "$ROOT", root.GenRoot)); err != nil {
			t.Fatalf("graph-resume comparator: %v", err)
		}
	case x.Has("resume-budget"), x.Has("read-slice"), x.Has("additive-warnings"):
		if normEngineText(got) == normEngineText(oracleText(v)) {
			t.Fatal("listed with a design change but identical to the oracle")
		}
		var err error
		switch {
		case x.Has("resume-budget"):
			checkResumeInvariants(t, v, text)
			// Compare at the generation root: the oracle truncated some
			// absolute paths, so they were never $ROOT-substituted.
			err = resumeBudgetCompat(toGenRoot(root, text), strings.ReplaceAll(oracleText(v), "$ROOT", root.GenRoot))
		case x.Has("read-slice"):
			err = readSliceCompat(v, got)
		default:
			err = additiveCompat(v, got, x)
		}
		if err != nil {
			t.Fatalf("%s comparator: %v", strings.Join(x.Compare, "+"), err)
		}
	default:
		for _, c := range x.Compare {
			if div, ok := divergences[c]; ok {
				if err := div(v, got); err != nil {
					t.Fatalf("%s comparator failed: %v", c, err)
				}
				return
			}
		}
		judgeOracle(t, v, root, text, nil)
	}
}

// toGenRoot maps output produced at root to the oracle's generation root.
func toGenRoot(root testutil.Root, text string) string {
	return strings.ReplaceAll(text, root.Path, root.GenRoot)
}

// judgeOracle compares an output (or error) with the oracle byte-exactly.
func judgeOracle(t *testing.T, v *vector, root testutil.Root, text string, runErr error) {
	t.Helper()
	if v.Expect.Kind == "error" {
		if runErr == nil {
			t.Fatalf("expected error %q, got output", v.Expect.Message)
		}
		got := root.Normalize(runErr.Error())
		if got == v.Expect.Message {
			return
		}
		if normEngineText(got) == normEngineText(v.Expect.Message) && jscDetail.MatchString(v.Expect.Message) {
			return
		}
		t.Fatalf("error\n got %q\nwant %q", got, v.Expect.Message)
	}
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	got := root.Normalize(text)
	want := oracleText(v)
	if sha(got) == v.Expect.OutputSha256 && got == want {
		if root.SameLength() && !v.caseAdapted && ojson.UTF16Len(text) != v.Expect.RawOutputLength {
			t.Fatalf("raw length %d want %d", ojson.UTF16Len(text), v.Expect.RawOutputLength)
		}
		return
	}
	if jscDetail.MatchString(want) && normEngineText(got) == normEngineText(want) {
		// Engine detail text differs by design; with a budgeted packet the
		// lengths differ too, so the budget invariant is checked instead.
		if v.Call.Tool == "workplan_resume" {
			checkResumeInvariants(t, v, text)
		}
		return
	}
	t.Fatalf("output differs from the oracle and the vector is not listed in %s\n%s", testutil.ExpectedFile, firstDiff(got, want))
}

// TestExpectationsAreKnown: every expectation names an existing corpus
// vector and only known comparators.
func TestExpectationsAreKnown(t *testing.T) {
	known := map[string]bool{}
	for _, l := range readLayers {
		known[l.name] = true
	}
	for c := range baseComparators {
		known[c] = true
	}
	for c := range mutationComparators {
		known[c] = true
	}
	for _, c := range []string{"hash-guidance", "nested-unknown-key", "wipe-mode-enum", "finding-rendering", "missing-specfiles-renders"} {
		known[c] = true
	}
	for id, x := range testutil.Expected(t) {
		if x.Reason == "" || !strings.HasPrefix(x.Contracts, "§") || len(x.Compare) == 0 {
			t.Errorf("%s: an entry needs a reason, a contracts section and its comparators", id)
		}
		for _, c := range x.Compare {
			if !known[c] {
				t.Errorf("%s: unknown comparator %q", id, c)
			}
		}
		if _, err := os.Stat(testutil.Testdata("vectors", id+".json")); err != nil {
			t.Errorf("%s: no such vector", id)
		}
		needsPin := false
		for _, c := range x.Compare {
			needsPin = needsPin || !unpinned[c]
		}
		if needsPin != x.Pinned() {
			t.Errorf("%s: pinned %v, but its comparators need a pin: %v", id, x.Pinned(), needsPin)
		}
	}
}
