package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/resume"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

const fullPlan = "full-plan"

var allWithheld = []string{"summary", "nextAction", "guardrails", "references", "recentValidation"}

// nativeMutate runs one tool call on the native surface (the adapter's).
func nativeMutate(t *testing.T, e *Engine, tool, in string) (Output, error, int) {
	t.Helper()
	name := strings.TrimPrefix(tool, "workplan_")
	auth := &countingAuth{}
	data, err := input.ParseMutationInput(name, parseT(t, in), input.SurfaceNative)
	if err != nil {
		return Output{}, err, 0
	}
	p, err := e.Prepare(name, data)
	if err != nil {
		return Output{}, err, 0
	}
	out, err := e.Execute(context.Background(), p, auth, ExecOptions{})
	return out, err, auth.n
}

func resumeDefault(t *testing.T, e *Engine, id string) (ojson.Value, string) {
	t.Helper()
	v, text, err := e.Resume(input.ResumeInput{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	return v, text
}

func makeStale(t *testing.T, root testutil.Root) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/full-plan.md"), []byte("# edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCheckpoint(t *testing.T, root string) map[string]any {
	t.Helper()
	p, err := ojson.Parse(mustRead(t, filepath.Join(root, ".opencode/workplan/full-plan.checkpoint.json")))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for _, m := range p.Value.Members() {
		switch m.Value.Kind() {
		case ojson.String:
			out[m.Key] = m.Value.Str()
		case ojson.Array:
			out[m.Key] = strs(m.Value)
		default:
			out[m.Key] = string(ojson.Compact(m.Value))
		}
	}
	return out
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestResumeWithheld: a stale or legacy checkpoint's withheld
// fields are named, the instruction points at the checkpoint file and
// merge=true, and the null/empty values stay for compatibility. Fresh,
// missing and invalid checkpoints are unchanged.
func TestResumeWithheld(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	v, _ := resumeDefault(t, e, fullPlan)
	if cp, _ := v.Get("checkpoint"); testutil.Has(cp, "withheld") || strMember(v, "instruction") != instructionFresh {
		t.Fatalf("fresh checkpoint: %s / %q", ojson.Compact(cp), strMember(v, "instruction"))
	}
	makeStale(t, root)
	v, text := resumeDefault(t, e, fullPlan)
	cp, _ := v.Get("checkpoint")
	w, _ := cp.Get("withheld")
	if !reflect.DeepEqual(strs(w), allWithheld) {
		t.Fatalf("withheld %v", strs(w))
	}
	// Compatibility: the reference shapes stay (null prose, empty lists,
	// totals).
	for _, k := range []string{"summary", "nextAction"} {
		if x, _ := cp.Get(k); x.Kind() != ojson.Null {
			t.Fatalf("%s %s", k, ojson.Compact(x))
		}
	}
	for _, k := range []string{"guardrails", "references", "recentValidation"} {
		if x, _ := cp.Get(k); len(x.Elems()) != 0 {
			t.Fatalf("%s shown: %s", k, ojson.Compact(x))
		}
	}
	if numMember(cp, "guardrailsTotal") != 1 || numMember(cp, "referencesTotal") != 1 {
		t.Fatalf("totals changed: %s", ojson.Compact(cp))
	}
	// The member sits right after freshness.
	var keys []string
	for _, m := range cp.Members() {
		keys = append(keys, m.Key)
	}
	if keys[2] != "freshness" || keys[3] != "withheld" {
		t.Fatalf("checkpoint keys %v", keys)
	}
	ins := strMember(v, "instruction")
	if ins != instructionWithheld(".opencode/workplan/full-plan.checkpoint.json") ||
		!strings.HasPrefix(ins, instructionStale+" ") || !strings.Contains(ins, "(workplan_read omits it)") ||
		!strings.Contains(ins, "merge=true") {
		t.Fatalf("instruction %q", ins)
	}
	// workplan_read indeed does not return the checkpoint (the instruction
	// names the file instead).
	rv, err := e.Read(input.ReadInput{ID: fullPlan})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ojson.Compact(rv)), "Phase A done; B in progress") {
		t.Fatal("workplan_read returns the checkpoint summary; update the instruction")
	}
	// Naming the withheld fields costs nothing here: the packet is the
	// one without them (no withheld member, the plain stale instruction)
	// plus the member and the longer instruction, at the same level.
	_, plainText := resumeEdited(t, e, input.ResumeInput{ID: fullPlan}, withoutWithheld)
	if err := compareWithoutWithheld(v, text, plainText); err != nil {
		t.Fatal(err)
	}

	// Legacy v1: named too.
	root2 := testutil.NewRoot(t, "checkpoint-legacy-v1")
	v2, _ := resumeDefault(t, mustEngine(t, root2.Path), "cp-v1")
	cp2, _ := v2.Get("checkpoint")
	if w2, _ := cp2.Get("withheld"); strMember(cp2, "freshness") != FreshnessLegacy || !reflect.DeepEqual(strs(w2), []string{"summary", "nextAction", "guardrails"}) {
		t.Fatalf("legacy %s", ojson.Compact(cp2))
	}
	// Missing and invalid checkpoints withhold nothing: no member, the
	// plain stale instruction.
	root3 := testutil.NewRoot(t, "minimal-valid")
	v3, _ := resumeDefault(t, mustEngine(t, root3.Path), "minimal")
	if cp3, _ := v3.Get("checkpoint"); testutil.Has(cp3, "withheld") || strMember(v3, "instruction") != instructionStale {
		t.Fatalf("missing checkpoint %s", ojson.Compact(cp3))
	}
	root4 := testutil.NewRoot(t, "checkpoint-corrupt")
	ids, _ := filepath.Glob(filepath.Join(root4.Path, ".opencode/workplan/*.checkpoint.json"))
	id4 := strings.TrimSuffix(filepath.Base(ids[0]), ".checkpoint.json")
	v4, _ := resumeDefault(t, mustEngine(t, root4.Path), id4)
	if cp4, _ := v4.Get("checkpoint"); testutil.Has(cp4, "withheld") || strMember(v4, "instruction") != instructionStale {
		t.Fatalf("invalid checkpoint %s", ojson.Compact(cp4))
	}
}

