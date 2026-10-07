package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// Checkpoint guidance: resume names what changed in a stale checkpoint
// and which stored fields it withholds; checkpoint writes warn about
// dropped list entries.

// instructionStale is the reference's instruction for guidance that is
// not fresh.
const instructionStale = "Checkpoint guidance is not fresh. Reconfirm unverified guardrails, blockers and references before relying on them; nextAction is withheld."

// ---- stale diagnostic ----

var moreRe = regexp.MustCompile(` \+(\d+) more$`)

// staleDiagnosticInverse maps resume's compact stale diagnostic back to
// the reference's null, after restating it.
func staleDiagnosticInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	cp, _ := parsed.Value.Get("checkpoint")
	d, _ := cp.Get("diagnostic")
	if v.Call.Tool != "workplan_resume" || d.Kind() != ojson.String || !strings.HasPrefix(d.Str(), "changed: ") {
		t.Fatal("stale-diagnostic needs a resume packet with a stale diagnostic")
	}
	if err := restateDiagnostic(root.Path, strings.ToLower(inputID(v.Call.Input)), d.Str()); err != nil {
		t.Fatalf("stale-diagnostic restatement: %v", err)
	}
	return reencode(text, objReplace(parsed.Value, "checkpoint", objReplace(cp, "diagnostic", ojson.NullValue())))
}

// restateDiagnostic checks the compact stale diagnostic against the
// checkpoint manifest and the current plan links, from raw bytes.
func restateDiagnostic(root, id, d string) error {
	var cp struct {
		Manifest []struct {
			Path    string `json:"path"`
			SHA256  string `json:"sha256"`
			Missing bool   `json:"missing"`
		} `json:"manifest"`
	}
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".checkpoint.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &cp); err != nil {
		return err
	}
	var plan struct {
		PlanFile  string   `json:"planFile"`
		SpecFiles []string `json:"specFiles"`
	}
	pdata, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".json"))
	if err != nil {
		return err
	}
	json.Unmarshal(pdata, &plan)
	now := map[string]bool{".opencode/workplan/" + id + ".json": true, plan.PlanFile: true}
	for _, s := range plan.SpecFiles {
		now[s] = true
	}
	changed := map[string]bool{}
	then := map[string]bool{}
	for _, en := range cp.Manifest {
		then[en.Path] = true
		cur, ok := fileSHA(filepath.Join(root, en.Path))
		if !now[en.Path] || en.Missing != !ok || (ok && cur != en.SHA256) {
			changed[en.Path] = true
		}
	}
	for p := range now {
		if !then[p] {
			changed[p] = true
		}
	}
	body := strings.TrimPrefix(d, "changed: ")
	more := 0
	if m := moreRe.FindStringSubmatch(body); m != nil {
		fmt.Sscan(m[1], &more)
		body = strings.TrimSuffix(body, m[0])
	}
	if len(changed) == 0 {
		if d != "changed: planHash only" {
			return fmt.Errorf("diagnostic %q, restated: planHash only", d)
		}
		return nil
	}
	shown := strings.Split(body, ", ")
	for _, p := range shown {
		if !changed[p] {
			return fmt.Errorf("diagnostic names %s, restated changes %v", p, changed)
		}
	}
	if len(shown) > 3 || len(shown)+more != len(changed) {
		return fmt.Errorf("diagnostic %q lists %d+%d, restated %d", d, len(shown), more, len(changed))
	}
	return nil
}

// ---- withheld fields ----

// restateWithheld is the test's own statement of the rule: summary and
// nextAction always, then guardrails, references and recentValidation
// when the stored checkpoint has entries.
func restateWithheld(root, id string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".checkpoint.json"))
	if err != nil {
		return nil, err
	}
	var cp map[string]any
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	out := []string{"summary", "nextAction"}
	for _, k := range []string{"guardrails", "references", "recentValidation"} {
		if l, _ := cp[k].([]any); len(l) > 0 {
			out = append(out, k)
		}
	}
	return out, nil
}

