package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// evidenceRoot is a git repository with src/a.go committed and a plan
// "demo" with steps p1/s1 and p1/s2. withGit=false leaves git out.
func evidenceRoot(t *testing.T, withGit bool) (*Engine, string) {
	t.Helper()
	if withGit {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
	}
	freezeClock(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a\n"), 0o644)
	if withGit {
		gitT(t, root, "init", "-q")
		gitT(t, root, "add", "-A")
		gitT(t, root, "commit", "-qm", "init")
	}
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := ojson.Parse([]byte(`{"id":"demo","goal":"g","phases":[{"id":"p1","title":"P","steps":[
		{"id":"s1","title":"S1","target":"src","action":"a","validation":"v"},
		{"id":"s2","title":"S2","target":"src","action":"a","validation":"v"}]}]}`))
	if _, err := runMutation(context.Background(), e, "create", in.Value, allowAll{}); err != nil {
		t.Fatal(err)
	}
	return e, root
}

func stateOf(t *testing.T, e *Engine) *snapshot.Snapshot {
	t.Helper()
	s, err := e.load("demo")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func recordT(t *testing.T, e *Engine, recs string) (Output, error) {
	t.Helper()
	in, err := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + stateOf(t, e).StateHash + `","recordEvidence":[` + recs + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	return runMutation(context.Background(), e, "update", in.Value, allowAll{})
}

func stepStates(t *testing.T, e *Engine) map[string]string {
	t.Helper()
	v, err := e.Inspect(input.InspectInput{ID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	steps, _ := v.Get("steps")
	for _, st := range steps.Elems() {
		id, _ := st.Get("id")
		ev, ok := st.Get("evidence")
		if !ok {
			t.Fatalf("step %s has no evidence member", id.Str())
		}
		s, _ := ev.Get("state")
		out[id.Str()] = s.Str()
	}
	return out
}

func TestEvidenceRecordKeepsPlanAndHashes(t *testing.T) {
	e, root := evidenceRoot(t, true)
	before := stateOf(t, e)
	out, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"go test ./...","exitCode":0,"output":"ok"}`)
	if err != nil {
		t.Fatal(err)
	}
	after := stateOf(t, e)
	if after.StateHash != before.StateHash || after.PlanHash != before.PlanHash || string(after.JSON.Bytes) != string(before.JSON.Bytes) {
		t.Fatal("recording evidence changed the plan or its hashes")
	}
	if sh, _ := out.Value.Get("stateHash"); sh.Str() != before.StateHash {
		t.Fatalf("result stateHash %s", sh.Str())
	}
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan/demo.evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := ojson.Parse(data)
	l, issues := evidence.Decode(p.Value)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	r := l.Records[0]
	if r.Source != evidence.SourceAgent || r.TreeOID == nil || r.OutputDigest == nil || *r.OutputDigest != testutil.SHA256Hex("ok") || r.RecordedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("record %+v", r)
	}
	if got := stepStates(t, e); got["s1"] != "fresh" || got["s2"] != "none" {
		t.Fatalf("states %v", got)
	}
}

func TestEvidenceStalenessFollowsScope(t *testing.T) {
	e, root := evidenceRoot(t, true)
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	os.WriteFile(filepath.Join(root, "docs", "x.md"), []byte("x"), 0o644)
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0,"scope":["src/"]},
		{"phaseId":"p1","stepId":"s2","command":"t","exitCode":0}`); err != nil {
		t.Fatal(err)
	}
	// Outside the scope, untracked: only the unscoped record goes stale.
	os.WriteFile(filepath.Join(root, "docs", "x.md"), []byte("y"), 0o644)
	if got := stepStates(t, e); got["s1"] != "fresh" || got["s2"] != "stale" {
		t.Fatalf("after docs edit %v", got)
	}
	// The plan's own files never count.
	os.WriteFile(filepath.Join(root, "docs", "x.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, ".opencode/workplan/demo.md"), []byte("edited\n"), 0o644)
	if got := stepStates(t, e); got["s1"] != "fresh" || got["s2"] != "fresh" {
		t.Fatalf("after workplan edit %v", got)
	}
	// A new untracked file inside the scope.
	os.WriteFile(filepath.Join(root, "src", "b.go"), []byte("package a\n"), 0o644)
	if got := stepStates(t, e); got["s1"] != "stale" {
		t.Fatalf("after src add %v", got)
	}
	os.Remove(filepath.Join(root, "src", "b.go"))
	if got := stepStates(t, e); got["s1"] != "fresh" {
		t.Fatalf("after revert %v", got)
	}
	// A failing later record makes the step failing; a passing one after
	// it restores it.
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":2,"scope":["src"]}`); err != nil {
		t.Fatal(err)
	}
	if got := stepStates(t, e); got["s1"] != "failing" {
		t.Fatalf("after failure %v", got)
	}
}

func TestEvidenceNeverWritesTheRepository(t *testing.T) {
	e, root := evidenceRoot(t, true)
	os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a // dirty\n"), 0o644)
	os.WriteFile(filepath.Join(root, "src", "new.go"), []byte("package a\n"), 0o644)
	before := testutil.Fingerprint(t, filepath.Join(root, ".git"))
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0,"scope":["src"]}`); err != nil {
		t.Fatal(err)
	}
	stepStates(t, e)
	if _, err := e.Doctor(input.DoctorInput{}); err != nil {
		t.Fatal(err)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, filepath.Join(root, ".git"))); len(d) > 0 {
		t.Fatalf("evidence wrote to the repository: %v", d)
	}
}

func TestEvidenceWithoutGitIsUnknown(t *testing.T) {
	e, _ := evidenceRoot(t, false)
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0}`); err != nil {
		t.Fatal(err)
	}
	if got := stepStates(t, e); got["s1"] != "unknown" {
		t.Fatalf("states %v", got)
	}
	v, err := e.Doctor(input.DoctorInput{})
	if err != nil {
		t.Fatal(err)
	}
	plans, _ := v.Get("plans")
	ev, _ := plans.Elems()[0].Get("evidence")
	tree, _ := ev.Get("tree")
	if oid, _ := tree.Get("oid"); oid.Kind() != ojson.Null {
		t.Fatalf("tree %s", ojson.Compact(tree))
	}
}

func TestEvidenceInvalidLedgerDegradesOnlyEvidence(t *testing.T) {
	e, root := evidenceRoot(t, true)
	p := filepath.Join(root, ".opencode/workplan/demo.evidence.json")
	os.WriteFile(p, []byte(`{"schemaVersion":1,"id":"demo","records":"x"}`), 0o600)
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0}`); err == nil || !strings.Contains(err.Error(), "evidence ledger at") {
		t.Fatalf("recording into an invalid ledger: %v", err)
	}
	if data, _ := os.ReadFile(p); string(data) != `{"schemaVersion":1,"id":"demo","records":"x"}` {
		t.Fatal("invalid ledger was rewritten")
	}
	v, err := e.Doctor(input.DoctorInput{})
	if err != nil {
		t.Fatal(err)
	}
	entry := v
	plans, _ := entry.Get("plans")
	pe := plans.Elems()[0]
	ev, _ := pe.Get("evidence")
	if valid, _ := ev.Get("valid"); valid.Bool() {
		t.Fatal("invalid ledger reported valid")
	}
	if valid, _ := pe.Get("valid"); !valid.Bool() {
		t.Fatal("an invalid ledger made the plan invalid")
	}
	if _, err := e.Inspect(input.InspectInput{ID: "demo"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Resume(input.ResumeInput{ID: "demo"}); err != nil {
		t.Fatal(err)
	}
	// A plain update still works and gives no evidence warnings.
	in, _ := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + stateOf(t, e).StateHash + `","updateSteps":[{"phaseId":"p1","stepId":"s1","status":"completed"}]}`))
	if _, err := runMutation(context.Background(), e, "update", in.Value, allowAll{}); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceCompletionWarnings(t *testing.T) {
	complete := func(t *testing.T, e *Engine, extra string) []string {
		in, _ := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + stateOf(t, e).StateHash + `","updateSteps":[{"phaseId":"p1","stepId":"s1","status":"completed"},{"phaseId":"p1","stepId":"s2","status":"completed"}]` + extra + `}`))
		out, err := runMutation(context.Background(), e, "update", in.Value, allowAll{})
		if err != nil {
			t.Fatal(err)
		}
		w, _ := out.Value.Get("warnings")
		var ws []string
		for _, x := range w.Elems() {
			ws = append(ws, x.Str())
		}
		return ws
	}
	t.Run("no ledger", func(t *testing.T) {
		e, _ := evidenceRoot(t, true)
		if ws := complete(t, e, ""); len(ws) != 0 {
			t.Fatalf("warnings without a ledger: %v", ws)
		}
	})
	t.Run("recorded in the same update", func(t *testing.T) {
		e, _ := evidenceRoot(t, true)
		ws := complete(t, e, `,"recordEvidence":[{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0},{"phaseId":"p1","stepId":"s2","command":"t","exitCode":1}]`)
		if len(ws) != 1 || !strings.Contains(ws[0], "p1/s2") || !strings.Contains(ws[0], "(failing)") {
			t.Fatalf("warnings %v", ws)
		}
		if !strings.Contains(string(stateOf(t, e).JSON.Bytes), `"completed"`) {
			t.Fatal("plan not updated")
		}
	})
}

