package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// mutationComparators replace the default comparison of a mutation vector
// entirely, by name.
var mutationComparators = map[string]func(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation){
	"id-refusal":                checkRefusal,
	"duplicate-members-refusal": checkRefusal,
	"precreate-resume":          checkPrecreateResume,
	"patch-validate":            checkPatchValidate,
	"compact-apply":             checkCompactApply,
	"draft-reset":               checkDraftReset,
	"title-slug-ids":            checkTitleSlugIDs,
	"missing-spec-refused":      checkMissingSpecRefused,
	"recovery-staging":          checkRecoveryStaging,
	// Handled by the default comparison.
	"unchanged-markdown":        nil,
	"precreate-journal-refusal": nil,
	"whole-second-timestamps":   nil,
	"finding-rendering":         nil,
	"drop-warnings":             nil,
	"hash-guidance":             nil,
}

// TestMutationVectors runs every mutation vector with a frozen clock at a
// root of the generation root's length and compares the output (or error
// text), the authorization count and the exact changed-file set and bytes
// with the oracle, through the comparators of a listed vector.
func TestMutationVectors(t *testing.T) {
	freezeClock(t)
	saved := engine.IDSource
	engine.IDSource = nil
	t.Cleanup(func() { engine.IDSource = saved })
	files, _ := filepath.Glob(testutil.Testdata("vectors", "mutations", "*.json"))
	if len(files) != 70 {
		t.Fatalf("expected 70 mutation vectors, got %d", len(files))
	}
	counts := map[string]int{}
	var notes []string
	for _, f := range files {
		var v mutationVector
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		x, listed := testutil.Expected(t)[v.ID]
		ok := t.Run(v.ID, func(t *testing.T) {
			root := testutil.NewRoot(t, v.Fixture)
			e := mustEngine(t, root.Path)
			for _, c := range x.Compare {
				run, known := mutationComparators[c]
				if !known {
					t.Fatalf("comparator %q does not apply to mutation vectors", c)
				}
				if run != nil {
					run(t, &v, root, e, x)
					return
				}
			}
			checkMutationVector(t, &v, root, e, x, listed, v.ChangedFiles)
		})
		switch {
		case !ok:
			counts["fail"]++
		case listed:
			counts["documented-difference"]++
			notes = append(notes, v.ID+": "+x.Reason+" ("+x.Contracts+")")
		default:
			counts["pass"]++
		}
	}
	t.Logf("mutation vectors: pass=%d documented-difference=%d fail=%d", counts["pass"], counts["documented-difference"], counts["fail"])
	sort.Strings(notes)
	for _, n := range notes {
		t.Log(n)
	}
	testutil.WritePins(t, "mutations/")
}

// checkMutationVector is the default comparison: authorizations, output or
// error text, metadata and the changed files, each equal to the oracle's
// or differing only by the entry's documented changes.
func checkMutationVector(t *testing.T, v *mutationVector, root testutil.Root, e *engine.Engine, x testutil.Expectation, listed bool, want map[string]changedFile) {
	t.Helper()
	in, err := ojson.Parse(v.Call.Input)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotFiles(t, root.Path)
	auth := &countingAuth{}
	out, runErr := runMutation(context.Background(), e, v.Call.Tool, in.Value, auth)
	wantPrompts := len(v.Prompts)
	if x.Authorizations != nil {
		wantPrompts = *x.Authorizations
	}
	if auth.n != wantPrompts {
		t.Errorf("authorizations %d, want %d", auth.n, wantPrompts)
	}
	if v.Expect.Kind == "error" {
		if runErr == nil {
			t.Fatalf("expected error %q, got output %s", v.Expect.Message, out.String())
		}
		checkErrorText(t, v.ID, root.Normalize(runErr.Error()), v.Expect.Message, x, listed)
		checkChanged(t, want, before, snapshotFiles(t, root.Path))
		return
	}
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	timestamps := listed && x.Has("whole-second-timestamps")
	got := root.Normalize(out.String())
	cmp := got
	if listed && x.Has("drop-warnings") {
		cmp = root.Normalize(string(ojson.Pretty(dropWarningsInverse(t, v, out))))
		if cmp == got {
			t.Fatal("listed with drop-warnings but the result carries none")
		}
	}
	wantText := mutationOracleText(v)
	if timestamps {
		if tsNorm(cmp) != tsNorm(wantText) {
			t.Fatalf("output differs beyond the timestamp and hashes\n%s", firstDiff(tsNorm(cmp), tsNorm(wantText)))
		}
	} else if cmp != wantText || sha(cmp) != v.Expect.OutputSha256 {
		t.Fatalf("output mismatch\n%s", firstDiff(cmp, wantText))
	}
	if len(v.Expect.Metadata) > 0 {
		p, _ := ojson.Parse(v.Expect.Metadata)
		g, w := string(ojson.Compact(out.Metadata)), string(ojson.Compact(p.Value))
		if timestamps {
			g, w = tsNorm(g), tsNorm(w)
		}
		if g != w {
			t.Fatalf("metadata\n got %s\nwant %s", g, w)
		}
	}
	after := snapshotFiles(t, root.Path)
	if listed && (timestamps || x.Has("finding-rendering")) {
		if checkChangedFiles(t, v, root, x, before, after, want) == 0 && got == wantText {
			t.Fatal("listed with write changes but the output and every file equal the oracle's")
		}
		if has(out.Value, "planHash") && has(out.Value, "workplan") {
			hashesMatchDisk(t, root.Path, strMember(out.Value, "workplan", "id"), out)
		}
	} else {
		checkChanged(t, want, before, after)
	}
	if x.Pinned() {
		testutil.CheckPin(t, v.ID, x, sha(got), ojson.UTF16Len(out.String()), root.SameLength())
	}
}

// checkErrorText compares a refusal with the oracle's text, or with the
// guidance comparator and pin of a listed vector.
func checkErrorText(t *testing.T, id, got, want string, x testutil.Expectation, listed bool) {
	t.Helper()
	if got == want {
		if listed && x.Has("hash-guidance") {
			t.Fatalf("listed with hash-guidance but identical to the oracle text; remove the entry")
		}
		return
	}
	if !listed || !x.Has("hash-guidance") {
		t.Fatalf("error\n got %q\nwant %q", got, want)
	}
	if err := guidanceCompat(got, want); err != nil {
		t.Fatalf("hash-guidance comparator: %v\n got %q\nwant %q", err, got, want)
	}
	testutil.CheckPin(t, id, x, sha(got), ojson.UTF16Len(got), true)
}

// afterBytes is the oracle's bytes of a file the vector changed (nil when
// deleted), from the recorded after-image directory.
func afterBytes(t *testing.T, v *mutationVector, rel string) []byte {
	t.Helper()
	name := strings.TrimPrefix(v.ID, "mutations/")
	b, err := os.ReadFile(testutil.Testdata("vectors", "mutations", name+".after", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("no recorded after-image of %s: %v", rel, err)
	}
	return b
}
