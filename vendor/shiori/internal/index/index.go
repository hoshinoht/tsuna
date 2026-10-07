// Package index builds derived, rebuildable lookup structures over a
// decoded plan. Indexes are never authority: they are rebuilt
// from the snapshot bytes and carry no state of their own.
package index

import (
	"fmt"
	"strconv"

	"github.com/hoshinoht/shiori/internal/model"
)

// StepKey is the composite step identity. It is a struct, not a delimited
// string, so legacy ids containing delimiters cannot collide.
type StepKey struct {
	PhaseID string
	StepID  string
}

func (k StepKey) String() string { return k.PhaseID + "/" + k.StepID }

// StepLoc locates a step in the ordered slices.
type StepLoc struct {
	Phase int
	Step  int
}

// Plan is the per-snapshot index.
type Plan struct {
	Doc *model.Plan

	// PhaseByID maps a phase id to its first index. Ambiguous (duplicate)
	// ids are listed in DuplicatePhases and are never resolved by
	// "last entry wins".
	PhaseByID       map[string]int
	DuplicatePhases []string
	// StepByKey maps a composite key to its first location.
	StepByKey      map[StepKey]StepLoc
	DuplicateSteps []StepKey
}

// Build indexes a decoded plan in O(phases + steps).
func Build(p *model.Plan) *Plan {
	ix := &Plan{
		Doc:       p,
		PhaseByID: make(map[string]int, len(p.Phases)),
		StepByKey: make(map[StepKey]StepLoc, p.StepCount()),
	}
	for i := range p.Phases {
		ph := &p.Phases[i]
		if _, dup := ix.PhaseByID[ph.ID]; dup {
			ix.DuplicatePhases = append(ix.DuplicatePhases, ph.ID)
		} else {
			ix.PhaseByID[ph.ID] = i
		}
		for j := range ph.Steps {
			k := StepKey{ph.ID, ph.Steps[j].ID}
			if _, dup := ix.StepByKey[k]; dup {
				ix.DuplicateSteps = append(ix.DuplicateSteps, k)
			} else {
				ix.StepByKey[k] = StepLoc{i, j}
			}
		}
	}
	return ix
}

// StepsByID lists every location of a bare step id (for filters that
// omit the phase), in document order. O(steps); built on demand.
func (ix *Plan) StepsByID(id string) []StepLoc {
	var out []StepLoc
	for i := range ix.Doc.Phases {
		for j := range ix.Doc.Phases[i].Steps {
			if ix.Doc.Phases[i].Steps[j].ID == id {
				out = append(out, StepLoc{i, j})
			}
		}
	}
	return out
}

// Phase returns the phase with id, when unambiguous.
func (ix *Plan) Phase(id string) (*model.Phase, bool) {
	i, ok := ix.PhaseByID[id]
	if !ok {
		return nil, false
	}
	return &ix.Doc.Phases[i], true
}

// Step returns the step for a composite key.
func (ix *Plan) Step(k StepKey) (*model.Step, bool) {
	loc, ok := ix.StepByKey[k]
	if !ok {
		return nil, false
	}
	return &ix.Doc.Phases[loc.Phase].Steps[loc.Step], true
}

// SeverityRank orders findings for projection: high findings by severity,
// then the reference's ordering of the rest (minor, question, note).
var SeverityRank = map[string]int{"blocker": 0, "critical": 1, "major": 2, "minor": 3, "question": 4, "note": 5}

// IsHigh reports blocker/critical/major.
func IsHigh(severity string) bool { return SeverityRank[severity] <= 2 && severity != "" }

// Buckets are ordered severity buckets of unresolved findings. Each bucket
// keeps stable document order.
type Buckets struct {
	BySeverity map[string][]int
	Resolved   int
}

// BuildBuckets groups unresolved finding indexes by severity (O(n)).
func BuildBuckets(p *model.Plan) Buckets {
	b := Buckets{BySeverity: map[string][]int{}}
	for i := range p.Findings {
		f := &p.Findings[i]
		if !f.Open() {
			b.Resolved++
			continue
		}
		b.BySeverity[f.Severity] = append(b.BySeverity[f.Severity], i)
	}
	return b
}

// Ordered returns indexes for the given severities in rank order (O(k)).
func (b Buckets) Ordered(severities ...string) []int {
	var out []int
	for _, s := range severities {
		out = append(out, b.BySeverity[s]...)
	}
	return out
}

// High returns unresolved blocker, critical, major finding indexes.
func (b Buckets) High() []int { return b.Ordered("blocker", "critical", "major") }

// Low returns the other unresolved findings in projection order.
func (b Buckets) Low() []int { return b.Ordered("minor", "question", "note") }

// DAG is the dependency graph over composite step keys. Edges point from
// a dependent step to its prerequisite, in stored order.
type DAG struct {
	Nodes      []StepKey // first-seen order
	Prereqs    map[StepKey][]StepKey
	Dependents map[StepKey][]StepKey
}