// resumeEdited renders the resume packet of in after edit changes the
// packet model: the same budget policy over a different model.
func resumeEdited(t *testing.T, e *Engine, in input.ResumeInput, edit func(*resume.Packet)) (ojson.Value, string) {
	t.Helper()
	s, err := e.load(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.resumePacket(s, in)
	if err != nil {
		t.Fatal(err)
	}
	edit(m)
	v, text, err := m.Render()
	if err != nil {
		t.Fatal(err)
	}
	return v, text
}

// withoutWithheld is the packet model without the withheld fields.
func withoutWithheld(m *resume.Packet) {
	m.Withheld = nil
	m.Instruction = instructionStale
}

// compareWithoutWithheld reports whether v is the plain packet plus the
// withheld member and the longer instruction, at the same level.
func compareWithoutWithheld(v ojson.Value, text, plainText string) error {
	cp, _ := v.Get("checkpoint")
	inv := testutil.Replace(testutil.Replace(v, "checkpoint", testutil.Without(cp, "withheld")), "instruction", ojson.StringValue(instructionStale))
	s := string(ojson.Pretty(inv))
	if !strings.HasPrefix(text, "{\n") {
		s = string(ojson.Compact(inv))
	}
	if s != plainText {
		return fmt.Errorf("packet minus the withheld fields differs from the packet without them\n%s", testutil.FirstDiff(s, plainText))
	}
	return nil
}

// TestResumeWithheldBudget: the withheld list is a pinned safety item.
// Every packet fits maxChars, always carries the complete list, and
// packets that differ from the packet without it by more than the member
// and the instruction are counted (the cost of the pinned item at tight
// budgets).
func TestResumeWithheldBudget(t *testing.T) {
	e, root := seedGraphRoadmap(t)
	mustMutate(t, e, "workplan_checkpoint", fmt.Sprintf(`{"id":"roadmap","merge":true,"guardrails":[%q,%q],"references":["docs/a.md","docs/b.md"],"recentValidation":[%q]}`,
		words("Guardrail one", 200), words("Guardrail two", 200), words("go test", 150)))
	os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/roadmap.md"), []byte("# edited\n"), 0o644)
	same, costly, fewerItems := 0, 0, 0
	for max := 4096; max <= 16000; max += budgetStep(max) {
		for _, limit := range []int{1, 8, 20} {
			v, text, err := e.Resume(input.ResumeInput{ID: "roadmap", MaxChars: &max, Limit: &limit})
			if err != nil {
				t.Fatal(err)
			}
			if ojson.UTF16Len(text) > max {
				t.Fatalf("%d/%d: %d over budget", max, limit, ojson.UTF16Len(text))
			}
			cp, _ := v.Get("checkpoint")
			if w, _ := cp.Get("withheld"); !reflect.DeepEqual(strs(w), allWithheld) {
				t.Fatalf("%d/%d: withheld %v", max, limit, strs(w))
			}
			pv, plainText := resumeEdited(t, e, input.ResumeInput{ID: "roadmap", MaxChars: &max, Limit: &limit}, withoutWithheld)
			if numMember(v, "page", "returned") < numMember(pv, "page", "returned") {
				fewerItems++
			}
			if compareWithoutWithheld(v, text, plainText) == nil {
				same++
			} else {
				costly++
			}
		}
	}
	t.Logf("withheld packets at the level without them: %d, at a lower level (pinned item cost): %d, of which with fewer page items: %d", same, costly, fewerItems)
	if same == 0 {
		t.Fatal("no packet kept the level of the packet without the withheld fields")
	}
}

// TestWithheldPlaceholderRefused: a copied withheld value is refused on
// both surfaces,
// before authorization, class invalid_input, nothing written.
func TestWithheldPlaceholderRefused(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	h := stateHashFor(t, e, fullPlan)
	refused := []string{"null", "  null  ", "null DEPLOYED to staging", "undefined", "undefined next", "\tnull x"}
	accepted := []string{"nullable config", "Null value", "NULL x", "null-safe", "the null case", "nullx", "undefinedness"}
	for _, field := range []string{"summary", "nextAction"} {
		for _, val := range refused {
			in := map[string]string{"summary": "Real summary", "nextAction": "Real next"}
			in[field] = val
			body := fmt.Sprintf(`{"id":%q,"summary":%q,"nextAction":%q,"expectedHash":%q}`, fullPlan, in["summary"], in["nextAction"], h)
			for _, surface := range []string{"native", "core"} {
				before := testutil.Fingerprint(t, root.Path)
				var err error
				var n int
				if surface == "native" {
					_, err, n = nativeMutate(t, e, "workplan_checkpoint", body)
				} else {
					_, err, n = mutateErr(t, e, "workplan_checkpoint", body)
				}
				var ie *input.InputError
				if !errors.As(err, &ie) || ErrorClass(err) != "invalid_input" || n != 0 {
					t.Fatalf("%s %s=%q: err %v class %s auths %d", surface, field, val, err, ErrorClass(err), n)
				}
				want := field + ": Checkpoint " + field + " starts with null/undefined"
				if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "merge=true") || strings.Contains(ie.Issues[0].Message, "; ") {
					t.Fatalf("message %q", err.Error())
				}
				if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
					t.Fatalf("refusal wrote %v", d)
				}
			}
		}
	}
	for _, val := range accepted {
		h = stateHashFor(t, e, fullPlan)
		body := fmt.Sprintf(`{"id":%q,"summary":%q,"nextAction":"n","expectedHash":%q,"merge":true}`, fullPlan, val, h)
		if _, err, _ := nativeMutate(t, e, "workplan_checkpoint", body); err != nil {
			t.Fatalf("%q refused: %v", val, err)
		}
	}
}

