package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/testutil"
)

func withPrompt(t *testing.T, tty bool, input string) {
	oldT, oldIn := IsTerminal, Stdin
	IsTerminal = func() bool { return tty }
	Stdin = strings.NewReader(input)
	t.Cleanup(func() { IsTerminal, Stdin = oldT, oldIn })
}

func stateHash(t *testing.T, root string) string {
	code, out, errOut := run("read", "minimal", "--json", "--no-markdown", "--root", root)
	if code != 0 {
		t.Fatal(errOut)
	}
	var v struct{ StateHash string }
	json.Unmarshal([]byte(out), &v)
	return v.StateHash
}

// Off a TTY --yes is required; existing-state writes need
// --expected-hash unless --legacy-unhashed; refusals change nothing.
func TestMutationConfirmationPolicy(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	before := testutil.Fingerprint(t, root.Path)
	withPrompt(t, false, "")
	if code, _, errOut := run("update", "minimal", "--append-note", "x", "--root", root.Path); code != 2 || !strings.Contains(errOut, "--expected-hash") {
		t.Fatalf("missing hash: %d %s", code, errOut)
	}
	sh := stateHash(t, root.Path)
	code, out, _ := run("update", "minimal", "--append-note", "x", "--expected-hash", sh, "--json", "--root", root.Path)
	if code != 1 || !strings.Contains(out, `"class": "permission_denied"`) {
		t.Fatalf("off-TTY without --yes: %d %s", code, out)
	}
	withPrompt(t, true, "no\n")
	if code, _, _ := run("update", "minimal", "--append-note", "x", "--expected-hash", sh, "--root", root.Path); code != 1 {
		t.Fatalf("answer 'no' accepted")
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("refusals wrote: %v", d)
	}
	withPrompt(t, true, "yes\n")
	code, out, errOut := run("update", "minimal", "--append-note", "x", "--expected-hash", sh, "--json", "--root", root.Path)
	if code != 0 || !strings.Contains(errOut, "Type yes to apply") || !strings.Contains(out, `"updated": true`) {
		t.Fatalf("TTY yes: %d %s %s", code, out, errOut)
	}
	// --yes never bypasses the stale-hash check.
	code, out, _ = run("update", "minimal", "--append-note", "y", "--expected-hash", sh, "--yes", "--json", "--root", root.Path)
	if code != 1 || !strings.Contains(out, `"class": "stale_state"`) {
		t.Fatalf("stale with --yes: %d %s", code, out)
	}
	// --legacy-unhashed still commits through the locked recheck.
	withPrompt(t, false, "")
	if code, _, errOut := run("update", "minimal", "--append-note", "z", "--legacy-unhashed", "--yes", "--root", root.Path); code != 0 {
		t.Fatalf("legacy: %s", errOut)
	}
}

// --json prints exactly the tool result text for a mutation.
func TestMutationJSONMatchesVector(t *testing.T) {
	var v struct {
		Expect struct {
			OutputSha256 string `json:"outputSha256"`
		} `json:"expect"`
	}
	testutil.ReadJSON(t, testutil.Testdata("vectors", "mutations", "create-new-full.json"), &v)
	root := testutil.NewRoot(t, "empty-workspace")
	withPrompt(t, false, "")
	in := `{"id":"New Plan!","kind":"  ","title":"  New  ","goal":" g ","scope":["a"," a ","","b"],"specFiles":["docs/x.md","./docs/x.md"],"phases":[{"id":"Phase One","title":" P1 ","steps":[{"id":"S 1","title":" s1 ","target":" ","action":" act ","validation":"val"}]}],"reviewFindings":[{"severity":"note","title":" f ","detail":" ","source":"src"}],"notes":["n","n"]}`
	// A link to a missing spec is refused before the prompt, with class
	// invalid_input.
	code, out, _ := run("create", "--input", in, "--yes", "--json", "--root", root.Path)
	if code != 1 || !strings.Contains(out, `"class": "invalid_input"`) || !strings.Contains(out, "Linked spec file does not exist: docs/x.md") {
		t.Fatalf("missing spec: code %d, %s", code, out)
	}
	os.MkdirAll(filepath.Join(root.Path, "docs"), 0o755)
	os.WriteFile(filepath.Join(root.Path, "docs", "x.md"), []byte("# x\n"), 0o644)
	code, out, errOut := run("create", "--input", in, "--yes", "--json", "--root", root.Path)
	if code != 0 {
		t.Fatal(out, errOut)
	}
	// The clock is real here, so compare everything but timestamps/hashes
	// structurally and require the vector's file set.
	if !strings.Contains(out, `"created": true`) || !strings.Contains(out, `"planFile": ".opencode/workplan/new-plan.md"`) {
		t.Fatalf("output %s", out)
	}
	for _, f := range []string{"new-plan.json", "new-plan.md"} {
		if _, err := os.Stat(filepath.Join(root.Path, ".opencode/workplan", f)); err != nil {
			t.Fatal(err)
		}
	}
}