func TestEvidenceInputRefusals(t *testing.T) {
	e, root := evidenceRoot(t, true)
	cases := map[string]string{
		`{"phaseId":"p9","stepId":"s1","command":"t","exitCode":0}`:                                                               "Phase not found: p9",
		`{"phaseId":"p1","stepId":"s9","command":"t","exitCode":0}`:                                                               "Step not found in phase p1: s9",
		`{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0,"scope":[".opencode/workplan"]}`:                                "cannot name the workplan directory",
		`{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0,"scope":["../x"]}`:                                              "inside the workspace root",
		`{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0,"output":"a","outputDigest":"` + strings.Repeat("a", 64) + `"}`: "mutually exclusive",
	}
	for rec, want := range cases {
		if _, err := recordT(t, e, rec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", rec, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode/workplan/demo.evidence.json")); !os.IsNotExist(err) {
		t.Fatal("a refused record created the ledger")
	}
}

func TestEvidenceWriteIsRecoverable(t *testing.T) {
	for _, mode := range []string{"resume", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			e, root := evidenceRoot(t, true)
			in, _ := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + stateOf(t, e).StateHash + `","recordEvidence":[{"phaseId":"p1","stepId":"s1","command":"t","exitCode":0}]}`))
			data, err := input.ParseMutationInput("update", in.Value, input.SurfaceCore)
			if err != nil {
				t.Fatal(err)
			}
			prep, err := e.Prepare("update", data)
			if err != nil {
				t.Fatal(err)
			}
			boom := errors.New("injected")
			_, err = e.Execute(context.Background(), prep, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: func(p string) error {
				if p == storage.FaultCleanup {
					return boom
				}
				return nil
			}}})
			var rr *storage.RecoveryRequiredError
			if !errors.As(err, &rr) {
				t.Fatalf("want recovery required, got %v", err)
			}
			v, err := e.Doctor(input.DoctorInput{})
			if err != nil {
				t.Fatal(err)
			}
			plans, _ := v.Get("plans")
			sh, _ := plans.Elems()[0].Get("stateHash")
			rin, _ := ojson.Parse([]byte(`{"id":"demo","expectedHash":"` + sh.Str() + `","recovery":"` + mode + `"}`))
			if _, err := runMutation(context.Background(), e, "update", rin.Value, allowAll{}); err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(root, ".opencode/workplan/demo.evidence.json"))
			if exists := err == nil; exists != (mode == "resume") {
				t.Fatalf("%s: ledger exists=%v", mode, exists)
			}
		})
	}
}