// TestCheckpointDropWarnings: counts before → after for
// each list that loses stored entries; shorter prose is not a warning.
func TestCheckpointDropWarnings(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	// Keeps every list (and a much shorter summary): no warnings member.
	out := mustMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","summary":"s","nextAction":"n","blockers":["waiting on review"],"recentValidation":["bun test: 3 passed","go test ok"],"guardrails":["do not touch adapter"],"references":["docs/spec-a.md"]}`)
	if testutil.Has(out.Value, "warnings") {
		t.Fatalf("warnings %s", ojson.Compact(out.Value))
	}
	// Drops: guardrails 1 → 0, references 1 → 1 (replaced),
	// recentValidation 2 → 1, blockers 1 → 0.
	out = mustMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","summary":"s","nextAction":"n","recentValidation":["go test ok"],"references":["docs/other.md"]}`)
	w, _ := out.Value.Get("warnings")
	want := []string{
		"guardrails: 1 → 0 (1 previous entry not kept; merge=true keeps omitted fields)",
		"references: 1 → 1 (1 previous entry not kept; merge=true keeps omitted fields)",
		"recentValidation: 2 → 1 (1 previous entry not kept; merge=true keeps omitted fields)",
		"blockers: 1 → 0 (1 previous entry not kept; merge=true keeps omitted fields)",
	}
	if !reflect.DeepEqual(strs(w), want) {
		t.Fatalf("warnings %q", strs(w))
	}
	var keys []string
	for _, m := range out.Value.Members() {
		keys = append(keys, m.Key)
	}
	if keys[len(keys)-1] != "warnings" || keys[len(keys)-2] != "directorySync" {
		t.Fatalf("result keys %v", keys)
	}
	// No stored checkpoint: nothing to drop.
	root2 := testutil.NewRoot(t, "minimal-valid")
	out = mustMutate(t, mustEngine(t, root2.Path), "workplan_checkpoint", `{"id":"minimal","summary":"s","nextAction":"n"}`)
	if testutil.Has(out.Value, "warnings") {
		t.Fatalf("first checkpoint warned: %s", ojson.Compact(out.Value))
	}
	// Two entries dropped from a legacy checkpoint: plural and counts.
	if got := checkpointDropWarnings(&model.Checkpoint{Guardrails: []string{"a", "b", "c"}}, &model.Checkpoint{Guardrails: []string{"c"}}); len(got) != 1 ||
		got[0] != "guardrails: 3 → 1 (2 previous entries not kept; merge=true keeps omitted fields)" {
		t.Fatalf("plural %q", got)
	}
}

