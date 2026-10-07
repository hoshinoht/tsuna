package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Cross-plan links (spec 06 X5): <id>.links.json records how a plan
// relates to other plans in the same root. Like evidence and lanes it is
// outside the state manifest; the portfolio view built from all of them
// is read-only and advisory.

// LinksSuffix is the sidecar file suffix after the plan id.
const LinksSuffix = ".links.json"

// Link relations: this plan blocks the other, waits for it, or relates.
var linkRelations = []string{"blocks", "blockedBy", "related"}

// MaxPlanLinks bounds a plan's links.
const MaxPlanLinks = 100

type planLink struct{ PlanID, Relation, Note string }

type linksView struct {
	exists bool
	rel    string
	links  []planLink
	issues []string
	art    snapshot.Artifact
}

func linksRel(id string) string { return snapshot.SidecarRel(id, LinksSuffix) }

func (e *Engine) loadLinks(id string) (linksView, error) {
	v := linksView{rel: linksRel(id)}
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	a, err := r.ReadFile(v.rel)
	if err != nil {
		return v, err
	}
	v.art, v.exists = a, a.Exists
	if !a.Exists {
		return v, nil
	}
	parsed, perr := ojson.Parse(a.Bytes)
	if perr != nil {
		v.issues = []string{"Invalid links JSON at " + a.Path + ": " + perr.Error()}
		return v, nil
	}
	v.links, v.issues = decodeLinks(parsed.Value, id)
	return v, nil
}

func decodeLinks(v ojson.Value, id string) ([]planLink, []string) {
	var issues []string
	if sv, _ := v.Get("schemaVersion"); sv.NumberLiteral() != "1" {
		issues = append(issues, "schemaVersion: expected 1")
	}
	if got, _ := v.Get("id"); got.Str() != id {
		issues = append(issues, "id: expected "+id)
	}
	lv, ok := v.Get("links")
	if !ok || lv.Kind() != ojson.Array {
		return nil, append(issues, "links: expected array")
	}
	var out []planLink
	for i, l := range lv.Elems() {
		pid, _ := l.Get("planId")
		rel, _ := l.Get("relation")
		note, _ := l.Get("note")
		if pid.Kind() != ojson.String || !validRelation(rel.Str()) {
			issues = append(issues, fmt.Sprintf("links.%d: expected planId and a relation (%s)", i, strings.Join(linkRelations, ", ")))
			continue
		}
		out = append(out, planLink{PlanID: pid.Str(), Relation: rel.Str(), Note: note.Str()})
	}
	return out, issues
}

func validRelation(r string) bool {
	for _, x := range linkRelations {
		if x == r {
			return true
		}
	}
	return false
}

func encodeLinks(id, at string, links []planLink) []byte {
	ls := make([]ojson.Value, len(links))
	for i, l := range links {
		b := ojson.NewObject(3).Set("planId", ojson.StringValue(l.PlanID)).Set("relation", ojson.StringValue(l.Relation))
		if l.Note != "" {
			b.Set("note", ojson.StringValue(l.Note))
		}
		ls[i] = b.Value()
	}
	v := ojson.NewObject(4).
		Set("schemaVersion", ojson.IntValue(1)).
		Set("id", ojson.StringValue(id)).
		Set("updatedAt", ojson.StringValue(at)).
		Set("links", ojson.ArrayValue(ls)).Value()
	return append(ojson.Pretty(v), '\n')
}

// updateLinks applies workplan_update.planLinks (a full replacement) and
// returns the new sidecar bytes (nil: no change requested) and warnings
// for links to plans that do not exist yet.
func (e *Engine) updateLinks(id string, data ojson.Value) (linksView, []byte, []string, error) {
	v, err := e.loadLinks(id)
	if err != nil || !has(data, "planLinks") {
		return v, nil, nil, err
	}
	var links []planLink
	seen := map[string]bool{}
	for i, lv := range getObjs(data, "planLinks") {
		raw, _ := getStr(lv, "planId")
		rel, _ := getStr(lv, "relation")
		note, _ := getStr(lv, "note")
		pid, err := model.NormalizeID(raw)
		if err != nil {
			return v, nil, nil, fmt.Errorf("Invalid planLinks input: planLinks.%d.planId: %v", i, err)
		}
		if pid == id {
			return v, nil, nil, fmt.Errorf("Invalid planLinks input: planLinks.%d: a plan cannot link to itself", i)
		}
		if key := pid + "\x00" + rel; seen[key] {
			return v, nil, nil, fmt.Errorf("Invalid planLinks input: planLinks.%d: duplicate %s link to %s", i, rel, pid)
		} else {
			seen[key] = true
		}
		links = append(links, planLink{PlanID: pid, Relation: rel, Note: model.TrimJS(note)})
	}
	ids, _ := e.PlanIDs()
	exists := map[string]bool{}
	for _, x := range ids {
		exists[x] = true
	}
	var warnings []string
	for _, l := range links {
		if !exists[l.PlanID] {
			warnings = append(warnings, "planLinks: plan "+l.PlanID+" does not exist (yet); the link is kept")
		}
	}
	return v, encodeLinks(id, e.nowISO(), links), warnings, nil
}

// portfolioPlan is one plan in the portfolio.
type portfolioPlan struct {
	id, title, status string
	done, total       int
	readable          bool
}

