package index

import (
	"math"
	"sort"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Graph is the analysis of a valid dependency sidecar over the plan's
// steps: readiness, order checks, downstream counts and
// the critical path. It is derived and never authority; callers build it
// only when the sidecar decodes and ValidateDependencies reports nothing,
// so every edge resolves to a plan step or an archived terminal summary
// and the graph is acyclic.
//
// A prerequisite is met only when it is completed (in the plan or as an
// archived terminal summary). Open means neither completed nor cancelled.
type Graph struct {
	ix       *Plan
	dag      *DAG
	terminal map[StepKey]string // archived prerequisite status
	order    map[StepKey]int    // plan order (phase-major); archived steps are absent
	keys     []StepKey          // plan steps in plan order (first occurrence)
	// Entries keeps the stored entries for field paths (warnings).
	Entries []model.DependencyEntry

	cp   *CriticalPath
	to   map[StepKey]pathCell
	from map[StepKey]pathCell
}

// NewGraph builds the analysis graph. d must be a valid sidecar.
func NewGraph(ix *Plan, d *model.Dependencies) *Graph {
	g := &Graph{ix: ix, dag: BuildDAG(d), terminal: map[StepKey]string{}, order: map[StepKey]int{}}
	if d != nil {
		g.Entries = d.Entries
		for _, ts := range d.TerminalSummaries {
			k := StepKey{ts.PhaseID, ts.StepID}
			if _, ok := g.terminal[k]; !ok {
				g.terminal[k] = ts.Status
			}
		}
	}
	n := 0
	for i := range ix.Doc.Phases {
		ph := &ix.Doc.Phases[i]
		for j := range ph.Steps {
			k := StepKey{ph.ID, ph.Steps[j].ID}
			if _, dup := g.order[k]; dup {
				continue
			}
			g.order[k] = n
			g.keys = append(g.keys, k)
			n++
		}
	}
	return g
}

// IsOpen reports a status that is neither completed nor cancelled.
func IsOpen(status string) bool { return status != "completed" && status != "cancelled" }

// Status is a step's status: the plan step's, else the archived terminal
// summary's. ok is false for an unknown step.
func (g *Graph) Status(k StepKey) (string, bool) {
	if st, ok := g.ix.Step(k); ok {
		return st.Status, true
	}
	s, ok := g.terminal[k]
	return s, ok
}

// Archived reports whether k resolves only to a terminal summary.
func (g *Graph) Archived(k StepKey) bool {
	if _, ok := g.ix.StepByKey[k]; ok {
		return false
	}
	_, ok := g.terminal[k]
	return ok
}

// Order is the plan position of a step, or -1 for an archived step.
func (g *Graph) Order(k StepKey) int {
	if o, ok := g.order[k]; ok {
		return o
	}
	return -1
}

// Prereq is one prerequisite with its resolved status.
type Prereq struct {
	Key    StepKey
	Status string
}

// Prereqs lists k's prerequisites in stored order with their statuses.
func (g *Graph) Prereqs(k StepKey) []Prereq {
	var out []Prereq
	for _, r := range g.dag.Prereqs[k] {
		s, _ := g.Status(r)
		out = append(out, Prereq{r, s})
	}
	return out
}

// Unmet lists k's prerequisites that are not completed (stored order).
func (g *Graph) Unmet(k StepKey) []Prereq {
	var out []Prereq
	for _, p := range g.Prereqs(k) {
		if p.Status != "completed" {
			out = append(out, p)
		}
	}
	return out
}

// Ready reports whether every prerequisite of k is completed.
func (g *Graph) Ready(k StepKey) bool { return len(g.Unmet(k)) == 0 }

// Dependents lists the steps that depend directly on k, in plan order.
func (g *Graph) Dependents(k StepKey) []StepKey {
	out := append([]StepKey{}, g.dag.Dependents[k]...)
	g.sortPlanOrder(out)
	return out
}

func (g *Graph) sortPlanOrder(ks []StepKey) {
	sort.SliceStable(ks, func(i, j int) bool { return g.Order(ks[i]) < g.Order(ks[j]) })
}

// Unblocks counts the distinct open plan steps that depend on k directly
// or transitively (the downstream work k holds up). O(V+E).
func (g *Graph) Unblocks(k StepKey) int {
	seen := map[StepKey]bool{k: true}
	stack := []StepKey{k}
	n := 0
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range g.dag.Dependents[cur] {
			if seen[d] {
				continue
			}
			seen[d] = true
			if s, ok := g.ix.Step(d); ok && IsOpen(s.Status) {
				n++
			}
			stack = append(stack, d)
		}
	}
	return n
}

// Link is a stored dependency edge with its field path indexes.
type Link struct {
	Entry, Ref int
	From, To   StepKey
}