// TestCheckpointMerge: merge keeps omitted fields, given fields
// replace theirs, appendValidation appends (deduplicated), the result
// binds the current plan state, and missing/legacy stored checkpoints
// behave as documented.
func TestCheckpointMerge(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	before := readCheckpoint(t, root.Path)
	makeStale(t, root)
	out, err, n := nativeMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","merge":true,"appendValidation":"go test ./... passed","expectedHash":"`+stateHashFor(t, e, fullPlan)+`"}`)
	if err != nil || n != 1 {
		t.Fatalf("merge refresh: %v (auths %d)", err, n)
	}
	if testutil.Has(out.Value, "warnings") {
		t.Fatalf("merge refresh warned: %s", ojson.Compact(out.Value))
	}
	after := readCheckpoint(t, root.Path)
	for _, k := range []string{"summary", "nextAction", "blockers", "guardrails", "references", "current", "createdAt"} {
		if !reflect.DeepEqual(before[k], after[k]) {
			t.Fatalf("%s: %v → %v", k, before[k], after[k])
		}
	}
	if want := []string{"bun test: 3 passed", "go test ./... passed"}; !reflect.DeepEqual(after["recentValidation"], want) {
		t.Fatalf("recentValidation %v", after["recentValidation"])
	}
	// Fresh again: bound to the current plan state.
	v, _ := resumeDefault(t, e, fullPlan)
	if cp, _ := v.Get("checkpoint"); !member(cp, "fresh").Bool() || strMember(cp, "summary") != "Phase A done; B in progress" {
		t.Fatalf("after merge %s", ojson.Compact(cp))
	}
	// Duplicates are skipped; a string array appends in order.
	mustMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","merge":true,"appendValidation":["go test ./... passed","  ","lint ok","lint ok"]}`)
	if got := readCheckpoint(t, root.Path)["recentValidation"]; !reflect.DeepEqual(got, []string{"bun test: 3 passed", "go test ./... passed", "lint ok"}) {
		t.Fatalf("appended %v", got)
	}
	// Given fields replace theirs; an explicit empty list clears (and warns).
	out = mustMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","merge":true,"summary":"New summary","guardrails":[]}`)
	after = readCheckpoint(t, root.Path)
	if after["summary"] != "New summary" || after["nextAction"] != before["nextAction"] || len(after["guardrails"].([]string)) != 0 ||
		!reflect.DeepEqual(after["references"], before["references"]) {
		t.Fatalf("partial merge %v", after)
	}
	if w, _ := out.Value.Get("warnings"); !reflect.DeepEqual(strs(w), []string{"guardrails: 1 → 0 (1 previous entry not kept; merge=true keeps omitted fields)"}) {
		t.Fatalf("warnings %q", strs(w))
	}
	// The stored position no longer resolves (step completed): re-derived.
	mustMutate(t, e, "workplan_update", `{"id":"full-plan","updateSteps":[{"phaseId":"phase-b","stepId":"step-b1","status":"completed"}]}`)
	mustMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","merge":true}`)
	if cur := readCheckpoint(t, root.Path)["current"].(string); !strings.Contains(cur, `"stepId":"step-b2"`) {
		t.Fatalf("position %s", cur)
	}
	// An explicit position wins over the stored one.
	mustMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","merge":true,"phaseId":"phase-c"}`)
	if cur := readCheckpoint(t, root.Path)["current"].(string); !strings.Contains(cur, `"stepId":"step-c1"`) {
		t.Fatalf("position %s", cur)
	}

	// Without merge the fields stay required (the reference message).
	_, err, n = nativeMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","expectedHash":"`+stateHashFor(t, e, fullPlan)+`"}`)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "summary: Invalid input: expected string, received undefined") {
		t.Fatalf("no merge: %v", err)
	}
	for _, bad := range []string{`"merge":false`, `"merge":"yes"`} {
		_, err, _ = nativeMutate(t, e, "workplan_checkpoint", `{"id":"full-plan",`+bad+`,"expectedHash":"`+stateHashFor(t, e, fullPlan)+`"}`)
		if err == nil || ErrorClass(err) != "invalid_input" {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	_, err, _ = nativeMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","merge":true,"appendValidation":3,"expectedHash":"`+stateHashFor(t, e, fullPlan)+`"}`)
	if err == nil || !strings.Contains(err.Error(), "appendValidation: Invalid input: expected string or array, received number") {
		t.Fatalf("appendValidation type: %v", err)
	}
	_, err, _ = nativeMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","merge":true,"appendValidation":["ok",1],"expectedHash":"`+stateHashFor(t, e, fullPlan)+`"}`)
	if err == nil || !strings.Contains(err.Error(), "appendValidation.1: Invalid input: expected string, received number") {
		t.Fatalf("appendValidation element: %v", err)
	}
	// appendValidation without merge appends to the given list.
	mustMutate(t, e, "workplan_checkpoint", `{"id":"full-plan","summary":"s","nextAction":"n","recentValidation":["a"],"appendValidation":"b"}`)
	if got := readCheckpoint(t, root.Path)["recentValidation"]; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("non-merge append %v", got)
	}

	// Missing checkpoint: merges as empty; summary/nextAction then needed.
	root2 := testutil.NewRoot(t, "minimal-valid")
	e2 := mustEngine(t, root2.Path)
	before2 := testutil.Fingerprint(t, root2.Path)
	_, err, n = nativeMutate(t, e2, "workplan_checkpoint", `{"id":"minimal","merge":true,"nextAction":"n","expectedHash":"`+stateHashFor(t, e2, "minimal")+`"}`)
	var ie *input.InputError
	if !errors.As(err, &ie) || n != 0 || ErrorClass(err) != "invalid_input" ||
		err.Error() != "Invalid checkpoint input: summary: merge=true keeps the stored summary, but there is no readable stored checkpoint — pass summary" {
		t.Fatalf("merge on missing: %v (auths %d)", err, n)
	}
	if d := testutil.DiffFingerprints(before2, testutil.Fingerprint(t, root2.Path)); len(d) > 0 {
		t.Fatalf("refusal wrote %v", d)
	}
	out = mustMutate(t, e2, "workplan_checkpoint", `{"id":"minimal","merge":true,"summary":"s","nextAction":"n","appendValidation":"v"}`)
	if cp, _ := out.Value.Get("checkpoint"); strMember(cp, "summary") != "s" || !reflect.DeepEqual(strs(member(cp, "recentValidation")), []string{"v"}) {
		t.Fatalf("merge on missing %s", ojson.Compact(cp))
	}

	// Legacy v1: fields carried over into a v2 checkpoint.
	root3 := testutil.NewRoot(t, "checkpoint-legacy-v1")
	e3 := mustEngine(t, root3.Path)
	v3, _ := resumeDefault(t, e3, "cp-v1")
	_ = v3
	legacy, err := ojson.Parse(mustRead(t, filepath.Join(root3.Path, ".opencode/workplan/cp-v1.checkpoint.json")))
	if err != nil {
		t.Fatal(err)
	}
	out = mustMutate(t, e3, "workplan_checkpoint", `{"id":"cp-v1","merge":true}`)
	cp3, _ := out.Value.Get("checkpoint")
	if numMember(cp3, "schemaVersion") != 2 || strMember(cp3, "summary") != strMember(legacy.Value, "summary") ||
		strMember(cp3, "nextAction") != strMember(legacy.Value, "nextAction") ||
		!reflect.DeepEqual(strs(member(cp3, "guardrails")), strs(member(legacy.Value, "guardrails"))) ||
		!reflect.DeepEqual(strs(member(cp3, "blockers")), strs(member(legacy.Value, "blockers"))) {
		t.Fatalf("legacy merge %s", ojson.Compact(cp3))
	}
	if v, _ := resumeDefault(t, e3, "cp-v1"); !member(member(v, "checkpoint"), "fresh").Bool() {
		t.Fatal("legacy merge is not fresh")
	}
}

