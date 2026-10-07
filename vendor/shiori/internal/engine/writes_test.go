package engine

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// TestGeneratedIDs: readable title slugs, a short suffix only
// on collision, generated step ids unique across the plan, existing ids
// never change.
func TestGeneratedIDs(t *testing.T) {
	root := testutil.NewRoot(t, "empty-workspace")
	e := mustEngine(t, root.Path)
	long := strings.Repeat("Very long title words ", 8)
	mustMutate(t, e, "workplan_create", `{"id":"ids","goal":"g","phases":[
		{"title":"Core api","steps":[{"title":"Write tests"},{"title":"Write tests"},{"id":"write-tests-3","title":"x"},{"title":"日本語"}]},
		{"title":"Core api","steps":[{"title":"Write tests"},{"title":"`+long+`"}]},
		{"id":"core-api-2","title":"Explicit"}]}`)
	p := planFile(t, root.Path, "ids")
	var got []string
	for _, ph := range p.Phases {
		var st []string
		for _, s := range ph.Steps {
			st = append(st, s.ID)
		}
		got = append(got, ph.ID+":"+strings.Join(st, ","))
	}
	if got[0] != "core-api:write-tests,write-tests-2,write-tests-3,"+p.Phases[0].Steps[3].ID || !strings.HasPrefix(p.Phases[0].Steps[3].ID, "step-") {
		t.Fatalf("phase 0: %v", got)
	}
	if !strings.HasPrefix(got[1], "core-api-3:write-tests-4,very-long-title-words") || len(p.Phases[1].Steps[1].ID) > maxSlugUnits || got[2] != "core-api-2:" {
		t.Fatalf("ids %v", got)
	}
	// addSteps/addPhases: slugs avoid every existing id; existing ids stay.
	mustMutate(t, e, "workplan_update", `{"id":"ids","addPhases":[{"phase":{"title":"Core api"}}],"addSteps":[{"phaseId":"core-api-2","step":{"title":"Write tests"}}]}`)
	p2 := planFile(t, root.Path, "ids")
	if p2.Phases[3].ID != "core-api-4" || p2.Phases[2].Steps[0].ID != "write-tests-5" {
		t.Fatalf("added ids %s %s", p2.Phases[3].ID, p2.Phases[2].Steps[0].ID)
	}
	for i := range p.Phases {
		for j := range p.Phases[i].Steps {
			if p2.Phases[i].Steps[j].ID != p.Phases[i].Steps[j].ID {
				t.Fatal("existing ids changed")
			}
		}
	}
}

// TestSpecFilesMustExist: missing spec links are refused at
// write time, before authorization.
func TestSpecFilesMustExist(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	fp := testutil.Fingerprint(t, root.Path)
	for _, tc := range []struct{ tool, input, want string }{
		{"workplan_create", `{"id":"n","goal":"g","specFiles":["docs/none.md"]}`, "specFiles.0: Linked spec file does not exist: docs/none.md"},
		{"workplan_update", `{"id":"minimal","specFiles":["docs/none.md"]}`, "specFiles.0: Linked spec file does not exist: docs/none.md"},
		{"workplan_update", `{"id":"minimal","addSpecFiles":["","docs/none.md"]}`, "addSpecFiles.1: Linked spec file does not exist: docs/none.md"},
	} {
		_, err, n := mutateErr(t, e, tc.tool, tc.input)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) || n != 0 || ErrorClass(err) != "invalid_input" {
			t.Fatalf("%s: %v (authorizations %d)", tc.input, err, n)
		}
	}
	if d := testutil.DiffFingerprints(fp, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("refusals changed files: %v", d)
	}
	os.MkdirAll(filepath.Join(root.Path, "docs"), 0o755)
	os.WriteFile(filepath.Join(root.Path, "docs", "none.md"), []byte("# now exists\n"), 0o644)
	mustMutate(t, e, "workplan_update", `{"id":"minimal","addSpecFiles":["docs/none.md"]}`)
}

// TestNoteLimit: 16 KiB per new note.
func TestNoteLimit(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	ok := strings.Repeat("a", MaxNoteBytes)
	mustMutate(t, e, "workplan_update", `{"id":"minimal","appendNotes":["`+ok+`"]}`)
	_, err, n := mutateErr(t, e, "workplan_update", `{"id":"minimal","appendNotes":["short","`+ok+`b"]}`)
	if err == nil || err.Error() != "appendNotes.1: Note is 16385 bytes, above the 16384-byte (16 KiB) per-note limit. Keep notes short; put long evidence in a file and reference its path." || n != 0 || ErrorClass(err) != "invalid_input" {
		t.Fatalf("appendNotes: %v", err)
	}
	if _, err, _ := mutateErr(t, e, "workplan_create", `{"id":"n2","goal":"g","notes":["`+ok+`é"]}`); err == nil || !strings.HasPrefix(err.Error(), "notes.0: Note is 16386 bytes") {
		t.Fatalf("create notes: %v", err)
	}
	// A stored oversized note (hand-edited) does not block other writes.
	mustMutate(t, e, "workplan_update", `{"id":"minimal","title":"still writable"}`)
}

