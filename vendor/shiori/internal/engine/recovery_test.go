package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// killChild runs one mutation in a separate process and blocks at the
// named commit point after announcing it, until the parent kills it.
func killChild() int {
	root, tool, raw, point, marker := os.Getenv("K_ROOT"), os.Getenv("K_TOOL"), os.Getenv("K_INPUT"), os.Getenv("K_POINT"), os.Getenv("K_MARKER")
	Clock = func() time.Time { return frozen }
	fail := func(err error) int {
		os.WriteFile(marker+".err", []byte(err.Error()), 0o644)
		return 3
	}
	e, err := New(root)
	if err != nil {
		return fail(err)
	}
	data, err := input.ParseMutationInput(tool, mustJSONPlain(raw), input.SurfaceCore)
	if err != nil {
		return fail(err)
	}
	p, err := e.Prepare(tool, data)
	if err != nil {
		return fail(err)
	}
	hooks := storage.Hooks{Fault: func(pt string) error {
		if pt == point {
			os.WriteFile(marker, []byte(pt), 0o644)
			select {} // killed here
		}
		return nil
	}}
	if _, err := e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: hooks}); err != nil {
		return fail(err)
	}
	return fail(errors.New("commit finished without reaching " + point))
}

// killAt runs tool/input in a child process, SIGKILLs it at point and
// returns once the child is gone.
func killAt(t *testing.T, root, tool, raw, point string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "at")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SHIORI_KILL_CHILD=1", "K_ROOT="+root, "K_TOOL="+tool, "K_INPUT="+raw, "K_POINT="+point, "K_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := false
	t.Cleanup(func() {
		if !done {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if data, err := os.ReadFile(marker + ".err"); err == nil {
			t.Fatalf("child failed before %s: %s", point, data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child did not reach %s", point)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL
		t.Fatal(err)
	}
	err := cmd.Wait()
	done = true
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ProcessState.String() != "signal: killed" {
		t.Fatalf("child exit: %v", err)
	}
}

type journalImage struct {
	TransactionID string `json:"transactionId"`
	Targets       []struct {
		Path          string  `json:"path"`
		BeforeHash    *string `json:"beforeHash"`
		AfterHash     *string `json:"afterHash"`
		BeforeContent *string `json:"beforeContent"`
	} `json:"targets"`
}

// TestKillRecovery SIGKILLs a separate process at deterministic commit
// points (journal published; mid-publication) and recovers both ways;
// recovery also removes the interrupted transaction's staging files.
func TestKillRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	freezeClock(t)
	wipeInput := func(t *testing.T, e *Engine) string {
		sh := readHashT(t, e, "full-plan")
		return `{"id":"full-plan","mode":"wipe","expectedHash":"` + sh + `","previewToken":"` + wipeToken(t, e, "full-plan", sh, "") + `","confirmation":"WIPE_PLAN_CONTENT"}`
	}
	cases := []struct {
		name, fixture, id, tool, point string
		call                           func(*testing.T, *Engine) string
		staged                         bool // staging files are left behind
	}{
		{"wipe-after-journal", "full-valid", "full-plan", "reset", storage.FaultJournalLink, wipeInput, true},
		{"wipe-mid-publication", "full-valid", "full-plan", "reset", storage.FaultPublish + ":2", wipeInput, true},
		{"update-mid-publication", "full-valid", "full-plan", "update", storage.FaultPublish + ":1", func(t *testing.T, e *Engine) string {
			return `{"id":"full-plan","appendNotes":["killed"],"expectedHash":"` + readHashT(t, e, "full-plan") + `"}`
		}, true},
	}
	for _, tc := range cases {
		for _, mode := range []string{"resume", "rollback"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				root := testutil.NewRoot(t, tc.fixture)
				e := mustEngine(t, root.Path)
				old := semanticFiles(t, root.Path)
				killAt(t, root.Path, tc.tool, tc.call(t, e), tc.point)
				jpath := filepath.Join(root.Path, ".opencode/workplan", tc.id+".transaction.json")
				data, err := os.ReadFile(jpath)
				if err != nil {
					t.Fatalf("no journal after SIGKILL at %s: %v", tc.point, err)
				}
				var j journalImage
				if err := json.Unmarshal(data, &j); err != nil {
					t.Fatal(err)
				}
				if m := machinery(t, root.Path); len(m) == 0 || (tc.staged && !containsPrefix(m, "."+j.TransactionID+".")) {
					t.Fatalf("expected the killed writer's locks/staging, got %v", m)
				}
				// Read-only diagnosis: recovery required, with a state hash.
				doc, _ := e.Doctor(input.DoctorInput{ID: &tc.id})
				plans, _ := doc.Get("plans")
				entry := plans.Elems()[0]
				if v, _ := entry.Get("recoveryRequired"); !v.Bool() || len(strMember(entry, "stateHash")) != 64 {
					t.Fatalf("doctor: %s", ojson.Compact(entry))
				}
				sh := strMember(entry, "stateHash")
				in := fmt.Sprintf(`{"id":%q,"recovery":%q,"expectedHash":%q}`, tc.id, mode, sh)
				data2, err := input.ParseMutationInput("update", parseT(t, in), input.SurfaceCore)
				if err != nil {
					t.Fatal(err)
				}
				// The dead owner's locks are not reclaimed within the grace
				// period: recovery waits and reports the lock.
				p, err := e.Prepare("update", data2)
				if err != nil {
					t.Fatal(err)
				}
				_, err = e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: storage.Hooks{Lock: storage.LockConfig{Wait: 50 * time.Millisecond}}})
				var lu *storage.LockUnavailableError
				if !errors.As(err, &lu) || !strings.Contains(err.Error(), "abandonment grace") {
					t.Fatalf("recovery inside the grace period: %v", err)
				}
				// Past the grace period the proven-dead owner is reclaimed.
				later := func() time.Time { return time.Now().Add(storage.DefaultLockGrace + time.Minute) }
				p, err = e.Prepare("update", data2)
				if err != nil {
					t.Fatal(err)
				}
				out, err := e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: storage.Hooks{Lock: storage.LockConfig{Now: later}}})
				if err != nil {
					t.Fatalf("%s: %v", mode, err)
				}
				for _, tg := range j.Targets {
					want := tg.AfterHash
					if mode == "rollback" {
						want = tg.BeforeHash
					}
					got, ok := fileSHA(filepath.Join(root.Path, tg.Path))
					if want == nil && ok || want != nil && (!ok || got != *want) {
						t.Fatalf("%s: %s is not at its %s image", mode, tg.Path, mode)
					}
					if mode == "rollback" && tg.BeforeContent != nil {
						b, _ := base64.StdEncoding.DecodeString(*tg.BeforeContent)
						if cur, _ := os.ReadFile(filepath.Join(root.Path, tg.Path)); string(cur) != string(b) {
							t.Fatalf("rollback bytes of %s differ", tg.Path)
						}
					}
				}
				if mode == "rollback" {
					if got := semanticFiles(t, root.Path); !equalMaps(got, old) {
						t.Fatalf("rollback is not the old state")
					}
				}
				if _, err := os.Stat(jpath); !os.IsNotExist(err) {
					t.Fatal("journal not removed")
				}
				if m := machinery(t, root.Path); len(m) > 0 {
					t.Fatalf("stray locks/staging after recovery: %v", m)
				}
				s, err := snapshot.Load(root.Path, tc.id, snapshot.DefaultLimits)
				if err != nil {
					t.Fatal(err)
				}
				if strMember(out.Value, "stateHash") != s.StateHash {
					t.Fatal("recovery result hash differs from disk")
				}
				t.Logf("SIGKILL at %s, %s: journal %s, %d targets restored, state %s", tc.point, mode, j.TransactionID[:8], len(j.Targets), s.StateHash[:12])
			})
		}
	}
}