// BackwardLinks lists stored edges whose prerequisite comes later in plan
// order than the dependent step (archived prerequisites never do).
func (g *Graph) BackwardLinks() []Link {
	var out []Link
	for i, e := range g.Entries {
		from := StepKey{e.PhaseID, e.StepID}
		fo := g.Order(from)
		for j, r := range e.DependsOn {
			to := StepKey{r.PhaseID, r.StepID}
			if to0 := g.Order(to); to0 >= 0 && fo >= 0 && to0 > fo {
				out = append(out, Link{i, j, from, to})
			}
		}
	}
	return out
}

// EmptyEntries lists the indexes of stored entries without prerequisites.
func (g *Graph) EmptyEntries() []int {
	var out []int
	for i, e := range g.Entries {
		if len(e.DependsOn) == 0 {
			out = append(out, i)
		}
	}
	return out
}

// Steps lists the plan steps in plan order.
func (g *Graph) Steps() []StepKey { return g.keys }

// Estimate is a step's optional positive numeric "estimate" member (kept
// as unknown step metadata; V2 has no estimate field). ok is false when
// absent or not a positive finite number.
func Estimate(st *model.Step) (float64, bool) {
	for _, m := range st.Unknown {
		if m.Key != "estimate" || m.Value.Kind() != ojson.Number {
			continue
		}
		f, ok := m.Value.Float()
		if ok && f > 0 && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return f, true
		}
		return 0, false
	}
	return 0, false
}

// CriticalPath is the longest chain of open steps through the graph:
// by summed weight (an estimate when present, else 1), then by step
// count, then earliest in plan order.
type CriticalPath struct {
	Steps     []StepKey
	Weight    float64
	Estimated bool // at least one step on the path used an estimate
}

type pathCell struct {
	weight float64
	count  int
	next   StepKey // predecessor (to) or successor (from)
	has    bool
	done   bool
}

func (g *Graph) weight(k StepKey) (float64, bool) {
	st, ok := g.ix.Step(k)
	if !ok {
		return 1, false
	}
	if f, ok := Estimate(st); ok {
		return f, true
	}
	return 1, false
}

func (g *Graph) open(k StepKey) bool {
	st, ok := g.ix.Step(k)
	return ok && IsOpen(st.Status)
}

func better(w float64, c int, o int, bw float64, bc int, bo int) bool {
	if w != bw {
		return w > bw
	}
	if c != bc {
		return c > bc
	}
	return o < bo
}

// longest computes, per open step, the heaviest open chain ending at it
// (dir = prereqs) or starting at it (dir = dependents). Memoized; the
// graph is acyclic.
func (g *Graph) longest(memo map[StepKey]pathCell, k StepKey, next func(StepKey) []StepKey) pathCell {
	if c, ok := memo[k]; ok && c.done {
		return c
	}
	w, _ := g.weight(k)
	best := pathCell{weight: w, count: 1, done: true}
	for _, n := range next(k) {
		if !g.open(n) {
			continue
		}
		c := g.longest(memo, n, next)
		if !best.has || better(c.weight+w, c.count+1, g.Order(n), best.weight, best.count, g.Order(best.next)) {
			best = pathCell{weight: c.weight + w, count: c.count + 1, next: n, has: true, done: true}
		}
	}
	memo[k] = best
	return best
}

func (g *Graph) analyse() {
	if g.cp != nil {
		return
	}
	g.to = map[StepKey]pathCell{}
	g.from = map[StepKey]pathCell{}
	prereqs := func(k StepKey) []StepKey { return g.dag.Prereqs[k] }
	deps := func(k StepKey) []StepKey { return g.Dependents(k) }
	var end StepKey
	var bestC pathCell
	found := false
	for _, k := range g.keys {
		if !g.open(k) {
			continue
		}
		c := g.longest(g.to, k, prereqs)
		g.longest(g.from, k, deps)
		if !found || better(c.weight, c.count, g.Order(k), bestC.weight, bestC.count, g.Order(end)) {
			end, bestC, found = k, c, true
		}
	}
	cp := &CriticalPath{}
	if found {
		var rev []StepKey
		k := end
		for {
			rev = append(rev, k)
			if _, est := g.weight(k); est {
				cp.Estimated = true
			}
			c := g.to[k]
			if !c.has {
				break
			}
			k = c.next
		}
		for i := len(rev) - 1; i >= 0; i-- {
			cp.Steps = append(cp.Steps, rev[i])
		}
		cp.Weight = bestC.weight
	}
	g.cp = cp
}

// Critical returns the critical path over the open steps.
func (g *Graph) Critical() *CriticalPath {
	g.analyse()
	return g.cp
}

// Slack is how much k's longest open chain falls short of the critical
// path (0 on the critical path). ok is false for a step that is not open.
func (g *Graph) Slack(k StepKey) (float64, bool) {
	if !g.open(k) {
		return 0, false
	}
	g.analyse()
	w, _ := g.weight(k)
	through := g.to[k].weight + g.from[k].weight - w
	return g.cp.Weight - through, true
}
