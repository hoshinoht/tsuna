package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/hoshinoht/shiori/internal/history"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// faultScenario is one mutation exercised under fault injection.
type faultScenario struct {
	name, fixture, id string
	// call returns the tool and core input (it may run a preview first).
	call func(t *testing.T, e *Engine) (string, string)
}

func fixed(tool, input string) func(*testing.T, *Engine) (string, string) {
	return func(*testing.T, *Engine) (string, string) { return tool, input }
}

var faultScenarios = []faultScenario{
	{"create", "empty-workspace", "fresh", fixed("create", `{"id":"fresh","goal":"g","phases":[{"id":"p","title":"P","steps":[{"id":"s","title":"S"}]}]}`)},
	{"create-overwrite", "minimal-valid", "minimal", func(t *testing.T, e *Engine) (string, string) {
		return "create", `{"id":"minimal","goal":"new goal","overwrite":true,"expectedHash":"` + stateHashFor(t, e, "minimal") + `"}`
	}},
	{"update-move", "minimal-valid", "minimal", fixed("update", `{"id":"minimal","planFile":".opencode/workplan/next/minimal.md","appendNotes":["moved"]}`)},
	{"update-deps", "full-valid", "full-plan", fixed("update", `{"id":"full-plan","updateSteps":[{"phaseId":"phase-b","stepId":"step-b1","status":"completed"}],"dependencies":[{"phaseId":"phase-c","stepId":"step-c1","dependsOn":[{"phaseId":"phase-b","stepId":"step-b1"}]}]}`)},
	{"patch", "minimal-valid", "minimal", fixed("patch", `{"id":"minimal","patchText":"*** Begin Patch\n*** Update File: .opencode/workplan/minimal.md\n@@\n-Ship the minimal plan\n+Patched\n*** End Patch"}`)},
	{"reset", "full-valid", "full-plan", fixed("reset", `{"id":"full-plan"}`)},
	{"checkpoint", "full-valid", "full-plan", fixed("checkpoint", `{"id":"full-plan","summary":"s","nextAction":"n"}`)},
	{"compact", "large-paging", "big-plan", func(t *testing.T, e *Engine) (string, string) {
		sel := `"id":"big-plan","archiveReason":"tidy","completedPhaseIds":["phase-1"],"noteIndexes":[0,1]`
		pv, err := e.CompactPreview(mustParse(t, "compact", mustJSON(t, "{"+sel+"}")))
		if err != nil {
			t.Fatal(err)
		}
		tok, _ := pv.Get("previewToken")
		return "compact", "{" + sel + `,"mode":"apply","confirmation":"ARCHIVE_SELECTED_HISTORY","previewToken":"` + tok.Str() + `"}`
	}},
	// The wipe (archive + plan + Markdown + two sidecar deletions), the
	// status-only draft reset that removes a checkpoint, and the repair of
	// an unreadable plan by create overwrite.
	{"reset-wipe", "full-valid", "full-plan", func(t *testing.T, e *Engine) (string, string) {
		sh := stateHashFor(t, e, "full-plan")
		tok := wipeToken(t, e, "full-plan", sh, "")
		return "reset", `{"id":"full-plan","mode":"wipe","expectedHash":"` + sh + `","previewToken":"` + tok + `","confirmation":"WIPE_PLAN_CONTENT"}`
	}},
	{"reset-draft-checkpoint", "checkpoint-stale-v2", "cp-stale", fixed("reset", `{"id":"cp-stale"}`)},
	{"create-overwrite-unreadable", "invalid-schema", "not-json", func(t *testing.T, e *Engine) (string, string) {
		return "create", `{"id":"not-json","goal":"repaired","overwrite":true,"expectedHash":"` + stateHashFor(t, e, "not-json") + `"}`
	}},
}

func mustJSON(t *testing.T, s string) ojson.Value {
	t.Helper()
	p, err := ojson.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return p.Value
}

// semanticFiles maps every regular file except coordination machinery
// (dot-files in the workplan tree: locks, staging) and the advisory
// change log to its sha256.
func semanticFiles(t *testing.T, root string) map[string]string {
	out := map[string]string{}
	for rel, sha := range snapshotFiles(t, root) {
		if strings.HasPrefix(filepath.Base(rel), ".") && strings.Contains(rel, ".opencode/") || strings.HasSuffix(rel, history.Suffix) {
			continue
		}
		out[rel] = sha
	}
	return out
}