// TestFileModes: new plan/Markdown files take the mode of the
// existing plans (owner rw added), else 0644 minus the umask; sidecars
// and archives stay 0600.
func TestFileModes(t *testing.T) {
	mode := func(p string) fs.FileMode {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.Mode().Perm()
	}
	root := testutil.NewRoot(t, "empty-workspace")
	e := mustEngine(t, root.Path)
	wp := filepath.Join(root.Path, ".opencode", "workplan")
	mustMutate(t, e, "workplan_create", `{"id":"first","goal":"g"}`)
	want := fs.FileMode(0o644 &^ processUmask)
	if mode(filepath.Join(wp, "first.json")) != want || mode(filepath.Join(wp, "first.md")) != want {
		t.Fatalf("first plan modes %v %v, want %v", mode(filepath.Join(wp, "first.json")), mode(filepath.Join(wp, "first.md")), want)
	}
	os.Chmod(filepath.Join(wp, "first.json"), 0o640)
	mustMutate(t, e, "workplan_create", `{"id":"second","goal":"g","phases":[{"id":"p","title":"P","status":"completed","steps":[{"id":"s","title":"S","action":"x","validation":"y","status":"completed"}]}]}`)
	if mode(filepath.Join(wp, "second.json")) != 0o640 || mode(filepath.Join(wp, "second.md")) != 0o640 {
		t.Fatalf("second plan modes %v", mode(filepath.Join(wp, "second.json")))
	}
	mustMutate(t, e, "workplan_checkpoint", `{"id":"second","summary":"s","nextAction":"n"}`)
	if mode(filepath.Join(wp, "second.checkpoint.json")) != 0o600 {
		t.Fatalf("checkpoint mode %v", mode(filepath.Join(wp, "second.checkpoint.json")))
	}
	// A read-only plan mode still yields owner-writable new files.
	os.Chmod(filepath.Join(wp, "first.json"), 0o444)
	os.Chmod(filepath.Join(wp, "second.json"), 0o444)
	mustMutate(t, e, "workplan_create", `{"id":"third","goal":"g"}`)
	if mode(filepath.Join(wp, "third.json")) != 0o644 {
		t.Fatalf("third plan mode %v", mode(filepath.Join(wp, "third.json")))
	}
	// Existing files keep their mode; the wipe archive is 0600.
	sh := readHashT(t, e, "third")
	tok := wipeToken(t, e, "third", sh, "")
	out := mustMutate(t, e, "workplan_reset", `{"id":"third","mode":"wipe","expectedHash":"`+sh+`","previewToken":"`+tok+`","confirmation":"WIPE_PLAN_CONTENT"}`)
	if mode(filepath.Join(wp, "third.json")) != 0o644 || mode(strMember(out.Value, "archivePath")) != 0o600 {
		t.Fatalf("modes after wipe: %v %v", mode(filepath.Join(wp, "third.json")), mode(strMember(out.Value, "archivePath")))
	}
}

// TestWholeSecondTimestamps: new writes record whole-second UTC; stored
// millisecond (and other accepted) timestamps stay valid and untouched.
func TestWholeSecondTimestamps(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	mustMutate(t, e, "workplan_create", `{"id":"fresh","goal":"g","phases":[{"id":"p","title":"P","steps":[{"id":"s","title":"S","action":"a","validation":"v"}]}]}`)
	whole := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	p := planFile(t, root.Path, "fresh")
	if !whole.MatchString(p.CreatedAt) || !whole.MatchString(p.UpdatedAt) {
		t.Fatalf("timestamps %s / %s", p.CreatedAt, p.UpdatedAt)
	}
	sh := stateHashFor(t, e, "fresh")
	mustMutate(t, e, "workplan_checkpoint", `{"id":"fresh","expectedHash":"`+sh+`","summary":"s","nextAction":"n"}`)
	var cp map[string]any
	data, _ := os.ReadFile(filepath.Join(root.Path, ".opencode/workplan/fresh.checkpoint.json"))
	json.Unmarshal(data, &cp)
	for k, x := range cp {
		if s, ok := x.(string); ok && strings.HasSuffix(k, "At") && !whole.MatchString(s) {
			t.Fatalf("checkpoint %s = %s", k, s)
		}
	}
	// The minimal plan keeps its stored millisecond createdAt; it stays
	// valid, and so do the other accepted forms.
	mustMutate(t, e, "workplan_update", `{"id":"minimal","appendNotes":["n"]}`)
	m := planFile(t, root.Path, "minimal")
	if m.CreatedAt != "2026-01-02T03:04:05.000Z" || !whole.MatchString(m.UpdatedAt) {
		t.Fatalf("minimal %s / %s", m.CreatedAt, m.UpdatedAt)
	}
	for _, ts := range []string{"2026-01-02T03:04:05.000Z", "2026-01-02T03:04:05Z", "2026-01-02T03:04Z", "2026-01-02T03:04:05.123456Z"} {
		if !model.ValidDatetime(ts) {
			t.Fatalf("%s no longer valid", ts)
		}
	}
	if v, _ := e.Validate(input.ValidateInput{ID: "minimal"}); !func() bool { x, _ := v.Get("valid"); return x.Bool() }() {
		t.Fatalf("validate %s", ojson.Compact(v))
	}
}

// TestStaleHashGuidance: the stale-hash refusal keeps the reference text,
// names the current hash once and appends where to re-read.
func TestStaleHashGuidance(t *testing.T) {
	se := &StaleHashError{Current: strings.Repeat("a", 64)}
	if msg := se.Error(); !strings.HasPrefix(msg, "Stale expectedHash; current stateHash is "+se.Current+". Reread the plan and recompute the mutation. ") ||
		strings.Count(msg, se.Current) != 1 || !strings.Contains(msg, "workplan_resume") || ErrorClass(se) != "stale_state" {
		t.Fatalf("stale message %q class %s", msg, ErrorClass(se))
	}
}
