package index

import (
	"reflect"
	"testing"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

func est(id string, e string) model.Step {
	s := step(id)
	if e != "" {
		s.Unknown = []ojson.Member{{Key: "estimate", Value: ojson.RawNumber(e)}}
	}
	return s
}

func ref(p, s string) model.StepRef { return model.StepRef{PhaseID: p, StepID: s} }

func entry(p, s string, on ...model.StepRef) model.DependencyEntry {
	return model.DependencyEntry{StepRef: ref(p, s), DependsOn: on}
}

// TestGraphCriticalPathWeights: by step count the a→b→c chain wins; with
// estimates the heavier two-step x→y chain wins.
func TestGraphCriticalPathWeights(t *testing.T) {
	build := func(ex, ey string) *Graph {
		p := &model.Plan{Phases: []model.Phase{{ID: "p", Steps: []model.Step{
			est("a", ""), est("b", ""), est("c", ""), est("x", ex), est("y", ey),
		}}}}
		d := &model.Dependencies{Entries: []model.DependencyEntry{
			entry("p", "b", ref("p", "a")), entry("p", "c", ref("p", "b")), entry("p", "y", ref("p", "x")),
		}}
		return NewGraph(Build(p), d)
	}
	keys := func(cp *CriticalPath) []string {
		var out []string
		for _, k := range cp.Steps {
			out = append(out, k.StepID)
		}
		return out
	}
	g := build("", "")
	if cp := g.Critical(); !reflect.DeepEqual(keys(cp), []string{"a", "b", "c"}) || cp.Estimated || cp.Weight != 3 {
		t.Fatalf("unweighted %v %v %v", keys(cp), cp.Estimated, cp.Weight)
	}
	if u := g.Unblocks(StepKey{"p", "a"}); u != 2 {
		t.Fatalf("unblocks %d", u)
	}
	if sl, ok := g.Slack(StepKey{"p", "x"}); !ok || sl != 1 {
		t.Fatalf("slack x %v", sl)
	}
	g = build("2.5", "4")
	if cp := g.Critical(); !reflect.DeepEqual(keys(cp), []string{"x", "y"}) || !cp.Estimated || cp.Weight != 6.5 {
		t.Fatalf("weighted %v %v %v", keys(cp), cp.Estimated, cp.Weight)
	}
	// Non-positive or non-numeric estimates are ignored.
	g = build("0", "-1")
	if cp := g.Critical(); !reflect.DeepEqual(keys(cp), []string{"a", "b", "c"}) || cp.Estimated {
		t.Fatalf("invalid estimates used: %v", keys(cp))
	}
}

// TestGraphReadiness: completed and archived-completed prerequisites are
// met; cancelled ones (plan or archived) are not; closed steps are not
// counted downstream.
func TestGraphReadiness(t *testing.T) {
	a, b, c, d := step("a"), step("b"), step("c"), step("d")
	a.Status, c.Status = "completed", "cancelled"
	p := &model.Plan{Phases: []model.Phase{{ID: "p", Steps: []model.Step{a, b, c, d}}}}
	deps := &model.Dependencies{
		Entries: []model.DependencyEntry{
			entry("p", "b", ref("p", "a"), ref("old", "done")),
			entry("p", "d", ref("p", "c"), ref("old", "dropped"), ref("p", "b")),
		},
		TerminalSummaries: []model.TerminalSummary{
			{StepRef: ref("old", "done"), Title: "Done", Status: "completed"},
			{StepRef: ref("old", "dropped"), Title: "Dropped", Status: "cancelled"},
		},
	}
	g := NewGraph(Build(p), deps)
	if !g.Ready(StepKey{"p", "b"}) {
		t.Fatal("b must be ready")
	}
	unmet := g.Unmet(StepKey{"p", "d"})
	want := []Prereq{{StepKey{"p", "c"}, "cancelled"}, {StepKey{"old", "dropped"}, "cancelled"}, {StepKey{"p", "b"}, "draft"}}
	if !reflect.DeepEqual(unmet, want) {
		t.Fatalf("unmet %v", unmet)
	}
	if !g.Archived(StepKey{"old", "done"}) || g.Order(StepKey{"old", "done"}) != -1 {
		t.Fatal("terminal summary must resolve as archived")
	}
	if u := g.Unblocks(StepKey{"p", "c"}); u != 1 {
		t.Fatalf("unblocks %d", u)
	}
	if bl := g.BackwardLinks(); len(bl) != 0 {
		t.Fatalf("backward %v", bl)
	}
}