func machinery(t *testing.T, root string) []string {
	var out []string
	for rel := range snapshotFiles(t, root) {
		b := filepath.Base(rel)
		if strings.HasPrefix(b, ".") && (strings.Contains(b, ".lock") || strings.HasSuffix(b, ".stage") || strings.HasSuffix(b, ".before")) && b != ".gitkeep" {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func runScenario(t *testing.T, sc faultScenario, hooks storage.Hooks, ctx context.Context, journalVersion int) (testutil.Root, *Engine, map[string]string, error) {
	root := testutil.NewRoot(t, sc.fixture)
	e, err := New(root.Path)
	if err != nil {
		t.Fatal(err)
	}
	e.JournalVersion = journalVersion
	pre := machinery(t, root.Path)
	old := semanticFiles(t, root.Path)
	tool, in := sc.call(t, e)
	data, err := input.ParseMutationInput(tool, mustJSON(t, in), input.SurfaceCore)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Prepare(tool, data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Execute(ctx, p, allowAll{}, ExecOptions{Hooks: hooks})
	_ = pre
	return root, e, old, err
}

// TestFaultInjection injects a failure at every commit stage of every
// writer and proves the result is the old state, the new state, or an
// explicit recovery-required state that both resume and rollback
// complete.
func TestFaultInjection(t *testing.T) {
	freezeClock(t)
	points := []string{storage.FaultDirs, storage.FaultLocked, storage.FaultStage + ":0", storage.FaultStage + ":1", storage.FaultBackup,
		storage.FaultJournalStage, storage.FaultJournalLink, storage.FaultJournalSync,
		storage.FaultPublish + ":0", storage.FaultPublish + ":1", storage.FaultPublish + ":2", storage.FaultPublish + ":3", storage.FaultPublish + ":4",
		storage.FaultDirSync, storage.FaultCleanup}
	durable := map[string]bool{storage.FaultJournalLink: true, storage.FaultJournalSync: true, storage.FaultDirSync: true, storage.FaultCleanup: true}
	injected := errors.New("injected fault")
	for _, jv := range []int{1, 2} {
		for _, sc := range faultScenarios {
			// Reference outcome without faults.
			root, _, _, err := runScenario(t, sc, storage.Hooks{}, context.Background(), jv)
			if err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			newState := semanticFiles(t, root.Path)
			for _, pt := range points {
				for _, mode := range []string{"resume", "rollback"} {
					t.Run(fmt.Sprintf("v%d/%s/%s/%s", jv, sc.name, pt, mode), func(t *testing.T) {
						hit := false
						hooks := storage.Hooks{Fault: func(p string) error {
							if p == pt {
								hit = true
								return injected
							}
							return nil
						}}
						root, e, old, err := runScenario(t, sc, hooks, context.Background(), jv)
						if !hit {
							if err != nil {
								t.Fatalf("fault point not reached but failed: %v", err)
							}
							t.Skip("fault point not reached by this mutation")
						}
						var rr *storage.RecoveryRequiredError
						isRR := errors.As(err, &rr)
						if isRR != (durable[pt] || strings.HasPrefix(pt, storage.FaultPublish)) {
							t.Fatalf("recovery-required=%v for %s: %v", isRR, pt, err)
						}
						// A pending v2 journal keeps its staged files and
						// backups (its images); nothing else may remain.
						if m := pendingMachinery(t, root.Path, isRR && jv == 2); len(m) > 0 {
							t.Fatalf("locks/staging left behind: %v", m)
						}
						if !isRR {
							if got := semanticFiles(t, root.Path); !equalMaps(got, old) {
								t.Fatalf("pre-journal failure changed artifacts")
							}
							return
						}
						// Journal present; state is recovery-required, visible
						// read-only.
						if _, err := os.Stat(rr.JournalPath); err != nil {
							t.Fatalf("journal missing: %v", err)
						}
						doc, _ := e.Doctor(input.DoctorInput{})
						if n, _ := doc.Get("pendingTransactionCount"); n.NumberLiteral() != "1" {
							t.Fatalf("doctor does not report the pending journal")
						}
						sh := stateHashFor(t, e, sc.id)
						in := fmt.Sprintf(`{"id":%q,"recovery":%q,"expectedHash":%q}`, sc.id, mode, sh)
						if _, err := runMutation(context.Background(), e, "workplan_update", mustJSON(t, in), allowAll{}); err != nil {
							t.Fatalf("%s recovery: %v", mode, err)
						}
						want := newState
						if mode == "rollback" {
							want = old
						}
						if got := semanticFiles(t, root.Path); !equalMaps(got, want) {
							t.Fatalf("%s did not restore the %s state:\n got %v\nwant %v", mode, map[bool]string{true: "new", false: "old"}[mode == "resume"], got, want)
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

// pendingMachinery is machinery without, when images are allowed, the
// stage and backup files of a pending transaction.
func pendingMachinery(t *testing.T, root string, images bool) []string {
	var out []string
	for _, m := range machinery(t, root) {
		if images && (strings.HasSuffix(m, ".stage") || strings.HasSuffix(m, ".before")) && !strings.Contains(m, ".transaction.") {
			continue
		}
		out = append(out, m)
	}
	return out
}

// stateHashFor returns the recovery expectedHash: the doctor hash (which
// covers journal-only plans).
func stateHashFor(t *testing.T, e *Engine, id string) string {
	t.Helper()
	doc, err := e.Doctor(input.DoctorInput{ID: &id})
	if err != nil {
		t.Fatal(err)
	}
	plans, _ := doc.Get("plans")
	for _, p := range plans.Elems() {
		if h, ok := p.Get("stateHash"); ok {
			return h.Str()
		}
	}
	t.Fatalf("no stateHash for %s in doctor", id)
	return ""
}