// Portfolio is the read-only cross-plan view: every plan with its
// progress, the "blocks" edges from all links sidecars (blockedBy links
// reversed), which plans wait on unfinished ones, cycles, and links to
// missing plans. ok=false when no plan has links.
func (e *Engine) Portfolio() (ojson.Value, bool, error) {
	ids, err := e.PlanIDs()
	if err != nil {
		return ojson.Value{}, false, err
	}
	plans := map[string]*portfolioPlan{}
	for _, id := range ids {
		pp := &portfolioPlan{id: id}
		if s, err := e.load(id); err == nil {
			pp.readable, pp.status = true, s.Plan.Status
			if s.Plan.Title != nil {
				pp.title = *s.Plan.Title
			}
			for _, ph := range s.Plan.Phases {
				for _, st := range ph.Steps {
					if st.Status != "cancelled" {
						pp.total++
						if st.Status == "completed" {
							pp.done++
						}
					}
				}
			}
		}
		plans[id] = pp
	}
	edges := map[linkEdge]bool{}
	related := map[linkEdge]bool{}
	var issues []string
	hasLinks := false
	for _, id := range ids {
		v, err := e.loadLinks(id)
		if err != nil || !v.exists {
			continue
		}
		hasLinks = true
		for _, is := range v.issues {
			issues = append(issues, id+": "+is)
		}
		for _, l := range v.links {
			if plans[l.PlanID] == nil {
				issues = append(issues, id+": links to missing plan "+l.PlanID)
				continue
			}
			switch l.Relation {
			case "blocks":
				edges[linkEdge{id, l.PlanID}] = true
			case "blockedBy":
				edges[linkEdge{l.PlanID, id}] = true
			default:
				a, b := id, l.PlanID
				if b < a {
					a, b = b, a
				}
				related[linkEdge{a, b}] = true
			}
		}
	}
	if !hasLinks {
		return ojson.Value{}, false, nil
	}
	blockers := map[string][]string{}
	for ed := range edges {
		blockers[ed.to] = append(blockers[ed.to], ed.from)
	}
	finished := func(id string) bool {
		st := plans[id].status
		return st == "completed" || st == "cancelled"
	}
	var list []ojson.Value
	for _, id := range ids {
		pp := plans[id]
		bs := blockers[id]
		sort.Strings(bs)
		var waiting []string
		for _, b := range bs {
			if !finished(b) {
				waiting = append(waiting, b)
			}
		}
		b := ojson.NewObject(7).
			Set("id", ojson.StringValue(id)).
			Set("title", ojson.StringValue(pp.title)).
			Set("status", ojson.StringValue(pp.status)).
			Set("stepsCompleted", ojson.IntValue(int64(pp.done))).
			Set("steps", ojson.IntValue(int64(pp.total))).
			Set("blockedBy", ojson.StringsValue(orEmpty(bs))).
			Set("waitingOn", ojson.StringsValue(orEmpty(waiting)))
		if !pp.readable {
			b.Set("readable", ojson.BoolValue(false))
		}
		list = append(list, b.Value())
	}
	var edgeList, relList []ojson.Value
	for _, ed := range sortedEdges(edges) {
		edgeList = append(edgeList, ojson.NewObject(2).Set("from", ojson.StringValue(ed[0])).Set("to", ojson.StringValue(ed[1])).Value())
	}
	for _, ed := range sortedEdges(related) {
		relList = append(relList, ojson.StringsValue(ed[:]))
	}
	out := ojson.NewObject(5).
		Set("plans", ojson.ArrayValue(list)).
		Set("blocks", ojson.ArrayValue(orEmptyV(edgeList))).
		Set("related", ojson.ArrayValue(orEmptyV(relList)))
	if cyc := blockCycle(ids, blockers); len(cyc) > 0 {
		out.Set("cycle", ojson.StringsValue(cyc))
	}
	return out.Set("issues", ojson.StringsValue(orEmpty(issues))).Value(), true, nil
}

type linkEdge struct{ from, to string }

func sortedEdges(m map[linkEdge]bool) [][2]string {
	var out [][2]string
	for k := range m {
		out = append(out, [2]string{k.from, k.to})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+"\x00"+out[i][1] < out[j][0]+"\x00"+out[j][1] })
	return out
}

func orEmptyV(v []ojson.Value) []ojson.Value {
	if v == nil {
		return []ojson.Value{}
	}
	return v
}

// blockCycle returns one cycle of "blocks" edges, if any.
func blockCycle(ids []string, blockers map[string][]string) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack, found []string
	var visit func(string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, b := range blockers[n] {
			switch color[b] {
			case grey:
				for i, s := range stack {
					if s == b {
						found = append(append([]string{}, stack[i:]...), b)
					}
				}
				return true
			case white:
				if visit(b) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, id := range ids {
		if color[id] == white && visit(id) {
			return found
		}
	}
	return nil
}

// waitingOnPlans lists the unfinished plans that block id (for resume).
func (e *Engine) waitingOnPlans(id string) []string {
	p, ok, err := e.Portfolio()
	if err != nil || !ok {
		return nil
	}
	plans, _ := p.Get("plans")
	for _, pl := range plans.Elems() {
		if pid, _ := pl.Get("id"); pid.Str() == id {
			w, _ := pl.Get("waitingOn")
			var out []string
			for _, x := range w.Elems() {
				out = append(out, x.Str())
			}
			return out
		}
	}
	return nil
}
