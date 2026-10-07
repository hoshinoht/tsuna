package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func TestPlanLinksCrashRecovery(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, mode := range []string{"resume", "rollback"} {
			for _, fault := range []string{storage.FaultJournalSync, storage.FaultPublish + ":0", storage.FaultCleanup} {
				for _, existing := range []bool{false, true} {
					t.Run(fmt.Sprintf("v%d/%s/%s/existing=%v", version, mode, fault, existing), func(t *testing.T) {
						freezeClock(t)
						root := testutil.NewRoot(t, "full-valid")
						e, _ := New(root.Path)
						e.JournalVersion = version
						if existing {
							mustRun(t, e, "update", `{"id":"full-plan","planLinks":[{"planId":"before","relation":"related"}]}`)
						}
						before, _ := os.ReadFile(e.absRel(linksRel("full-plan")))
						prep, err := e.Prepare("update", mustJSON(t, `{"id":"full-plan","planLinks":[{"planId":"after","relation":"blocks"}]}`))
						if err != nil {
							t.Fatal(err)
						}
						after := prep.Intent.Targets[0].After
						_, err = e.Execute(context.Background(), prep, allowAll{}, ExecOptions{Hooks: storage.Hooks{Fault: func(point string) error {
							if point == fault {
								return errors.New("simulated crash")
							}
							return nil
						}}})
						var rr *storage.RecoveryRequiredError
						if !errors.As(err, &rr) {
							t.Fatalf("expected pending journal: %v", err)
						}
						_, err = runMutation(context.Background(), e, "update", mustJSON(t, `{"id":"full-plan","recovery":"`+mode+`","expectedHash":"`+stateHash(t, e, "full-plan")+`"}`), allowAll{})
						if err != nil {
							t.Fatalf("%s recovery: %v", mode, err)
						}
						got, readErr := os.ReadFile(e.absRel(linksRel("full-plan")))
						want := after
						if mode == "rollback" {
							want = before
						}
						if !bytes.Equal(got, want) {
							t.Fatalf("recovered links %s, want %s", got, want)
						}
						if mode == "rollback" && !existing && !os.IsNotExist(readErr) {
							t.Fatalf("rollback must remove newly created links: %v", readErr)
						}
						if m := machinery(t, root.Path); len(m) > 0 {
							t.Fatalf("recovery left machinery: %v", m)
						}
					})
				}
			}
		}
	}
}

func TestLinksJournalScope(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	hash := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, op, path string
		after          *string
	}{
		{"foreign plan", "update", linksRel("other"), &hash},
		{"deletion", "update", linksRel("full-plan"), nil},
		{"wrong operation", "reset:draft", linksRel("full-plan"), &hash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := &model.Journal{WorkplanID: "full-plan", Operation: tc.op, Targets: []model.JournalTarget{{Path: tc.path, AfterHash: tc.after}}}
			var invalid *JournalInvalidError
			if err := e.validateJournal("full-plan", j); !errors.As(err, &invalid) {
				t.Fatalf("unsafe journal accepted: %v", err)
			}
		})
	}
}

func TestRebaseSidecarConflicts(t *testing.T) {
	cases := []struct{ name, first, next string }{
		{"links", `"planLinks":[{"planId":"other","relation":"blocks"}]`, `"planLinks":[{"planId":"third","relation":"related"}]`},
		{"evidence", `"recordEvidence":[{"phaseId":"phase-b","stepId":"step-b1","command":"first","exitCode":0}]`, `"recordEvidence":[{"phaseId":"phase-b","stepId":"step-b1","command":"next","exitCode":1}]`},
		{"lanes", `"lanes":[{"op":"propose","laneId":"one","steps":[{"phaseId":"phase-b","stepId":"step-b1"}],"claims":["src"]}]`, `"lanes":[{"op":"claims","laneId":"one","add":["docs"]}]`},
	}
	for _, tc := range cases {
		for _, theirsOnly := range []bool{false, true} {
			for _, oursOnly := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/theirs-only=%v/ours-only=%v", tc.name, theirsOnly, oursOnly), func(t *testing.T) {
					freezeClock(t)
					root := testutil.NewRoot(t, "full-valid")
					e, _ := New(root.Path)
					h := stateHash(t, e, "full-plan")
					if theirsOnly {
						mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h+`","goal":"new goal"}`)
					}
					first := `{"id":"full-plan","expectedHash":"` + stateHash(t, e, "full-plan") + `",` + tc.first
					if !theirsOnly {
						first += `,"goal":"new goal"`
					}
					mustRun(t, e, "update", first+`}`)
					before := testutil.Fingerprint(t, root.Path)
					next := `{"id":"full-plan","expectedHash":"` + h + `","rebase":true,` + tc.next
					if !oursOnly {
						next += `,"appendNotes":["independent note"]`
					}
					_, err := runMutation(context.Background(), e, "update", mustJSON(t, next+`}`), allowAll{})
					var stale *StaleHashError
					if !errors.As(err, &stale) || !strings.Contains(stale.NotRebased, tc.name) {
						t.Fatalf("overlap must be refused as stale: %v", err)
					}
					if diff := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(diff) > 0 {
						t.Fatalf("refused rebase changed files: %v", diff)
					}
				})
			}
		}
	}
}

func TestRebaseDisjointSidecarUpdate(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	h := stateHash(t, e, "full-plan")
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h+`","goal":"new goal"}`)
	current := stateHash(t, e, "full-plan")
	out := mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+h+`","rebase":true,"planLinks":[{"planId":"other","relation":"related"}]}`)
	if _, ok := out.Get("rebased"); !ok {
		t.Fatal("missing rebased result")
	}
	if stateHash(t, e, "full-plan") != current {
		t.Fatal("sidecar rebase changed plan state")
	}
}