// BuildDAG builds adjacency maps from a decoded dependency sidecar.
func BuildDAG(d *model.Dependencies) *DAG {
	g := &DAG{Prereqs: map[StepKey][]StepKey{}, Dependents: map[StepKey][]StepKey{}}
	seen := map[StepKey]bool{}
	addNode := func(k StepKey) {
		if !seen[k] {
			seen[k] = true
			g.Nodes = append(g.Nodes, k)
		}
	}
	if d == nil {
		return g
	}
	for _, e := range d.Entries {
		from := StepKey{e.PhaseID, e.StepID}
		addNode(from)
		for _, r := range e.DependsOn {
			to := StepKey{r.PhaseID, r.StepID}
			addNode(to)
			g.Prereqs[from] = append(g.Prereqs[from], to)
			g.Dependents[to] = append(g.Dependents[to], from)
		}
	}
	return g
}

// Cycles returns each cycle found by a depth-first search in node order,
// as the key path from the re-entered node back to itself (O(V+E)).
func (g *DAG) Cycles() [][]StepKey {
	const (
		white = iota
		gray
		black
	)
	color := make(map[StepKey]int, len(g.Nodes))
	var stack []StepKey
	var cycles [][]StepKey
	var visit func(k StepKey)
	visit = func(k StepKey) {
		color[k] = gray
		stack = append(stack, k)
		for _, next := range g.Prereqs[k] {
			switch color[next] {
			case white:
				visit(next)
			case gray:
				start := len(stack) - 1
				for start >= 0 && stack[start] != next {
					start--
				}
				cyc := append([]StepKey{}, stack[start:]...)
				cycles = append(cycles, append(cyc, next))
			}
		}
		stack = stack[:len(stack)-1]
		color[k] = black
	}
	for _, k := range g.Nodes {
		if color[k] == white {
			visit(k)
		}
	}
	return cycles
}

// ValidateDependencies returns the semantic dependency issues (existing
// targets, cycles) with exact field paths. Archived terminal summaries
// count as existing prerequisites.
func ValidateDependencies(ix *Plan, d *model.Dependencies) []string {
	var out []string
	exists := func(k StepKey) bool {
		if _, ok := ix.StepByKey[k]; ok {
			return true
		}
		for _, ts := range d.TerminalSummaries {
			if ts.PhaseID == k.PhaseID && ts.StepID == k.StepID {
				return true
			}
		}
		return false
	}
	sources := map[StepKey]bool{}
	for i, e := range d.Entries {
		from := StepKey{e.PhaseID, e.StepID}
		if !exists(from) {
			out = append(out, fmt.Sprintf("dependencies.%d: Source step %s does not exist", i, from))
		}
		if sources[from] {
			out = append(out, fmt.Sprintf("dependencies.%d: Duplicate dependency source %s", i, from))
		}
		sources[from] = true
		seen := map[StepKey]bool{}
		for j, r := range e.DependsOn {
			to := StepKey{r.PhaseID, r.StepID}
			p := "dependencies." + strconv.Itoa(i) + ".dependsOn." + strconv.Itoa(j)
			if !exists(to) {
				out = append(out, p+": Step "+to.String()+" does not exist")
			}
			if seen[to] {
				out = append(out, p+": Duplicate dependency")
			}
			seen[to] = true
		}
	}
	for _, cyc := range BuildDAG(d).Cycles() {
		s := ""
		for i, k := range cyc {
			if i > 0 {
				s += " -> "
			}
			s += k.String()
		}
		out = append(out, "dependencies: Cycle detected: "+s)
	}
	return out
}

// MarkerIndex maps generated Markdown markers to plan entities. Because
// the marker format is lossy for non-ASCII ids, a marker shared by
// several entities is ambiguous and is never resolved "first match wins";
// section retrieval must fall back to heading plus ordinal.
type MarkerIndex struct {
	PhaseMarkers map[string][]int
	StepMarkers  map[string][]StepKey
}

// Ambiguous reports whether a marker names more than one entity.
func (m *MarkerIndex) Ambiguous(marker string) bool {
	return len(m.PhaseMarkers[marker]) > 1 || len(m.StepMarkers[marker]) > 1
}

// BuildMarkers builds the marker index; ids that normalize to empty are
// skipped (they have no marker).
func BuildMarkers(p *model.Plan) *MarkerIndex {
	m := &MarkerIndex{PhaseMarkers: map[string][]int{}, StepMarkers: map[string][]StepKey{}}
	for i := range p.Phases {
		ph := &p.Phases[i]
		if mk, err := model.PhaseMarker(ph.ID); err == nil {
			m.PhaseMarkers[mk] = append(m.PhaseMarkers[mk], i)
		}
		for j := range ph.Steps {
			if mk, err := model.StepMarker(ph.Steps[j].ID); err == nil {
				m.StepMarkers[mk] = append(m.StepMarkers[mk], StepKey{ph.ID, ph.Steps[j].ID})
			}
		}
	}
	return m
}