// TestCheckpointIncidentSequence replays the real-use incident: a stale
// checkpoint, the resume view, a naive rebuild from that view (refused on
// "null …", warned on the drops), and the merge-mode refresh that keeps
// every field and appends one validation line.
func TestCheckpointIncidentSequence(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	stored := readCheckpoint(t, root.Path)
	makeStale(t, root)
	view, _ := resumeDefault(t, e, fullPlan)
	cp, _ := view.Get("checkpoint")
	if w, _ := cp.Get("withheld"); !reflect.DeepEqual(strs(w), allWithheld) {
		t.Fatalf("withheld %v", strs(w))
	}
	h := strMember(view, "hashes", "stateHash")
	// The naive rebuild: the view's null summary concatenated with news,
	// one validation line, empty guardrails/references.
	naive := func(summary string) string {
		return fmt.Sprintf(`{"id":"full-plan","summary":%q,"nextAction":"Deploy","phaseId":"phase-b","stepId":"step-b1","blockers":["waiting on review"],"recentValidation":["deploy ok"],"guardrails":[],"references":[],"expectedHash":%q}`, summary, h)
	}
	summaryV, _ := cp.Get("summary")
	_, err, n := nativeMutate(t, e, "workplan_checkpoint", naive(string(ojson.Compact(summaryV))+" DEPLOYED to staging"))
	if err == nil || n != 0 || ErrorClass(err) != "invalid_input" || !strings.Contains(err.Error(), "withheld field") {
		t.Fatalf("null rebuild: %v (auths %d)", err, n)
	}
	if got := readCheckpoint(t, root.Path); !reflect.DeepEqual(got, stored) {
		t.Fatal("refused rebuild changed the checkpoint")
	}
	// The merge-mode refresh (on a copy of the same state).
	root2 := testutil.NewRoot(t, "full-valid")
	e2 := mustEngine(t, root2.Path)
	makeStale(t, root2)
	out, err, _ := nativeMutate(t, e2, "workplan_checkpoint", `{"id":"full-plan","merge":true,"appendValidation":"deploy ok","expectedHash":"`+stateHashFor(t, e2, fullPlan)+`"}`)
	if err != nil || testutil.Has(out.Value, "warnings") {
		t.Fatalf("merge refresh: %v %s", err, ojson.Compact(out.Value))
	}
	got := readCheckpoint(t, root2.Path)
	for _, k := range []string{"summary", "nextAction", "blockers", "guardrails", "references", "current"} {
		if !reflect.DeepEqual(got[k], stored[k]) {
			t.Fatalf("%s lost: %v → %v", k, stored[k], got[k])
		}
	}
	if !reflect.DeepEqual(got["recentValidation"], append(append([]string{}, stored["recentValidation"].([]string)...), "deploy ok")) {
		t.Fatalf("recentValidation %v", got["recentValidation"])
	}
	// The naive rebuild without the null prefix is written, with warnings.
	out, err, _ = nativeMutate(t, e, "workplan_checkpoint", naive("DEPLOYED to staging"))
	if err != nil {
		t.Fatal(err)
	}
	w, _ := out.Value.Get("warnings")
	want := []string{
		"guardrails: 1 → 0 (1 previous entry not kept; merge=true keeps omitted fields)",
		"references: 1 → 0 (1 previous entry not kept; merge=true keeps omitted fields)",
		"recentValidation: 1 → 1 (1 previous entry not kept; merge=true keeps omitted fields)",
	}
	if !reflect.DeepEqual(strs(w), want) {
		t.Fatalf("warnings %q", strs(w))
	}
}

