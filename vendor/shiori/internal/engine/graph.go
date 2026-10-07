package engine

import (
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// The dependency graph drives work. Everything here is advisory: order
// checks warn and never refuse, and the critical path and readiness
// ranking are recommendations only. Graph additions appear only when the
// plan has a dependency sidecar that decodes and validates; a missing or
// invalid sidecar adds nothing (its issues are already reported).

// graph returns the analysis graph for a valid recorded sidecar, else nil.
func (e *Engine) graph(ix *index.Plan, dv depView) *index.Graph {
	if dv.deps == nil || len(dv.issues) > 0 {
		return nil
	}
	if ix == nil {
		return nil
	}
	return index.NewGraph(ix, dv.deps)
}

// startedStatus is a step status that an order check applies to:
// in_progress, review or completed.
func startedStatus(s string) bool { return s == "in_progress" || s == "review" || s == "completed" }

func prereqList(ps []index.Prereq) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.Key.String() + " (" + p.Status + ")"
	}
	return strings.Join(parts, ", ")
}

func cancelledOf(ps []index.Prereq) []string {
	var out []string
	for _, p := range ps {
		if p.Status == "cancelled" {
			out = append(out, p.Key.String())
		}
	}
	return out
}

func orderWarning(k index.StepKey, status string, unmet []index.Prereq) string {
	return "dependencies: Order warning: step " + k.String() + " is " + status +
		" but its prerequisites are not completed: " + prereqList(unmet) +
		". The status is kept; complete the prerequisites or revise the dependencies."
}

func cancelledWarning(k index.StepKey, cancelled []string) string {
	return "dependencies: Step " + k.String() + " is blocked by cancelled prerequisite " + strings.Join(cancelled, ", ") +
		". Replace or remove the dependency, or cancel the step."
}

func backwardWarning(l index.Link) string {
	return "dependencies." + strconv.Itoa(l.Entry) + ".dependsOn." + strconv.Itoa(l.Ref) +
		": Backward link: " + l.From.String() + " depends on " + l.To.String() + ", which comes later in plan order."
}

func emptyEntryWarning(i int) string {
	return "dependencies." + strconv.Itoa(i) + ".dependsOn: Dependency entry lists no prerequisites."
}

// graphWarnings are the non-failing validate/doctor diagnostics: order
// violations, cancelled prerequisites, backward links and empty entries.
// valid and issues are unchanged.
func graphWarnings(g *index.Graph) []string {
	if g == nil {
		return nil
	}
	var out []string
	for _, k := range g.Steps() {
		unmet := g.Unmet(k)
		if len(unmet) == 0 {
			continue
		}
		status, _ := g.Status(k)
		if startedStatus(status) {
			out = append(out, orderWarning(k, status, unmet))
		}
		if c := cancelledOf(unmet); len(c) > 0 && index.IsOpen(status) {
			out = append(out, cancelledWarning(k, c))
		}
	}
	for _, l := range g.BackwardLinks() {
		out = append(out, backwardWarning(l))
	}
	for _, i := range g.EmptyEntries() {
		out = append(out, emptyEntryWarning(i))
	}
	return out
}

func stepRefStatus(k index.StepKey, status string) ojson.Value {
	return ojson.NewObject(3).
		Set("phaseId", ojson.StringValue(k.PhaseID)).
		Set("stepId", ojson.StringValue(k.StepID)).
		Set("status", ojson.StringValue(status)).Value()
}

func stepRefValue(k index.StepKey) ojson.Value {
	return refValue(model.StepRef{PhaseID: k.PhaseID, StepID: k.StepID})
}

func floatValue(f float64) ojson.Value {
	return ojson.RawNumber(strconv.FormatFloat(f, 'f', -1, 64))
}

// criticalPathValue renders the critical path when it chains at least two
// open steps (a single step is not a path); ok is false otherwise.
func criticalPathValue(g *index.Graph) (ojson.Value, bool) {
	if g == nil {
		return ojson.Value{}, false
	}
	cp := g.Critical()
	if len(cp.Steps) < 2 {
		return ojson.Value{}, false
	}
	steps := make([]ojson.Value, len(cp.Steps))
	for i, k := range cp.Steps {
		s, _ := g.Status(k)
		steps[i] = stepRefStatus(k, s)
	}
	b := ojson.NewObject(4).
		Set("length", ojson.IntValue(int64(len(cp.Steps))))
	if cp.Estimated {
		b.Set("estimate", floatValue(cp.Weight))
	}
	return b.Set("steps", ojson.ArrayValue(steps)).
		Set("recommendation", ojson.StringValue("Advisory: finishing these steps first shortens the longest remaining dependency chain; nothing is executed automatically.")).Value(), true
}

// statusChangeWarnings are the update-time order warnings: steps
// whose status the update sets to in_progress, review or completed while a
// prerequisite is not completed, and open dependents left behind a step
// the update cancels. The write is never refused for them.
func statusChangeWarnings(old *model.Plan, g *index.Graph) []string {
	if g == nil {
		return nil
	}
	before := map[index.StepKey]string{}
	for i := range old.Phases {
		for j := range old.Phases[i].Steps {
			k := index.StepKey{PhaseID: old.Phases[i].ID, StepID: old.Phases[i].Steps[j].ID}
			if _, ok := before[k]; !ok {
				before[k] = old.Phases[i].Steps[j].Status
			}
		}
	}
	var out []string
	for _, k := range g.Steps() {
		status, _ := g.Status(k)
		if prev, ok := before[k]; ok && prev == status {
			continue
		}
		if startedStatus(status) {
			if unmet := g.Unmet(k); len(unmet) > 0 {
				out = append(out, "Order warning: step "+k.String()+" was set to "+status+
					" while its prerequisites are not completed: "+prereqList(unmet)+
					". The change was applied (order checks are warnings only).")
			}
		}
		if status == "cancelled" {
			var open []string
			for _, d := range g.Dependents(k) {
				if s, ok := g.Status(d); ok && index.IsOpen(s) {
					open = append(open, d.String())
				}
			}
			if len(open) > 0 {
				out = append(out, "Step "+k.String()+" was cancelled; its dependents are now blocked by a cancelled prerequisite: "+
					strings.Join(open, ", ")+". Replace or remove those dependencies, or cancel the dependents.")
			}
		}
	}
	return out
}