// withheldInverse checks checkpoint.withheld against the raw checkpoint
// file and the instruction (the reference's stale text, then a sentence
// naming the checkpoint file and merge=true), and restores the reference
// packet: no withheld member, the stale instruction, the same budget
// level.
func withheldInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, input ojson.Value, text string) string {
	t.Helper()
	if v.Call.Tool != "workplan_resume" {
		t.Fatalf("withheld-fields does not apply to %s", v.Call.Tool)
	}
	idv, _ := input.Get("id")
	id := idv.Str()
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	out := parsed.Value
	cp, _ := out.Get("checkpoint")
	w, ok := cp.Get("withheld")
	if !ok {
		t.Fatal("resume packet without checkpoint.withheld")
	}
	var got []string
	for _, x := range w.Elems() {
		got = append(got, x.Str())
	}
	want, err := restateWithheld(root.Path, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withheld %v, restated %v", got, want)
	}
	ins, _ := out.Get("instruction")
	rel := ".opencode/workplan/" + id + ".checkpoint.json"
	if !strings.HasPrefix(ins.Str(), instructionStale+" ") || !strings.Contains(ins.Str(), rel) ||
		!strings.Contains(ins.Str(), "via workplan_checkpoint merge=true") || !strings.Contains(ins.Str(), "(workplan_read omits it)") {
		t.Fatalf("instruction %q", ins.Str())
	}
	out = objReplace(out, "checkpoint", objWithout(cp, "withheld"))
	out = objReplace(out, "instruction", ojson.StringValue(instructionStale))
	return reencode(text, out)
}

// ---- checkpoint write warnings ----

// restateDropWarnings restates the rule from the fixture's stored
// checkpoint and the vector input (no merge in the corpus): each list
// whose stored entries are not all in the new list, in the order
// guardrails, references, recentValidation, blockers.
func restateDropWarnings(t *testing.T, v *mutationVector) []string {
	t.Helper()
	var in map[string]any
	json.Unmarshal(v.Call.Input, &in)
	id, _ := in["id"].(string)
	data, err := os.ReadFile(testutil.Testdata("fixtures", v.Fixture, ".opencode/workplan", id+".checkpoint.json"))
	if err != nil {
		t.Fatalf("restating warnings: %v", err)
	}
	var cp map[string]any
	if err := json.Unmarshal(data, &cp); err != nil {
		t.Fatal(err)
	}
	strs := func(x any) []string {
		var out []string
		seen := map[string]bool{}
		l, _ := x.([]any)
		for _, e := range l {
			s := strings.TrimSpace(e.(string))
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return out
	}
	var out []string
	for _, k := range []string{"guardrails", "references", "recentValidation", "blockers"} {
		var before []string // the stored list as stored
		if l, ok := cp[k].([]any); ok {
			for _, e := range l {
				before = append(before, e.(string))
			}
		}
		now := strs(in[k])
		kept := map[string]bool{}
		for _, x := range now {
			kept[x] = true
		}
		n := 0
		for _, x := range before {
			if !kept[x] {
				n++
			}
		}
		if n == 0 {
			continue
		}
		noun := "entries"
		if n == 1 {
			noun = "entry"
		}
		out = append(out, fmt.Sprintf("%s: %d → %d (%d previous %s not kept; merge=true keeps omitted fields)", k, len(before), len(now), n, noun))
	}
	return out
}

// dropWarningsInverse checks a checkpoint result's warnings against the
// restatement and removes them.
func dropWarningsInverse(t *testing.T, v *mutationVector, out engine.Output) ojson.Value {
	t.Helper()
	ws, ok := out.Value.Get("warnings")
	if v.Call.Tool != "workplan_checkpoint" || !ok {
		t.Fatal("drop-warnings needs a checkpoint result with warnings")
	}
	var got []string
	for _, x := range ws.Elems() {
		got = append(got, x.Str())
	}
	if want := restateDropWarnings(t, v); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings %q, restated %q", got, want)
	}
	return objWithout(out.Value, "warnings")
}
