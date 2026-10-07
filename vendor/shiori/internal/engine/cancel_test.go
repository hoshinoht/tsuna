package engine

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

type denyAuth struct{}

func (denyAuth) Authorize(context.Context, AuthRequest) error { return ErrDenied }

// cancelInAuth cancels the request while the approval is pending and then
// "approves" late; the late approval must not reactivate the request.
type cancelInAuth struct{ cancel context.CancelFunc }

func (c cancelInAuth) Authorize(context.Context, AuthRequest) error { c.cancel(); return nil }

// Preparation, denial, cancellation before authorization and a late
// approval after cancellation leave the workspace byte-, mode- and
// mtime-identical: no locks, directories, staging, journals or artifacts.
func TestNoSideEffectsBeforeCommit(t *testing.T) {
	freezeClock(t)
	for _, sc := range faultScenarios {
		for _, how := range []string{"prepare-only", "denied", "cancelled-before", "late-approval"} {
			t.Run(sc.name+"/"+how, func(t *testing.T) {
				root := testutil.NewRoot(t, sc.fixture)
				e, _ := New(root.Path)
				tool, in := sc.call(t, e)
				before := testutil.Fingerprint(t, root.Path)
				data, err := input.ParseMutationInput(tool, mustJSON(t, in), input.SurfaceCore)
				if err != nil {
					t.Fatal(err)
				}
				p, err := e.Prepare(tool, data)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch how {
				case "denied":
					if _, err := e.Execute(ctx, p, denyAuth{}, ExecOptions{}); !errors.Is(err, ErrDenied) {
						t.Fatalf("got %v", err)
					}
				case "cancelled-before":
					cancel()
					var ce *storage.CancelledError
					if _, err := e.Execute(ctx, p, allowAll{}, ExecOptions{}); !errors.As(err, &ce) {
						t.Fatalf("got %v", err)
					}
				case "late-approval":
					var ce *storage.CancelledError
					if _, err := e.Execute(ctx, p, cancelInAuth{cancel}, ExecOptions{}); !errors.As(err, &ce) {
						t.Fatalf("got %v", err)
					}
				}
				if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
					t.Fatalf("side effects: %v", d)
				}
				// A prepared intent is single-use.
				if how != "prepare-only" {
					if _, err := e.Execute(context.Background(), p, allowAll{}, ExecOptions{}); err == nil {
						t.Fatal("prepared intent reused")
					}
				}
			})
		}
	}
}

// Cancellation after the journal is durable stops publication and reports
// an uncertain, recovery-required outcome; the journal is kept.
func TestCancelAfterJournal(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	ctx, cancel := context.WithCancel(context.Background())
	data, _ := input.ParseMutationInput("update", mustJSON(t, `{"id":"full-plan","appendNotes":["x"]}`), input.SurfaceCore)
	p, err := e.Prepare("update", data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Execute(ctx, p, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: func(pt string) error {
		if pt == storage.FaultJournalSync {
			cancel()
		}
		return nil
	}}})
	var rr *storage.RecoveryRequiredError
	if !errors.As(err, &rr) || !rr.Uncertain {
		t.Fatalf("got %v", err)
	}
	if m := pendingMachinery(t, root.Path, true); len(m) > 0 {
		t.Fatalf("machinery: %v", m)
	}
	v, _ := e.Read(input.ReadInput{ID: "full-plan"})
	if r, _ := v.Get("recoveryRequired"); !r.Bool() {
		t.Fatal("read does not report recovery required")
	}
}

// A lock left by a process that is proven dead is reclaimed after the
// grace period (real liveness check, no injection).
func TestDeadOwnerReclaimedAfterGrace(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "minimal-valid")
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot spawn a short-lived process")
	}
	deadPID := cmd.Process.Pid
	writeDeadLock(t, root.Path, ".opencode/workplan/.workspace-mutation.lock", deadPID)
	e, _ := New(root.Path)
	data, _ := input.ParseMutationInput("update", mustJSON(t, `{"id":"minimal","appendNotes":["x"]}`), input.SurfaceCore)
	p, _ := e.Prepare("update", data)
	hooks := storage.Hooks{Lock: storage.LockConfig{Wait: 300 * time.Millisecond, Grace: time.Minute}}
	if _, err := e.Execute(context.Background(), p, allowAll{}, ExecOptions{Hooks: hooks}); err != nil {
		t.Fatal(err)
	}
	if m := machinery(t, root.Path); len(m) > 0 {
		t.Fatalf("machinery: %v", m)
	}
}

func writeDeadLock(t *testing.T, root, rel string, pid int) {
	t.Helper()
	host := hostnameT(t)
	body := `{"hostname":"` + host + `","pid":` + itoaT(pid) + `,"nonce":"deadbeef","startedAt":"2020-01-01T00:00:00.000Z"}` + "\n"
	p := root + "/" + rel
	if err := writeFileT(p, body); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	chtimesT(p, old)
}