func member(v ojson.Value, key string) ojson.Value { x, _ := v.Get(key); return x }

// TestStaleCheckpointDetail: doctor names what changed.
func TestStaleCheckpointDetail(t *testing.T) {
	e, root := seedRoadmap(t)
	os.MkdirAll(filepath.Join(root.Path, "docs"), 0o755)
	os.WriteFile(filepath.Join(root.Path, "docs", "spec.md"), []byte("# spec\n"), 0o644)
	mustMutate(t, e, "workplan_update", `{"id":"roadmap","addSpecFiles":["docs/spec.md"]}`)
	mustMutate(t, e, "workplan_checkpoint", `{"id":"roadmap","summary":"s","nextAction":"n"}`)
	os.WriteFile(filepath.Join(root.Path, "docs", "spec.md"), []byte("# spec v2\n"), 0o644)
	doc, _ := e.Doctor(input.DoctorInput{})
	plans, _ := doc.Get("plans")
	issues := strs(func() ojson.Value { x, _ := plans.Elems()[0].Get("issues"); return x }())
	if !containsPrefix(issues, "checkpoint: "+msgStale+". Changed since the checkpoint: docs/spec.md changed (checkpoint sha256 ") ||
		containsPrefix(issues, "roadmap.json changed") {
		t.Fatalf("issues %v", issues)
	}
	os.Remove(filepath.Join(root.Path, "docs", "spec.md"))
	doc, _ = e.Doctor(input.DoctorInput{})
	if !strings.Contains(string(ojson.Compact(doc)), "docs/spec.md changed (checkpoint sha256 ") || !strings.Contains(string(ojson.Compact(doc)), ", now missing)") {
		t.Fatalf("doctor: %s", ojson.Compact(doc))
	}
}

