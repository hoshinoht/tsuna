package index

import (
	"reflect"
	"testing"

	"github.com/hoshinoht/shiori/internal/model"
)

func step(id string) model.Step { return model.Step{ID: id, Title: id, Status: "draft"} }

func TestCompositeKeysDoNotCollide(t *testing.T) {
	// A delimiter-joined key would make ("a/b","c") and ("a","b/c") equal.
	p := &model.Plan{Phases: []model.Phase{
		{ID: "a/b", Steps: []model.Step{step("c")}},
		{ID: "a", Steps: []model.Step{step("b/c")}},
	}}
	ix := Build(p)
	if len(ix.StepByKey) != 2 || len(ix.DuplicateSteps) != 0 {
		t.Fatalf("keys collided: %v dup=%v", ix.StepByKey, ix.DuplicateSteps)
	}
	if st, ok := ix.Step(StepKey{"a", "b/c"}); !ok || st.ID != "b/c" {
		t.Fatal("lookup by composite key failed")
	}
}

func TestDuplicatesAreReportedNotLastWins(t *testing.T) {
	p := &model.Plan{Phases: []model.Phase{
		{ID: "p", Steps: []model.Step{step("s"), step("s")}},
		{ID: "p", Steps: []model.Step{step("s")}},
		{ID: "q", Steps: []model.Step{step("s")}},
	}}
	ix := Build(p)
	if !reflect.DeepEqual(ix.DuplicatePhases, []string{"p"}) {
		t.Fatalf("duplicate phases %v", ix.DuplicatePhases)
	}
	if loc := ix.StepByKey[StepKey{"p", "s"}]; loc != (StepLoc{0, 0}) {
		t.Fatalf("first occurrence must be kept, got %v", loc)
	}
	if len(ix.StepsByID("s")) != 4 {
		t.Fatal("bare-id index must list every location")
	}
}

func TestCyclesAndMissingTargets(t *testing.T) {
	p := &model.Plan{Phases: []model.Phase{{ID: "p", Steps: []model.Step{step("s1"), step("s2"), step("s3")}}}}
	d := &model.Dependencies{Entries: []model.DependencyEntry{
		{StepRef: model.StepRef{PhaseID: "p", StepID: "s1"}, DependsOn: []model.StepRef{{PhaseID: "p", StepID: "s2"}}},
		{StepRef: model.StepRef{PhaseID: "p", StepID: "s2"}, DependsOn: []model.StepRef{{PhaseID: "p", StepID: "s1"}}},
		{StepRef: model.StepRef{PhaseID: "p", StepID: "s3"}, DependsOn: []model.StepRef{{PhaseID: "p", StepID: "s3"}, {PhaseID: "ghost", StepID: "x"}}},
	}}
	got := ValidateDependencies(Build(p), d)
	want := []string{
		"dependencies.2.dependsOn.1: Step ghost/x does not exist",
		"dependencies: Cycle detected: p/s1 -> p/s2 -> p/s1",
		"dependencies: Cycle detected: p/s3 -> p/s3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	g := BuildDAG(d)
	if !reflect.DeepEqual(g.Dependents[StepKey{"p", "s1"}], []StepKey{{"p", "s2"}}) {
		t.Fatalf("dependents adjacency wrong: %v", g.Dependents)
	}
	// Archived prerequisites count as existing.
	d.TerminalSummaries = []model.TerminalSummary{{StepRef: model.StepRef{PhaseID: "ghost", StepID: "x"}, Title: "old", Status: "completed"}}
	if got := ValidateDependencies(Build(p), d); len(got) != 2 {
		t.Fatalf("terminal summary not honoured: %q", got)
	}
}

func TestSeverityBucketsKeepStableOrder(t *testing.T) {
	resolved := "resolved"
	p := &model.Plan{Findings: []model.Finding{
		{Severity: "note"}, {Severity: "major"}, {Severity: "blocker"}, {Severity: "question"},
		{Severity: "blocker", Status: &resolved}, {Severity: "minor"}, {Severity: "blocker"}, {Severity: "critical"},
	}}
	b := BuildBuckets(p)
	if !reflect.DeepEqual(b.High(), []int{2, 6, 7, 1}) || !reflect.DeepEqual(b.Low(), []int{5, 3, 0}) || b.Resolved != 1 {
		t.Fatalf("high %v low %v resolved %d", b.High(), b.Low(), b.Resolved)
	}
}

// TestMarkerCollisionIsAmbiguous: lossy markers that collide are
// ambiguous, never resolved first-match-wins.
func TestMarkerCollisionIsAmbiguous(t *testing.T) {
	p := &model.Plan{Phases: []model.Phase{
		{ID: "phase-𝒜-x", Steps: []model.Step{step("step-été"), step("step-t")}},
		{ID: "phase-x", Steps: []model.Step{step("ok")}},
	}}
	m := BuildMarkers(p)
	pm, _ := model.PhaseMarker("phase-x")
	sm, _ := model.StepMarker("step-t")
	if !m.Ambiguous(pm) || !m.Ambiguous(sm) {
		t.Fatal("colliding markers must be ambiguous")
	}
	okm, _ := model.StepMarker("ok")
	if m.Ambiguous(okm) {
		t.Fatal("unique marker reported ambiguous")
	}
}
