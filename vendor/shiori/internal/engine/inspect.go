package engine

import (
	"fmt"
	"strconv"

	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/lanes"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/resume"
)

// DefaultInspectLimit is the inspect page size default.
const DefaultInspectLimit = 100

// Inspect implements workplan_inspect: stable phase/step ids and Markdown
// markers, paged with a snapshot/options-bound checksummed cursor.
func (e *Engine) Inspect(in input.InspectInput) (ojson.Value, error) {
	id, err := normalizeRequested(in.ID)
	if err != nil {
		return ojson.Value{}, err
	}
	s, err := e.load(id)
	if err != nil {
		return ojson.Value{}, err
	}
	if s.Journal.Exists {
		return recoveryPacket(s, "inspect"), nil
	}
	p := s.Plan
	if err := model.UniqueIDError(p); err != nil {
		return ojson.Value{}, err
	}
	ix := index.Build(p)
	limit := DefaultInspectLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	phaseIdx := -1
	if in.PhaseID != nil {
		i, ok := ix.PhaseByID[*in.PhaseID]
		if !ok {
			return ojson.Value{}, fmt.Errorf("Phase not found: %s", *in.PhaseID)
		}
		phaseIdx = i
	}
	// Flatten in document order: each phase followed by its steps.
	type item struct {
		phase int
		step  int // -1 for the phase itself
	}
	var items []item
	for i := range p.Phases {
		if phaseIdx >= 0 && i != phaseIdx {
			continue
		}
		items = append(items, item{i, -1})
		for j := range p.Phases[i].Steps {
			items = append(items, item{i, j})
		}
	}
	offset := 0
	if in.Cursor != nil {
		c, err := resume.ParseInspectCursor(*in.Cursor)
		if err != nil {
			return ojson.Value{}, err
		}
		if c.StateHash != s.StateHash || c.Limit != limit || !resume.EqualPtr(c.PhaseID, in.PhaseID) || c.Offset > len(items) {
			return ojson.Value{}, resume.ErrInspectCursorStale
		}
		offset = c.Offset
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	dv := e.dependencies(s, ix)
	g := e.graph(ix, dv)
	ev, err := e.loadEvidence(id)
	if err != nil {
		return ojson.Value{}, err
	}
	lv, err := e.loadLanes(id)
	if err != nil {
		return ojson.Value{}, err
	}
	owners := stepLanes(lv.ledger)
	var evViews map[model.StepRef]*evidence.StepView
	var evCur evidence.Current
	if ev.ledger != nil {
		evCur = e.currentTree(ev.ledger, e.lanesOf(id), nil)
		evViews = ev.ledger.Views(evCur)
	}
	phases := []ojson.Value{}
	steps := []ojson.Value{}
	for _, it := range items[offset:end] {
		ph := &p.Phases[it.phase]
		if it.step < 0 {
			marker, err := model.PhaseMarker(ph.ID)
			if err != nil {
				return ojson.Value{}, err
			}
			phases = append(phases, ojson.NewObject(8).
				Set("type", ojson.StringValue("phase")).
				Set("id", ojson.StringValue(ph.ID)).
				Set("title", ojson.StringValue(ph.Title)).
				Set("status", ojson.StringValue(ph.Status)).
				Set("index", ojson.IntValue(int64(it.phase))).
				Set("indexLabel", ojson.StringValue(strconv.Itoa(it.phase+1))).
				Set("stepCount", ojson.IntValue(int64(len(ph.Steps)))).
				Set("markdownMarker", ojson.StringValue(marker)).Value())
			continue
		}
		st := &ph.Steps[it.step]
		marker, err := model.StepMarker(st.ID)
		if err != nil {
			return ojson.Value{}, err
		}
		sb := ojson.NewObject(15).
			Set("type", ojson.StringValue("step")).
			Set("phaseId", ojson.StringValue(ph.ID)).
			Set("phaseTitle", ojson.StringValue(ph.Title)).
			Set("id", ojson.StringValue(st.ID)).
			Set("title", ojson.StringValue(st.Title)).
			Set("status", ojson.StringValue(st.Status)).
			Set("target", ojson.NullableString(st.Target)).
			Set("index", ojson.IntValue(int64(it.step))).
			Set("indexPath", ojson.StringValue(strconv.Itoa(it.phase+1)+"."+strconv.Itoa(it.step+1))).
			Set("markdownMarker", ojson.StringValue(marker))
		if g != nil {
			inspectGraphMembers(sb, g, index.StepKey{PhaseID: ph.ID, StepID: st.ID})
		}
		if ln := owners[model.StepRef{PhaseID: ph.ID, StepID: st.ID}]; ln != nil {
			sb.Set("lane", laneRef(ln))
		}
		if evViews != nil {
			if v := evViews[model.StepRef{PhaseID: ph.ID, StepID: st.ID}]; v != nil {
				sb.Set("evidence", v.Value())
			} else {
				sb.Set("evidence", ojson.NewObject(1).Set("state", ojson.StringValue(evidence.StateNone)).Value())
			}
		}
		steps = append(steps, sb.Value())
	}
	next := ojson.NullValue()
	if end < len(items) {
		next = ojson.StringValue(resume.InspectCursor{
			StateHash: s.StateHash, PhaseID: in.PhaseID, Limit: limit, Offset: end,
		}.Encode())
	}
	out := ojson.NewObject(10).
		Set("path", ojson.StringValue(s.JSON.Path)).
		Set("workplan", p.Summary()).
		Set("plan", ojson.NewObject(2).
			Set("path", ojson.StringValue(s.Markdown.Path)).
			Set("exists", ojson.BoolValue(s.Markdown.Exists)).Value()).
		Set("phases", ojson.ArrayValue(phases)).
		Set("steps", ojson.ArrayValue(steps)).
		Set("dependencies", dv.value())
	// The advisory critical path over the whole plan (not the
	// page), only when it chains at least two open steps.
	if cp, ok := criticalPathValue(g); ok {
		out.Set("criticalPath", cp)
	}
	if lv.exists {
		lb := ojson.NewObject(3).
			Set("path", ojson.StringValue(lv.rel)).
			Set("valid", ojson.BoolValue(lv.ledger != nil))
		if lv.ledger == nil {
			lb.Set("issues", ojson.StringsValue(lv.issues))
		} else {
			active := []ojson.Value{}
			for i := range lv.ledger.Lanes {
				if lanes.Active(lv.ledger.Lanes[i].State) {
					active = append(active, laneRef(&lv.ledger.Lanes[i]))
				}
			}
			lb.Set("active", ojson.ArrayValue(active))
		}
		out.Set("lanes", lb.Value())
	}
	if ev.exists {
		eb := ojson.NewObject(4).
			Set("path", ojson.StringValue(ev.rel)).
			Set("valid", ojson.BoolValue(ev.ledger != nil))
		if ev.ledger == nil {
			eb.Set("issues", ojson.StringsValue(ev.issues))
		} else {
			eb.Set("tree", evCur.TreeValue())
		}
		out.Set("evidence", eb.Value())
	}
	return out.
		Set("pagination", ojson.NewObject(5).
			Set("total", ojson.IntValue(int64(len(items)))).
			Set("offset", ojson.IntValue(int64(offset))).
			Set("returned", ojson.IntValue(int64(end-offset))).
			Set("omitted", ojson.IntValue(int64(len(items)-end))).
			Set("nextCursor", next).Value()).
		Set("planHash", ojson.StringValue(s.PlanHash)).
		Set("stateHash", ojson.StringValue(s.StateHash)).Value(), nil
}

// inspectGraphMembers adds the per-step graph view: the
// step's prerequisites with their statuses and its direct dependents;
// for an open step also its readiness, how many open steps it holds up
// (transitively) and its slack against the critical path.
func inspectGraphMembers(b *ojson.Builder, g *index.Graph, k index.StepKey) {
	pre := []ojson.Value{}
	for _, p := range g.Prereqs(k) {
		pre = append(pre, stepRefStatus(p.Key, p.Status))
	}
	deps := []ojson.Value{}
	for _, d := range g.Dependents(k) {
		deps = append(deps, stepRefValue(d))
	}
	b.Set("prerequisites", ojson.ArrayValue(pre)).
		Set("dependents", ojson.ArrayValue(deps))
	if slack, open := g.Slack(k); open {
		readiness := "ready"
		if !g.Ready(k) {
			readiness = "blocked"
		}
		b.Set("readiness", ojson.StringValue(readiness)).
			Set("unblocks", ojson.IntValue(int64(g.Unblocks(k)))).
			Set("slack", floatValue(slack))
	}
}