// TestResumeStaleDiagnostic: a stale checkpoint's resume
// diagnostic names what changed (at most three paths, then "+N more").
func TestResumeStaleDiagnostic(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	resume := func(max int) (string, ojson.Value) {
		v, text, err := e.Resume(input.ResumeInput{ID: "full-plan", MaxChars: &max})
		if err != nil {
			t.Fatal(err)
		}
		cp, _ := v.Get("checkpoint")
		d, _ := cp.Get("diagnostic")
		return text, d
	}
	if _, d := resume(12000); d.Kind() != ojson.Null {
		t.Fatalf("fresh checkpoint diagnostic %s", ojson.Compact(d))
	}
	os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/full-plan.md"), []byte("# edited\n"), 0o644)
	if _, d := resume(12000); d.Str() != "changed: .opencode/workplan/full-plan.md" {
		t.Fatalf("diagnostic %q", d.Str())
	}
	// More than three changed links: three paths and "+N more".
	for _, f := range []string{"a", "b", "c"} {
		os.WriteFile(filepath.Join(root.Path, f+".md"), []byte(f), 0o644)
	}
	s, _ := snapshot.Load(root.Path, "full-plan", snapshot.DefaultLimits)
	p := s.Plan
	p.SpecFiles = []string{"a.md", "b.md", "c.md"}
	os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/full-plan.json"), p.EncodeStored(), 0o644)
	for _, max := range []int{4096, 12000} {
		text, d := resume(max)
		if !regexp.MustCompile(`^changed: [^,]+, [^,]+, [^,]+ \+[1-9][0-9]* more$`).MatchString(d.Str()) {
			t.Fatalf("diagnostic %q", d.Str())
		}
		if ojson.UTF16Len(text) > max {
			t.Fatalf("packet %d over budget %d", ojson.UTF16Len(text), max)
		}
	}
	if got := staleDiagnostic([]string{strings.Repeat("x", 300)}); ojson.UTF16Len(got) != maxDiagnosticUnits || !strings.HasSuffix(got, "…") {
		t.Fatalf("uncapped diagnostic (%d units)", ojson.UTF16Len(got))
	}
	// Legacy v1 checkpoints keep a null diagnostic.
	root2 := testutil.NewRoot(t, "checkpoint-legacy-v1")
	e2 := mustEngine(t, root2.Path)
	ids, _ := filepath.Glob(filepath.Join(root2.Path, ".opencode/workplan/*.checkpoint.json"))
	id := strings.TrimSuffix(filepath.Base(ids[0]), ".checkpoint.json")
	v, _, err := e2.Resume(input.ResumeInput{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if cp, _ := v.Get("checkpoint"); strMember(cp, "freshness") != FreshnessLegacy || func() bool { d, _ := cp.Get("diagnostic"); return d.Kind() != ojson.Null }() {
		t.Fatalf("legacy checkpoint %s", ojson.Compact(cp))
	}
}
