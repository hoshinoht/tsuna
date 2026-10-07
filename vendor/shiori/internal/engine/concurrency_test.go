package engine

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// Separate-process barrier tests: each child prepares
// its mutation, signals readiness, waits for a shared "go" file, then
// authorizes and commits. All children therefore prepared against the
// same state; the locks and the locked recheck must produce exactly one
// winner, with no lost update and no duplicate ownership.

func TestMain(m *testing.M) {
	if os.Getenv("SHIORI_BARRIER_CHILD") == "1" {
		os.Exit(barrierChild())
	}
	if os.Getenv("SHIORI_KILL_CHILD") == "1" {
		os.Exit(killChild()) // recovery_test.go
	}
	flag.Parse()
	// Every test commit cross-checks the fast post-commit hashes against
	// a full reload; benchmarks measure the fast path alone.
	if f := flag.Lookup("test.bench"); f == nil || f.Value.String() == "" {
		verifyPostHashes = true
	}
	testCacheAll = os.Getenv("SHIORI_TEST_CACHE") == "1"
	os.Exit(m.Run())
}

type childResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Phase string `json:"phase"`
}

func barrierChild() int {
	root, tool, raw, dir, name := os.Getenv("B_ROOT"), os.Getenv("B_TOOL"), os.Getenv("B_INPUT"), os.Getenv("B_DIR"), os.Getenv("B_NAME")
	report := func(r childResult) int {
		data, _ := json.Marshal(r)
		os.WriteFile(filepath.Join(dir, name+".result"), data, 0o644)
		return 0
	}
	e, err := New(root)
	if err != nil {
		return report(childResult{Error: err.Error(), Phase: "new"})
	}
	data, err := input.ParseMutationInput(tool, mustJSONPlain(raw), input.SurfaceCore)
	if err != nil {
		return report(childResult{Error: err.Error(), Phase: "parse"})
	}
	p, err := e.Prepare(tool, data)
	os.WriteFile(filepath.Join(dir, name+".ready"), nil, 0o644)
	if err != nil {
		return report(childResult{Error: err.Error(), Phase: "prepare"})
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	hooks := storage.Hooks{}
	if os.Getenv("B_HOLD") != "" {
		hooks.AfterLock = func() { time.Sleep(150 * time.Millisecond) }
	}
	if _, err := e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: hooks}); err != nil {
		return report(childResult{Error: err.Error(), Phase: "commit"})
	}
	return report(childResult{OK: true})
}

type childCall struct{ tool, input string }

func runBarrier(t *testing.T, root string, calls []childCall, hold bool) []childResult {
	t.Helper()
	dir := t.TempDir()
	var cmds []*exec.Cmd
	for i, c := range calls {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "SHIORI_BARRIER_CHILD=1", "B_ROOT="+root, "B_TOOL="+c.tool, "B_INPUT="+c.input, "B_DIR="+dir, fmt.Sprintf("B_NAME=c%d", i))
		if hold {
			cmd.Env = append(cmd.Env, "B_HOLD=1")
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	deadline := time.Now().Add(20 * time.Second)
	for i := range calls {
		for {
			if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("c%d.ready", i))); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("children did not prepare")
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	os.WriteFile(filepath.Join(dir, "go"), nil, 0o644)
	var out []childResult
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d: %v", i, err)
		}
		var r childResult
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("c%d.result", i)))
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(data, &r)
		out = append(out, r)
	}
	return out
}

func winners(t *testing.T, rs []childResult) (int, []string) {
	n := 0
	var errs []string
	for _, r := range rs {
		if r.OK {
			n++
		} else {
			if r.Phase != "commit" {
				t.Fatalf("child failed before commit: %s: %s", r.Phase, r.Error)
			}
			errs = append(errs, r.Error)
		}
	}
	return n, errs
}

func TestBarrierCreateCreateSharedDestination(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls []childCall
	}{
		{"same-plan", []childCall{{"create", `{"id":"race","goal":"one"}`}, {"create", `{"id":"race","goal":"two"}`}, {"create", `{"id":"race","goal":"three"}`}}},
		{"different-plans", []childCall{{"create", `{"id":"a","goal":"g","planFile":".opencode/workplan/shared.md"}`}, {"create", `{"id":"b","goal":"g","planFile":".opencode/workplan/shared.md"}`}, {"create", `{"id":"c","goal":"g","planFile":".opencode/workplan/shared.md"}`}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testutil.NewRoot(t, "empty-workspace")
			rs := runBarrier(t, root.Path, tc.calls, false)
			n, errs := winners(t, rs)
			if n != 1 {
				t.Fatalf("%d winners (%v)", n, errs)
			}
			for _, e := range errs {
				if !strings.Contains(e, "Refusing to overwrite existing workplan artifact") && !strings.Contains(e, "already owned by") {
					t.Fatalf("unexpected loser error: %s", e)
				}
			}
			assertSingleOwner(t, root.Path, ".opencode/workplan/shared.md", tc.name == "different-plans")
			if m := machinery(t, root.Path); len(m) > 0 {
				t.Fatalf("machinery left: %v", m)
			}
		})
	}
}

func assertSingleOwner(t *testing.T, root, md string, check bool) {
	t.Helper()
	if !check {
		return
	}
	owners := 0
	ents, _ := os.ReadDir(filepath.Join(root, ".opencode/workplan"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") && !strings.Contains(e.Name(), ".transaction") {
			data, _ := os.ReadFile(filepath.Join(root, ".opencode/workplan", e.Name()))
			if strings.Contains(string(data), `"planFile": "`+md+`"`) {
				owners++
			}
		}
	}
	if owners != 1 {
		t.Fatalf("%d plans own %s", owners, md)
	}
}

