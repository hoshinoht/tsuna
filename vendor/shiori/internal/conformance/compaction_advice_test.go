package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// The compaction advisor adds compactionRecommended
// to resume (top level) and doctor (plan entries). With the default
// thresholds no corpus fixture qualifies (the largest eligible history,
// large-paging, can save well under 32 KiB), so no corpus vector lists
// it; TestCompactionAdviceOnCorpus runs the comparator with lowered
// thresholds to prove it works. Note rollover needs an input member no
// oracle vector has, so it cannot change a vector.

// adviceInverse checks each advice against the raw plan JSON and removes
// it.
func adviceInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	inv, err := withoutAdvice(root.Path, v.Call.Tool, text)
	if err != nil {
		t.Fatalf("compaction-advice: %v", err)
	}
	return inv
}

// withoutAdvice removes compactionRecommended, restating each advice from
// the raw plan JSON.
func withoutAdvice(root, tool, text string) (string, error) {
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		return "", err
	}
	v := parsed.Value
	var advices []ojson.Value
	var ids []string
	switch tool {
	case "workplan_resume":
		a, ok := v.Get("compactionRecommended")
		if !ok {
			return "", fmt.Errorf("resume output without compactionRecommended")
		}
		wp, _ := v.Get("workplan")
		id, _ := wp.Get("id")
		advices, ids = append(advices, a), append(ids, id.Str())
		v = objWithout(v, "compactionRecommended")
	case "workplan_doctor":
		plans, _ := v.Get("plans")
		var out []ojson.Value
		for _, p := range plans.Elems() {
			if a, ok := p.Get("compactionRecommended"); ok {
				id, _ := p.Get("id")
				advices, ids = append(advices, a), append(ids, id.Str())
				p = objWithout(p, "compactionRecommended")
			}
			out = append(out, p)
		}
		v = objReplace(v, "plans", ojson.ArrayValue(out))
	default:
		return "", fmt.Errorf("no compaction advice in %s", tool)
	}
	if len(advices) == 0 {
		return "", fmt.Errorf("output carries no compactionRecommended")
	}
	for i, a := range advices {
		if err := testutil.CheckAdviceCounts(root, ids[i], tool, a, advisor.DefaultRolloverKeep); err != nil {
			return "", fmt.Errorf("%s: %v", ids[i], err)
		}
	}
	return reencode(text, v), nil
}

// TestCompactionAdviceOnCorpus runs the comparator on corpus vectors with
// lowered thresholds, so that the fixtures with eligible history get the
// advice: the output minus the advice equals the output with the default
// thresholds (which advise nothing on the corpus), and the counts restate
// from the raw plan. The advice never costs page content, so every resume
// packet (small budgets included) is the default packet plus the member.
func TestCompactionAdviceOnCorpus(t *testing.T) {
	low := &advisor.Thresholds{MinSavingsBytes: 1, Notes: 1, TerminalPercent: 1, PlanBytes: 1}
	advised := 0
	for _, f := range loadVectors(t, "tools", "resume") {
		var v vector
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		if v.Call.Tool != "workplan_doctor" && v.Call.Tool != "workplan_resume" {
			continue
		}
		t.Run(v.ID, func(t *testing.T) {
			root := testutil.NewRoot(t, v.Fixture)
			in, err := ojson.Parse(v.Call.Input)
			if err != nil {
				t.Fatal(err)
			}
			plain := mustEngine(t, root.Path)
			plainText, err := runTool(plain, v.Call.Tool, in.Value)
			if err != nil {
				return
			}
			if strings.Contains(plainText, `"compactionRecommended"`) {
				t.Fatal("the default thresholds advise compaction on a corpus fixture")
			}
			e := mustEngine(t, root.Path)
			e.Compaction = low
			text, err := runTool(e, v.Call.Tool, in.Value)
			if err != nil {
				t.Fatalf("advice changed the outcome: %v", err)
			}
			if text == plainText {
				return
			}
			inv, err := withoutAdvice(root.Path, v.Call.Tool, text)
			if err != nil {
				t.Fatal(err)
			}
			if inv != plainText {
				t.Fatalf("output minus compactionRecommended differs from the output without advice\n%s", firstDiff(inv, plainText))
			}
			advised++
		})
	}
	if advised == 0 {
		t.Fatal("no corpus vector exercised the compaction advice comparator")
	}
	t.Logf("compaction advice comparator checked %d vectors with lowered thresholds", advised)
}