// TestEvidenceCommitLinks: passing records link to the commit whose
// content they tested; over a run of commits with the same scope content
// the oldest (the one that introduced it) is named.
func TestEvidenceCommitLinks(t *testing.T) {
	e, root := evidenceRoot(t, true)
	head := func() string {
		out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	initial := head()
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"go test","exitCode":0},{"phaseId":"p1","stepId":"s2","command":"go vet","exitCode":0,"scope":["src"]}`); err != nil {
		t.Fatal(err)
	}
	links := func() map[string]CommitLink {
		m, err := e.EvidenceCommits(context.Background(), "demo", 20)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]CommitLink{}
		for ref, cmds := range m {
			for _, l := range cmds {
				out[ref.StepID] = l
			}
		}
		return out
	}
	if l := links(); l["s1"] != (CommitLink{initial, "tree"}) || l["s2"] != (CommitLink{initial, "scope"}) {
		t.Fatalf("clean tree: %v (init %s)", l, initial)
	}
	os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a\n\nvar X = 1\n"), 0o644)
	gitT(t, root, "commit", "-qam", "two")
	two := head()
	os.WriteFile(filepath.Join(root, "README"), []byte("r\n"), 0o644)
	gitT(t, root, "add", "README")
	gitT(t, root, "commit", "-qm", "three")
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s2","command":"go vet","exitCode":0,"scope":["src"]}`); err != nil {
		t.Fatal(err)
	}
	if l := links(); l["s1"] != (CommitLink{initial, "tree"}) || l["s2"] != (CommitLink{two, "scope"}) {
		t.Fatalf("after two commits: %v (two %s)", l, two)
	}
	// Uncommitted content links nowhere.
	os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a // wip\n"), 0o644)
	if _, err := recordT(t, e, `{"phaseId":"p1","stepId":"s1","command":"go test","exitCode":0}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := links()["s1"]; ok {
		t.Fatal("uncommitted state linked to a commit")
	}
}