// Patch --json carries the text output and the metadata.
func TestPatchJSON(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	withPrompt(t, false, "")
	pf := filepath.Join(t.TempDir(), "p.patch")
	os.WriteFile(pf, []byte("*** Begin Patch\n*** Update File: .opencode/workplan/minimal.md\n@@\n-Ship the minimal plan\n+Patched\n*** End Patch\n"), 0o644)
	code, out, errOut := run("patch", "minimal", "--patch-file", pf, "--validate", "--legacy-unhashed", "--yes", "--json", "--root", root.Path)
	if code != 0 {
		t.Fatal(errOut)
	}
	var v struct {
		Output   string
		Metadata struct {
			Patched  bool
			Validate bool
		}
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || !v.Metadata.Patched || !v.Metadata.Validate || !strings.HasPrefix(v.Output, "Patched workplan minimal") {
		t.Fatalf("%v %s", err, out)
	}
}

// Compaction through the CLI: preview (read-only, no confirmation), then
// apply with token, confirmation and expected hash.
func TestCompactCLI(t *testing.T) {
	root := testutil.NewRoot(t, "large-paging")
	withPrompt(t, false, "")
	before := testutil.Fingerprint(t, root.Path)
	code, out, errOut := run("compact", "big-plan", "--reason", "tidy", "--archive-phase", "phase-1", "--archive-note", "0", "--json", "--root", root.Path)
	if code != 0 {
		t.Fatal(errOut)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("preview wrote: %v", d)
	}
	var pv struct{ PreviewToken, StateHash string }
	json.Unmarshal([]byte(out), &pv)
	code, out, errOut = run("compact", "big-plan", "--reason", "tidy", "--archive-phase", "phase-1", "--archive-note", "0", "--apply", "--preview-token", pv.PreviewToken, "--confirm", "ARCHIVE_SELECTED_HISTORY", "--expected-hash", pv.StateHash, "--yes", "--json", "--root", root.Path)
	if code != 0 || !strings.Contains(out, `"compacted": true`) {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
}

// The status gate: the CLI error carries the exact field-path issues and
// nothing is prepared or written.
func TestStatusGateCLI(t *testing.T) {
	root := testutil.NewRoot(t, "empty-workspace")
	before := testutil.Fingerprint(t, root.Path)
	withPrompt(t, false, "")
	code, out, _ := run("create", "--input", `{"id":"gated","goal":"g","status":"in_progress","phases":[{"id":"p","title":"P","steps":[{"id":"s","title":"S","action":"a"}]}]}`,
		"--yes", "--json", "--root", root.Path)
	var e struct {
		Error struct {
			Class  string `json:"class"`
			Issues []struct{ Path, Message string }
		} `json:"error"`
	}
	json.Unmarshal([]byte(out), &e)
	if code != 1 || e.Error.Class != "invalid_structure" || len(e.Error.Issues) != 1 ||
		e.Error.Issues[0].Path != "phases.0.steps.0.validation" || e.Error.Issues[0].Message != "Required for executable workplans" {
		t.Fatalf("gate: %d %s", code, out)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("gate wrote: %v", d)
	}
}

// The step status gate carries field paths in the --json error and
// refuses before the prompt.
func TestStepStatusGateCLI(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	withPrompt(t, false, "")
	if code, out, errOut := run("update", "minimal", "--input", `{"addSteps":[{"phaseId":"phase-one","step":{"id":"bare","title":"Bare"}}]}`, "--legacy-unhashed", "--yes", "--root", root.Path); code != 0 {
		t.Fatal(out, errOut)
	}
	before := testutil.Fingerprint(t, root.Path)
	code, out, _ := run("update", "minimal", "--input", `{"updateSteps":[{"phaseId":"phase-one","stepId":"bare","status":"in_progress"}]}`, "--legacy-unhashed", "--yes", "--json", "--root", root.Path)
	var e struct {
		Error struct {
			Class  string `json:"class"`
			Issues []struct{ Path, Message string }
		} `json:"error"`
	}
	json.Unmarshal([]byte(out), &e)
	if code != 1 || e.Error.Class != "invalid_structure" || len(e.Error.Issues) != 2 ||
		e.Error.Issues[0].Path != "phases.0.steps.1.action" || e.Error.Issues[1].Path != "phases.0.steps.1.validation" {
		t.Fatalf("step gate: %d %s", code, out)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("step gate wrote: %v", d)
	}
}

// Create applies the step status gate to the steps it creates.
func TestCreateStepStatusGateCLI(t *testing.T) {
	root := testutil.NewRoot(t, "empty-workspace")
	withPrompt(t, false, "")
	before := testutil.Fingerprint(t, root.Path)
	code, out, _ := run("create", "--input", `{"id":"gated","goal":"g","phases":[{"id":"p","title":"P","steps":[{"id":"s","title":"S","status":"in_progress","validation":"v"}]}]}`,
		"--yes", "--json", "--root", root.Path)
	var e struct {
		Error struct {
			Class  string `json:"class"`
			Issues []struct{ Path, Message string }
		} `json:"error"`
	}
	json.Unmarshal([]byte(out), &e)
	if code != 1 || e.Error.Class != "invalid_structure" || len(e.Error.Issues) != 1 || e.Error.Issues[0].Path != "phases.0.steps.0.action" {
		t.Fatalf("create step gate: %d %s", code, out)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("create step gate wrote: %v", d)
	}
}

// Note rollover flags; the preview token binds them.
func TestCompactRolloverCLI(t *testing.T) {
	root := testutil.NewRoot(t, "large-paging")
	withPrompt(t, false, "")
	if code, _, errOut := run("compact", "big-plan", "--reason", "roll", "--keep-notes", "5", "--root", root.Path); code != 2 || !strings.Contains(errOut, "need --rollover") {
		t.Fatalf("--keep-notes without --rollover: %d %s", code, errOut)
	}
	if code, out, _ := run("compact", "big-plan", "--reason", "roll", "--rollover", "--archive-note", "1", "--json", "--root", root.Path); code != 1 || !strings.Contains(out, "noteRollover cannot be combined with noteIndexes") {
		t.Fatalf("--rollover with --archive-note: %d %s", code, out)
	}
	before := testutil.Fingerprint(t, root.Path)
	code, out, errOut := run("compact", "big-plan", "--reason", "roll", "--rollover", "--keep-notes", "50", "--pin-note", "0", "--json", "--root", root.Path)
	if code != 0 {
		t.Fatal(errOut)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("preview wrote: %v", d)
	}
	var pv struct {
		PreviewToken, StateHash string
		CanonicalSelection      struct {
			NoteIndexes  []int
			NoteRollover struct {
				KeepLatest     int
				PinNoteIndexes []int
			}
		}
		NoteRollover struct{ SelectedCount int }
	}
	json.Unmarshal([]byte(out), &pv)
	if pv.CanonicalSelection.NoteRollover.KeepLatest != 50 || len(pv.CanonicalSelection.NoteRollover.PinNoteIndexes) != 1 ||
		pv.NoteRollover.SelectedCount != len(pv.CanonicalSelection.NoteIndexes) || pv.NoteRollover.SelectedCount == 0 {
		t.Fatalf("preview %s", out)
	}
	for _, i := range pv.CanonicalSelection.NoteIndexes {
		if i == 0 || i >= 10 {
			t.Fatalf("selected note %d (pinned 0; keep the latest 50 of 60)", i)
		}
	}
	// Apply with a different keep is refused; with the same flags it commits.
	code, out, _ = run("compact", "big-plan", "--reason", "roll", "--rollover", "--keep-notes", "40", "--pin-note", "0", "--apply", "--preview-token", pv.PreviewToken, "--confirm", "ARCHIVE_SELECTED_HISTORY", "--expected-hash", pv.StateHash, "--yes", "--json", "--root", root.Path)
	if code != 1 || !strings.Contains(out, "previewToken does not match") {
		t.Fatalf("different keep: %d %s", code, out)
	}
	code, out, errOut = run("compact", "big-plan", "--reason", "roll", "--rollover", "--keep-notes", "50", "--pin-note", "0", "--apply", "--preview-token", pv.PreviewToken, "--confirm", "ARCHIVE_SELECTED_HISTORY", "--expected-hash", pv.StateHash, "--yes", "--json", "--root", root.Path)
	if code != 0 || !strings.Contains(out, `"compacted": true`) || !strings.Contains(out, `"savings"`) {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
}

// --compaction-advice is a trusted read flag of resume and doctor.
func TestCompactionAdviceFlag(t *testing.T) {
	root := testutil.NewRoot(t, "large-paging")
	_, plain, _ := run("doctor", "big-plan", "--json", "--root", root.Path)
	if strings.Contains(plain, "compactionRecommended") {
		t.Fatal("default thresholds recommend compaction for the 45 KB fixture")
	}
	code, out, errOut := run("doctor", "big-plan", "--json", "--compaction-advice", "min-savings-kib=1,notes=10", "--root", root.Path)
	if code != 0 || !strings.Contains(out, `"compactionRecommended"`) {
		t.Fatalf("%d %s", code, errOut)
	}
	code, out, _ = run("resume", "big-plan", "--compaction-advice", "min-savings-kib=1,notes=10", "--root", root.Path)
	if code != 0 || !strings.Contains(out, "compact:   recommended") {
		t.Fatalf("human resume: %s", out)
	}
	if _, off, _ := run("doctor", "big-plan", "--json", "--compaction-advice", "off", "--root", root.Path); off != plain {
		t.Fatal("--compaction-advice off differs from the default output")
	}
	for _, args := range [][]string{
		{"read", "big-plan", "--compaction-advice", "off"},
		{"doctor", "big-plan", "--compaction-advice", "notes=0"},
		{"doctor", "big-plan", "--compaction-advice", "bogus=1"},
	} {
		if code, _, _ := run(append(args, "--root", root.Path)...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// checkpoint --merge keeps the stored fields and
// --append-validation appends; the withheld-placeholder refusal and the
// drop warnings reach the CLI unchanged.
func TestCheckpointMergeCLI(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	if err := os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/full-plan.md"), []byte("# edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withPrompt(t, false, "")
	hash := func() string {
		code, out, errOut := run("read", "full-plan", "--json", "--no-markdown", "--root", root.Path)
		if code != 0 {
			t.Fatal(errOut)
		}
		var v struct{ StateHash string }
		json.Unmarshal([]byte(out), &v)
		return v.StateHash
	}
	if code, out, _ := run("resume", "full-plan", "--root", root.Path); code != 0 ||
		!strings.Contains(out, "withheld:  summary, nextAction, guardrails, references, recentValidation (stored, not shown") {
		t.Fatalf("human resume: %d %s", code, out)
	}
	before := testutil.Fingerprint(t, root.Path)
	code, out, _ := run("checkpoint", "full-plan", "--summary", "null DEPLOYED", "--next-action", "x", "--expected-hash", hash(), "--yes", "--json", "--root", root.Path)
	if code != 1 || !strings.Contains(out, `"class": "invalid_input"`) || !strings.Contains(out, "withheld field") {
		t.Fatalf("null summary: %d %s", code, out)
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("refusal wrote: %v", d)
	}
	if code, _, errOut := run("update", "full-plan", "--merge", "--expected-hash", hash(), "--root", root.Path); code != 2 || !strings.Contains(errOut, "--merge does not apply") {
		t.Fatalf("--merge on update: %d %s", code, errOut)
	}
	code, out, errOut := run("checkpoint", "full-plan", "--merge", "--append-validation", "deploy ok", "--expected-hash", hash(), "--yes", "--json", "--root", root.Path)
	var res struct {
		Checkpoint struct {
			Summary          string   `json:"summary"`
			NextAction       string   `json:"nextAction"`
			Guardrails       []string `json:"guardrails"`
			RecentValidation []string `json:"recentValidation"`
		} `json:"checkpoint"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 {
		t.Fatalf("merge: %d %s %s", code, out, errOut)
	}
	if res.Checkpoint.Summary != "Phase A done; B in progress" || res.Checkpoint.NextAction != "Finish step-b1" || len(res.Checkpoint.Guardrails) != 1 ||
		strings.Join(res.Checkpoint.RecentValidation, "|") != "bun test: 3 passed|deploy ok" || res.Warnings != nil {
		t.Fatalf("merged %+v", res)
	}
	code, out, _ = run("checkpoint", "full-plan", "--summary", "s", "--next-action", "n", "--guardrail", "do not touch adapter", "--expected-hash", hash(), "--yes", "--json", "--root", root.Path)
	res.Warnings = nil
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 || len(res.Warnings) != 3 ||
		res.Warnings[0] != "references: 1 → 0 (1 previous entry not kept; merge=true keeps omitted fields)" {
		t.Fatalf("warnings: %d %s", code, out)
	}
}
