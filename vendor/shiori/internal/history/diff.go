package history

import (
	"reflect"
	"strconv"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Diff lists the plan elements that differ between a and b (nil a: a new
// plan; nil b: a removed one). updatedAt is not a change.
func Diff(a, b *model.Plan) []Change {
	switch {
	case a == nil && b == nil:
		return nil
	case a == nil:
		return []Change{{Path: "plan", Op: "added"}}
	case b == nil:
		return []Change{{Path: "plan", Op: "removed"}}
	}
	var out []Change
	add := func(c Change) { out = append(out, c) }
	field := func(path string, x, y any) {
		if !reflect.DeepEqual(x, y) {
			add(Change{Path: path, Op: "changed"})
		}
	}
	list := func(path string, x, y []string) {
		if !equalStrings(x, y) {
			add(Change{Path: path, Op: "changed"})
		}
	}
	field("kind", a.Kind, b.Kind)
	field("title", a.Title, b.Title)
	field("goal", a.Goal, b.Goal)
	list("scope", a.Scope, b.Scope)
	list("nonGoals", a.NonGoals, b.NonGoals)
	list("constraints", a.Constraints, b.Constraints)
	list("relevantFiles", a.RelevantFiles, b.RelevantFiles)
	field("planFile", a.PlanFile, b.PlanFile)
	list("specFiles", a.SpecFiles, b.SpecFiles)
	if !equalMembers(a.Unknown, b.Unknown) {
		add(Change{Path: "extra", Op: "changed"})
	}
	if a.Status != b.Status {
		add(Change{Path: "status", Op: "changed", From: a.Status, To: b.Status})
	}
	notes(a.Notes, b.Notes, add)
	findings(a.Findings, b.Findings, add)
	phases(a.Phases, b.Phases, add)
	return out
}

func notes(a, b []string, add func(Change)) {
	if equalStrings(a, b) {
		return
	}
	if len(b) > len(a) && equalStrings(a, b[:len(a)]) {
		add(Change{Path: "notes", Op: "appended", Count: len(b) - len(a)})
		return
	}
	add(Change{Path: "notes", Op: "changed"})
}

func findings(a, b []model.Finding, add func(Change)) {
	for i := 0; i < len(a) || i < len(b); i++ {
		p := "findings/" + strconv.Itoa(i)
		switch {
		case i >= len(a):
			add(Change{Path: p, Op: "added"})
		case i >= len(b):
			add(Change{Path: p, Op: "removed"})
		case !equalFinding(a[i], b[i]):
			c := Change{Path: p, Op: "changed"}
			if a[i].Open() != b[i].Open() {
				c.From, c.To = openWord(a[i].Open()), openWord(b[i].Open())
			}
			add(c)
		}
	}
}

func openWord(open bool) string {
	if open {
		return "open"
	}
	return "resolved"
}

func phases(a, b []model.Phase, add func(Change)) {
	ai := map[string]int{}
	for i, p := range a {
		ai[p.ID] = i
	}
	bi := map[string]int{}
	for i, p := range b {
		bi[p.ID] = i
	}
	if reordered(ids(a, func(p model.Phase) string { return p.ID }), bi) {
		add(Change{Path: "phases", Op: "reordered"})
	}
	for _, p := range a {
		if _, ok := bi[p.ID]; !ok {
			add(Change{Path: "phases/" + p.ID, Op: "removed"})
		}
	}
	for _, q := range b {
		path := "phases/" + q.ID
		i, ok := ai[q.ID]
		if !ok {
			add(Change{Path: path, Op: "added"})
			continue
		}
		p := a[i]
		if p.Status != q.Status {
			add(Change{Path: path, Op: "changed", From: p.Status, To: q.Status})
		} else if p.Title != q.Title || !equalMembers(p.Unknown, q.Unknown) {
			add(Change{Path: path, Op: "changed"})
		}
		steps(path, p.Steps, q.Steps, add)
	}
}

func steps(phase string, a, b []model.Step, add func(Change)) {
	ai := map[string]int{}
	for i, s := range a {
		ai[s.ID] = i
	}
	bi := map[string]int{}
	for i, s := range b {
		bi[s.ID] = i
	}
	if reordered(ids(a, func(s model.Step) string { return s.ID }), bi) {
		add(Change{Path: phase + "/steps", Op: "reordered"})
	}
	for _, s := range a {
		if _, ok := bi[s.ID]; !ok {
			add(Change{Path: phase + "/steps/" + s.ID, Op: "removed"})
		}
	}
	for _, t := range b {
		path := phase + "/steps/" + t.ID
		i, ok := ai[t.ID]
		if !ok {
			add(Change{Path: path, Op: "added"})
			continue
		}
		s := a[i]
		if s.Status != t.Status {
			add(Change{Path: path, Op: "changed", From: s.Status, To: t.Status})
		} else if !equalStep(s, t) {
			add(Change{Path: path, Op: "changed"})
		}
	}
}

func ids[T any](xs []T, id func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = id(x)
	}
	return out
}

// reordered: the elements present on both sides changed relative order.
func reordered(before []string, after map[string]int) bool {
	last := -1
	for _, id := range before {
		j, ok := after[id]
		if !ok {
			continue
		}
		if j < last {
			return true
		}
		last = j
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalPtr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// equalMembers compares unknown members; nil and empty are equal.
func equalMembers(a, b []ojson.Member) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func equalStep(a, b model.Step) bool {
	return a.ID == b.ID && a.Title == b.Title && a.Status == b.Status && equalPtr(a.Target, b.Target) &&
		equalPtr(a.Action, b.Action) && equalPtr(a.Validation, b.Validation) && equalMembers(a.Unknown, b.Unknown)
}

func equalFinding(a, b model.Finding) bool {
	return a.Severity == b.Severity && a.Title == b.Title && equalPtr(a.Detail, b.Detail) && equalPtr(a.Source, b.Source) &&
		equalPtr(a.Status, b.Status) && equalMembers(a.Unknown, b.Unknown)
}
