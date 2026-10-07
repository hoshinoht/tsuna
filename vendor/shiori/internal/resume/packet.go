// Package resume assembles the workplan_resume continuation packet: the
// packet model, its rendering under the display caps, the budget policy
// that fits it into maxChars, and the paging cursors of resume and
// inspect.
package resume

import (
	"strconv"

	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Current is the step to continue with.
type Current struct {
	PhaseID, PhaseTitle, PhaseStatus string
	StepID, StepTitle, StepStatus    string
	Target, Action, Validation       *string
	Readiness                        Readiness
}

// Readiness of an open step, shown only when the plan has a valid
// dependency sidecar.
type Readiness struct {
	Shown     bool
	Ready     bool
	BlockedBy []index.Prereq // prerequisites not completed, stored order
	Unblocks  int            // open steps held up, transitively
}

// ReadinessOf is the readiness of k in g (not shown when g is nil).
func ReadinessOf(g *index.Graph, k index.StepKey) Readiness {
	if g == nil {
		return Readiness{}
	}
	unmet := g.Unmet(k)
	return Readiness{Shown: true, Ready: len(unmet) == 0, BlockedBy: unmet, Unblocks: g.Unblocks(k)}
}

// set adds readiness members compactly: a ready step carries how many
// open steps it unblocks, a blocked one its unmet prerequisites (ids and
// status only, at most the pinned-list cap, with the omitted count).
func (r Readiness) set(b *ojson.Builder, listCap int) {
	if !r.Shown {
		return
	}
	if r.Ready {
		b.Set("readiness", ojson.StringValue("ready")).
			Set("unblocks", ojson.IntValue(int64(r.Unblocks)))
		return
	}
	n := shown(len(r.BlockedBy), listCap)
	refs := make([]ojson.Value, n)
	for i := 0; i < n; i++ {
		refs[i] = stepRefStatus(r.BlockedBy[i].Key, r.BlockedBy[i].Status)
	}
	b.Set("readiness", ojson.StringValue("blocked")).
		Set("blockedBy", ojson.ArrayValue(refs))
	if o := len(r.BlockedBy) - n; o > 0 {
		b.Set("blockedByOmitted", ojson.IntValue(int64(o)))
	}
}

// Finding is a review finding as the packet shows it.
type Finding struct {
	Index           int
	Severity, Title string
	Detail, Source  *string
	Status          string
	MetadataOmitted bool
}

// FindingOf is the packet view of p.Findings[i].
func FindingOf(p *model.Plan, i int) Finding {
	f := &p.Findings[i]
	status := "open"
	if f.Status != nil {
		status = *f.Status
	}
	return Finding{Index: i, Severity: f.Severity, Title: f.Title, Detail: f.Detail, Source: f.Source,
		Status: status, MetadataOmitted: len(f.Unknown) > 0}
}

// Item is one page entry.
type Item struct {
	Kind string // active-work | finding | reference
	// active-work
	PhaseID, PhaseTitle, PhaseStatus, StepID, Title, Status string
	Target, Action, Validation                              *string
	Readiness                                               Readiness
	// finding
	Finding Finding
	// reference
	Reference, Source string
}

// Critical is resume's compact critical path: its length and the first
// open step on it. The full path stays in workplan_inspect and
// workplan_doctor.
type Critical struct {
	Length int
	Next   index.StepKey
}

// Packet is the complete, untruncated continuation state.
type Packet struct {
	Path, PlanFile      string
	PlanHash, StateHash string
	PlanFresh           bool
	CheckpointExists    bool
	CheckpointFresh     bool
	Freshness           string
	Withheld            []string // stored checkpoint fields not shown (not fresh)
	SourceUpdatedAt     *string
	Summary, NextAction *string
	Current             *Current
	Blockers            []string
	BlockersTotal       int
	Guardrails          []string // shown candidates (withheld when not fresh)
	GuardrailsTotal     int
	References          []string
	ReferencesTotal     int
	RecentValidation    []string
	Diagnostic          *string
	ID                  string
	Title               *string
	Goal, Status        string
	Scope, NonGoals     []string
	Constraints         []string
	RelevantFiles       []string
	UpdatedAt           string
	DepsRecorded        bool
	DepsValid           bool
	CurrentDeps         []model.StepRef
	Critical            *Critical
	Compaction          *ojson.Value // compact compaction advice
	Evidence            *ojson.Value // compact evidence advice
	Lanes               *ojson.Value // active lanes and the current step's lane
	Since               *ojson.Value // writes since the last checkpoint (change log)
	WaitingOn           *ojson.Value // unfinished plans that block this one (plan links)
	High                []Finding
	HighCounts          [3]int
	Warnings            []string
	Items               []Item
	Offset, Limit       int
	MaxChars            int
	PhaseFilter         *string
	StepFilter          *string
	ActiveWorkTotal     int
	FindingsTotal       int
	ReferencesPageTotal int
	HistoricalNotes     int
	ResolvedFindings    int
	Instruction         string
}

// params is one degradation level. Caps are UTF-16 code units including
// the trailing ellipsis; 0 means the class is not truncated.
type params struct {
	listCap   int // pinned list length cap
	titleCap  int // titles (plan, phase, step, finding)
	longCap   int // goal, page-item target/action/validation, list entries
	pinnedCap int // current work: summary, next action, current step prose
	protCap   int // file paths, references, instruction
	items     int // page items to include
	compact   bool
}

// textClass selects the display cap of a packet string. Protected strings
// are file paths, references and the fixed instruction; ids, hashes,
// enums, counts and retrieval pointers are never passed through
// truncation at all.
type textClass int

const (
	clsProtected textClass = iota
	clsTitle
	clsLong
	clsPinned // current work: summary, next action, current target/action/validation
)

// truncState records truncated display fields in traversal order.
type truncState struct {
	caps        [4]int // per textClass; 0 = no cap
	danger      []string
	other       []string
	dangerCount int
}

func (t *truncState) str(path, s string, cls textClass, danger bool) ojson.Value {
	c := t.caps[cls]
	if c <= 0 {
		return ojson.StringValue(s)
	}
	out, cut := ojson.TruncateUTF16(s, c)
	if cut {
		t.record(path, danger)
	}
	return ojson.StringValue(out)
}

func (t *truncState) ptr(path string, s *string, cls textClass, danger bool) ojson.Value {
	if s == nil {
		return ojson.NullValue()
	}
	return t.str(path, *s, cls, danger)
}

func (t *truncState) record(path string, danger bool) {
	if danger {
		t.danger = append(t.danger, path)
		t.dangerCount++
	} else {
		t.other = append(t.other, path)
	}
}

func (t *truncState) list(path string, items []string, cap int, cls textClass, danger bool) ojson.Value {
	n := len(items)
	if n > cap {
		n = cap
	}
	out := make([]ojson.Value, n)
	for i := 0; i < n; i++ {
		out[i] = t.str(path+"["+strconv.Itoa(i)+"]", items[i], cls, danger)
	}
	return ojson.ArrayValue(out)
}

func shown(total, cap int) int {
	if total > cap {
		return cap
	}
	return total
}

func (t *truncState) finding(path string, f Finding, withKind bool, danger bool) ojson.Value {
	b := ojson.NewObject(9)
	if withKind {
		b.Set("kind", ojson.StringValue("finding"))
	}
	b.Set("index", ojson.IntValue(int64(f.Index))).
		Set("severity", ojson.StringValue(f.Severity)).
		Set("title", t.str(path+".title", f.Title, clsTitle, danger)).
		Set("detail", t.ptr(path+".detail", f.Detail, clsLong, danger)).
		Set("source", t.ptr(path+".source", f.Source, clsLong, danger)).
		Set("status", ojson.StringValue(f.Status)).
		Set("metadataOmitted", ojson.BoolValue(f.MetadataOmitted))
	if f.MetadataOmitted {
		t.record(path+".customMetadata", danger)
	}
	return b.Value()
}

func stepRefStatus(k index.StepKey, status string) ojson.Value {
	return ojson.NewObject(3).
		Set("phaseId", ojson.StringValue(k.PhaseID)).
		Set("stepId", ojson.StringValue(k.StepID)).
		Set("status", ojson.StringValue(status)).Value()
}

func refValue(r model.StepRef) ojson.Value {
	return ojson.NewObject(2).
		Set("phaseId", ojson.StringValue(r.PhaseID)).
		Set("stepId", ojson.StringValue(r.StepID)).Value()
}

// build renders the packet for one degradation level.
func (m *Packet) build(pr params) ojson.Value {
	t := &truncState{caps: [4]int{pr.protCap, pr.titleCap, pr.longCap, pr.pinnedCap}}
	L := pr.listCap

	pathV := t.str("path", m.Path, clsProtected, false)
	planFileV := t.str("planFile", m.PlanFile, clsProtected, false)
	// The summary is truncated (and listed) before the current position.
	summaryV := t.ptr("checkpoint.summary", m.Summary, clsPinned, false)
	var current ojson.Value
	if m.Current == nil {
		current = ojson.NullValue()
	} else {
		c := m.Current
		cb := ojson.NewObject(12).
			Set("phaseId", ojson.StringValue(c.PhaseID)).
			Set("phaseTitle", t.str("checkpoint.current.phaseTitle", c.PhaseTitle, clsTitle, false)).
			Set("phaseStatus", ojson.StringValue(c.PhaseStatus)).
			Set("stepId", ojson.StringValue(c.StepID)).
			Set("stepTitle", t.str("checkpoint.current.stepTitle", c.StepTitle, clsTitle, false)).
			Set("stepStatus", ojson.StringValue(c.StepStatus))
		c.Readiness.set(cb, L)
		current = cb.
			Set("target", t.ptr("checkpoint.current.target", c.Target, clsPinned, false)).
			Set("action", t.ptr("checkpoint.current.action", c.Action, clsPinned, false)).
			Set("validation", t.ptr("checkpoint.current.validation", c.Validation, clsPinned, false)).Value()
	}
	cpb := ojson.NewObject(21).
		Set("exists", ojson.BoolValue(m.CheckpointExists)).
		Set("fresh", ojson.BoolValue(m.CheckpointFresh)).
		Set("freshness", ojson.StringValue(m.Freshness))
	if m.Withheld != nil {
		// Field names, never shortened (like enums).
		cpb.Set("withheld", ojson.StringsValue(m.Withheld))
	}
	checkpoint := cpb.
		Set("sourceUpdatedAt", ojson.NullableString(m.SourceUpdatedAt)).
		Set("summary", summaryV).
		Set("current", current).
		Set("nextAction", t.ptr("checkpoint.nextAction", m.NextAction, clsPinned, false)).
		Set("blockers", t.list("checkpoint.blockers", m.Blockers, L, clsLong, true)).
		Set("blockersTotal", ojson.IntValue(int64(m.BlockersTotal))).
		Set("guardrails", t.list("checkpoint.guardrails", m.Guardrails, L, clsLong, true)).
		Set("guardrailsTotal", ojson.IntValue(int64(m.GuardrailsTotal))).
		Set("references", t.list("checkpoint.references", m.References, L, clsProtected, true)).
		Set("referencesTotal", ojson.IntValue(int64(m.ReferencesTotal))).
		Set("recentValidation", t.list("checkpoint.recentValidation", m.RecentValidation, L, clsLong, true)).
		Set("evidenceStatus", ojson.StringValue("unverified")).
		Set("diagnostic", t.ptr("checkpoint.diagnostic", m.Diagnostic, clsLong, false)).Value()

	var titleV ojson.Value
	if m.Title == nil {
		titleV = ojson.NullValue()
	} else {
		titleV = t.str("workplan.title", *m.Title, clsTitle, false)
	}
	workplan := ojson.NewObject(12).
		Set("id", ojson.StringValue(m.ID)).
		Set("title", titleV).
		Set("goal", t.str("workplan.goal", m.Goal, clsLong, false)).
		Set("status", ojson.StringValue(m.Status)).
		Set("scope", t.list("workplan.scope", m.Scope, L, clsLong, true)).
		Set("scopeTotal", ojson.IntValue(int64(len(m.Scope)))).
		Set("nonGoals", t.list("workplan.nonGoals", m.NonGoals, L, clsLong, true)).
		Set("nonGoalsTotal", ojson.IntValue(int64(len(m.NonGoals)))).
		Set("constraints", t.list("workplan.constraints", m.Constraints, L, clsLong, true)).
		Set("constraintsTotal", ojson.IntValue(int64(len(m.Constraints)))).
		Set("relevantFiles", t.list("workplan.relevantFiles", m.RelevantFiles, L, clsProtected, false)).
		Set("updatedAt", ojson.StringValue(m.UpdatedAt)).Value()

	depRefs := make([]ojson.Value, 0, shown(len(m.CurrentDeps), L))
	for i := 0; i < shown(len(m.CurrentDeps), L); i++ {
		depRefs = append(depRefs, refValue(m.CurrentDeps[i]))
	}
	currentDependencies := ojson.NewObject(4).
		Set("recorded", ojson.BoolValue(m.DepsRecorded)).
		Set("valid", ojson.BoolValue(m.DepsValid)).
		Set("total", ojson.IntValue(int64(len(m.CurrentDeps)))).
		Set("references", ojson.ArrayValue(depRefs)).Value()
	var criticalPath ojson.Value
	if m.Critical != nil {
		criticalPath = ojson.NewObject(2).
			Set("length", ojson.IntValue(int64(m.Critical.Length))).
			Set("nextStep", ojson.NewObject(2).
				Set("phaseId", ojson.StringValue(m.Critical.Next.PhaseID)).
				Set("stepId", ojson.StringValue(m.Critical.Next.StepID)).Value()).Value()
	}

	high := make([]ojson.Value, 0, shown(len(m.High), L))
	for i := 0; i < shown(len(m.High), L); i++ {
		high = append(high, t.finding("safety.highFindings["+strconv.Itoa(i)+"]", m.High[i], false, true))
	}
	warnings := t.list("safety.unverifiedWarnings", m.Warnings, L, clsLong, true)

	omitted := [8]int{
		len(m.Constraints) - shown(len(m.Constraints), L),
		len(m.Blockers) - shown(len(m.Blockers), L),
		len(m.High) - shown(len(m.High), L),
		len(m.CurrentDeps) - shown(len(m.CurrentDeps), L),
		len(m.Guardrails) - shown(len(m.Guardrails), L),
		len(m.References) - shown(len(m.References), L),
		len(m.Scope) - shown(len(m.Scope), L),
		len(m.NonGoals) - shown(len(m.NonGoals), L),
	}

	// Page items.
	total := len(m.Items)
	n := pr.items
	if rem := total - m.Offset; n > rem {
		n = rem
	}
	if n < 0 {
		n = 0
	}
	items := make([]ojson.Value, 0, n)
	for i := 0; i < n; i++ {
		it := &m.Items[m.Offset+i]
		base := "page.items[" + strconv.Itoa(i) + "]"
		switch it.Kind {
		case "active-work":
			ib := ojson.NewObject(13).
				Set("kind", ojson.StringValue(it.Kind)).
				Set("phaseId", ojson.StringValue(it.PhaseID)).
				Set("phaseTitle", t.str(base+".phaseTitle", it.PhaseTitle, clsTitle, false)).
				Set("phaseStatus", ojson.StringValue(it.PhaseStatus)).
				Set("stepId", ojson.StringValue(it.StepID)).
				Set("title", t.str(base+".title", it.Title, clsTitle, false)).
				Set("status", ojson.StringValue(it.Status))
			it.Readiness.set(ib, L)
			items = append(items, ib.
				Set("target", t.ptr(base+".target", it.Target, clsLong, false)).
				Set("action", t.ptr(base+".action", it.Action, clsLong, false)).
				Set("validation", t.ptr(base+".validation", it.Validation, clsLong, false)).Value())
		case "finding":
			items = append(items, t.finding(base, it.Finding, true, false))
		default:
			items = append(items, ojson.NewObject(3).
				Set("kind", ojson.StringValue(it.Kind)).
				Set("reference", t.str(base+".reference", it.Reference, clsProtected, false)).
				Set("source", ojson.StringValue(it.Source)).Value())
		}
	}
	next := ojson.NullValue()
	if m.Offset+n < total {
		next = ojson.StringValue(Cursor{
			StateHash: m.StateHash, MaxChars: m.MaxChars, Limit: m.Limit,
			PhaseID: m.PhaseFilter, StepID: m.StepFilter, Offset: m.Offset + n,
		}.Encode())
	}
	page := ojson.NewObject(7).
		Set("total", ojson.IntValue(int64(total))).
		Set("offset", ojson.IntValue(int64(m.Offset))).
		Set("limit", ojson.IntValue(int64(m.Limit))).
		Set("returned", ojson.IntValue(int64(n))).
		Set("omitted", ojson.IntValue(int64(total-m.Offset-n))).
		Set("items", ojson.ArrayValue(items)).
		Set("nextCursor", next).Value()

	instruction := t.str("instruction", m.Instruction, clsProtected, false)

	omittedSum := 0
	for _, o := range omitted {
		omittedSum += o
	}
	// Overflow is any omitted danger item or any truncated display field
	// (reference behaviour; a page that merely continues is not overflow).
	overflow := omittedSum > 0 || t.dangerCount > 0 || len(t.danger)+len(t.other) > 0
	pointers := []string{}
	if overflow {
		pointers = []string{"workplan_read:" + m.ID, "workplan_inspect:" + m.ID}
	}
	safety := ojson.NewObject(12).
		Set("highFindings", ojson.ArrayValue(high)).
		Set("highFindingsTotal", ojson.IntValue(int64(len(m.High)))).
		Set("highFindingCounts", ojson.NewObject(3).
			Set("blocker", ojson.IntValue(int64(m.HighCounts[0]))).
			Set("critical", ojson.IntValue(int64(m.HighCounts[1]))).
			Set("major", ojson.IntValue(int64(m.HighCounts[2]))).Value()).
		Set("unverifiedWarnings", warnings).
		Set("unverifiedWarningCount", ojson.IntValue(int64(len(m.Warnings)))).
		Set("unverifiedWarningsOmitted", ojson.IntValue(int64(len(m.Warnings)-shown(len(m.Warnings), L)))).
		Set("overflow", ojson.BoolValue(overflow)).
		Set("truncatedDangerFieldCount", ojson.IntValue(int64(t.dangerCount))).
		Set("omittedDangerCounts", ojson.NewObject(8).
			Set("constraints", ojson.IntValue(int64(omitted[0]))).
			Set("blockers", ojson.IntValue(int64(omitted[1]))).
			Set("highFindings", ojson.IntValue(int64(omitted[2]))).
			Set("dependencies", ojson.IntValue(int64(omitted[3]))).
			Set("guardrails", ojson.IntValue(int64(omitted[4]))).
			Set("references", ojson.IntValue(int64(omitted[5]))).
			Set("scope", ojson.IntValue(int64(omitted[6]))).
			Set("nonGoals", ojson.IntValue(int64(omitted[7]))).Value()).
		Set("overflowPointers", ojson.StringsValue(pointers)).Value()

	all := append(append([]string{}, t.danger...), t.other...)
	pathCap := PathCap(m.MaxChars)
	listed := all
	if len(listed) > pathCap {
		listed = listed[:pathCap]
	}

	out := ojson.NewObject(16).
		Set("path", pathV).
		Set("planFile", planFileV).
		Set("hashes", ojson.NewObject(2).
			Set("planHash", ojson.StringValue(m.PlanHash)).
			Set("stateHash", ojson.StringValue(m.StateHash)).Value()).
		Set("planFresh", ojson.BoolValue(m.PlanFresh)).
		Set("checkpoint", checkpoint).
		Set("workplan", workplan).
		Set("currentDependencies", currentDependencies)
	if m.Critical != nil {
		out.Set("criticalPath", criticalPath)
	}
	if m.Compaction != nil {
		out.Set("compactionRecommended", *m.Compaction)
	}
	if m.Lanes != nil {
		out.Set("lanes", *m.Lanes)
	}
	if m.Evidence != nil {
		out.Set("evidence", *m.Evidence)
	}
	if m.Since != nil {
		out.Set("sinceCheckpoint", *m.Since)
	}
	if m.WaitingOn != nil {
		out.Set("waitingOnPlans", *m.WaitingOn)
	}
	return out.
		Set("safety", safety).
		Set("page", page).
		Set("counts", ojson.NewObject(6).
			Set("activeWorkTotal", ojson.IntValue(int64(m.ActiveWorkTotal))).
			Set("findingsTotal", ojson.IntValue(int64(m.FindingsTotal))).
			Set("highFindingsTotal", ojson.IntValue(int64(len(m.High)))).
			Set("referencesTotal", ojson.IntValue(int64(m.ReferencesPageTotal))).
			Set("historicalNotesOmitted", ojson.IntValue(int64(m.HistoricalNotes))).
			Set("resolvedFindingsOmitted", ojson.IntValue(int64(m.ResolvedFindings))).Value()).
		Set("retrieval", ojson.NewObject(3).
			Set("read", ojson.StringValue("workplan_read id="+m.ID)).
			Set("inspect", ojson.StringValue("workplan_inspect id="+m.ID)).
			Set("dependencies", ojson.StringValue(snapshot.SidecarRel(m.ID, ".dependencies.json"))).Value()).
		Set("truncatedFields", ojson.StringsValue(listed)).
		Set("truncatedFieldCount", ojson.IntValue(int64(len(all)))).
		Set("truncatedFieldPathsOmitted", ojson.IntValue(int64(len(all)-len(listed)))).
		Set("instruction", instruction).Value()
}

func (m *Packet) encode(pr params) (ojson.Value, []byte) {
	v := m.build(pr)
	if pr.compact {
		return v, ojson.Compact(v)
	}
	return v, ojson.Pretty(v)
}