func TestBarrierMoveMoveSharedDestination(t *testing.T) {
	root := testutil.NewRoot(t, "empty-workspace")
	e, _ := New(root.Path)
	for _, id := range []string{"p1", "p2", "p3"} {
		if _, err := runMutation(context.Background(), e, "workplan_create", mustJSON(t, `{"id":"`+id+`","goal":"g"}`), allowAll{}); err != nil {
			t.Fatal(err)
		}
	}
	dest := ".opencode/workplan/moved-here.md"
	calls := []childCall{
		{"update", `{"id":"p1","planFile":"` + dest + `"}`},
		{"update", `{"id":"p2","planFile":"` + dest + `"}`},
		{"update", `{"id":"p3","planFile":"` + dest + `"}`},
	}
	rs := runBarrier(t, root.Path, calls, false)
	n, errs := winners(t, rs)
	if n != 1 {
		t.Fatalf("%d winners (%v)", n, errs)
	}
	for _, e := range errs {
		if !strings.Contains(e, "Refusing to overwrite existing workplan artifact") && !strings.Contains(e, "already owned by") {
			t.Fatalf("unexpected loser error: %s", e)
		}
	}
	assertSingleOwner(t, root.Path, dest, true)
}

// Same plan, same expectedHash: exactly one update wins, the rest are
// stale; no update is lost silently.
func TestBarrierSamePlanUpdates(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e, _ := New(root.Path)
	sh := stateHashFor(t, e, "minimal")
	var calls []childCall
	for i := 0; i < 4; i++ {
		calls = append(calls, childCall{"update", fmt.Sprintf(`{"id":"minimal","appendNotes":["note %d"],"expectedHash":"%s"}`, i, sh)})
	}
	rs := runBarrier(t, root.Path, calls, true)
	n, errs := winners(t, rs)
	if n != 1 {
		t.Fatalf("%d winners (%v)", n, errs)
	}
	for _, e := range errs {
		if !strings.Contains(e, "changed after preparation") {
			t.Fatalf("unexpected loser error: %s", e)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root.Path, ".opencode/workplan/minimal.json"))
	if c := strings.Count(string(data), `"note `); c != 1 {
		t.Fatalf("%d notes recorded", c)
	}
}

// Pending journals claim destinations: a mutation prepared before
// another plan's journal appeared is refused under the lock.
func TestPendingJournalClaimUnderLock(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "empty-workspace")
	e, _ := New(root.Path)
	dest := ".opencode/workplan/claimed.md"
	// B prepares first (destination free).
	dataB, _ := input.ParseMutationInput("create", mustJSON(t, `{"id":"b","goal":"g","planFile":"`+dest+`"}`), input.SurfaceCore)
	pb, err := e.Prepare("create", dataB)
	if err != nil {
		t.Fatal(err)
	}
	// A crashes after publishing its journal (claims dest, file absent).
	dataA, _ := input.ParseMutationInput("create", mustJSON(t, `{"id":"a","goal":"g","planFile":"`+dest+`"}`), input.SurfaceCore)
	pa, _ := e.Prepare("create", dataA)
	_, err = e.Execute(context.Background(), pa, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: func(p string) error {
		if p == storage.FaultJournalSync {
			return errors.New("crash")
		}
		return nil
	}}})
	var rr *storage.RecoveryRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("A: %v", err)
	}
	_, err = e.Execute(context.Background(), pb, allowAll{}, ExecOptions{})
	if err == nil || !strings.Contains(err.Error(), "Workplan destination is claimed by pending transaction") {
		t.Fatalf("B: %v", err)
	}
	// A fresh preparation is refused before authorization too.
	auth := &countingAuth{}
	_, err = runMutation(context.Background(), e, "workplan_create", mustJSON(t, `{"id":"c","goal":"g","planFile":"`+dest+`"}`), auth)
	if err == nil || auth.n != 0 {
		t.Fatalf("C: %v (%d authorizations)", err, auth.n)
	}
	// An unreadable journal is an ambiguous claim and fails closed.
	os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/zzz.transaction.json"), []byte("{bad"), 0o600)
	_, err = runMutation(context.Background(), e, "workplan_create", mustJSON(t, `{"id":"d","goal":"g"}`), auth)
	if err == nil || !strings.Contains(err.Error(), "Ambiguous pending workplan destination claim") {
		t.Fatalf("D: %v", err)
	}
}

func mustJSONPlain(s string) ojson.Value {
	p, err := ojson.Parse([]byte(s))
	if err != nil {
		panic(err)
	}
	return p.Value
}

// Case policy: linked Markdown ownership compares
// case-folded paths, so differently cased links that alias one file on a
// case-insensitive filesystem fail closed on every platform.
func TestOwnershipIsCaseFolded(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "empty-workspace")
	e, _ := New(root.Path)
	if _, err := runMutation(context.Background(), e, "workplan_create", mustJSON(t, `{"id":"x","goal":"g","planFile":".opencode/workplan/Alias.md"}`), allowAll{}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root.Path, ".opencode/workplan/Alias.md")) // absent but still owned
	auth := &countingAuth{}
	_, err := runMutation(context.Background(), e, "workplan_create", mustJSON(t, `{"id":"y","goal":"g","planFile":".opencode/workplan/alias.md"}`), auth)
	if err == nil || !strings.Contains(err.Error(), "already owned by x") || auth.n != 0 {
		t.Fatalf("got %v (%d authorizations)", err, auth.n)
	}
}
