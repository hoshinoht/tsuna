package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/lanes"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// ReportActivity is how many recent logged writes a report lists.
const ReportActivity = 10

// Report is a read-only status report of one plan for people who do not
// run agents: progress, open work, evidence (with the commits it
// verified when commits > 0), high findings, lanes and recent activity.
// It returns the JSON form and its Markdown rendering.
func (e *Engine) Report(ctx context.Context, rawID string, commits int) (ojson.Value, string, error) {
	id, err := normalizeRequested(rawID)
	if err != nil {
		return ojson.Value{}, "", err
	}
	s, err := e.load(id)
	if err != nil {
		return ojson.Value{}, "", err
	}
	p := s.Plan
	var md strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&md, format, a...) }
	title := id
	if p.Title != nil && *p.Title != "" {
		title = *p.Title
	}
	total, done := 0, 0
	for _, ph := range p.Phases {
		for _, st := range ph.Steps {
			if st.Status == "cancelled" {
				continue
			}
			total++
			if st.Status == "completed" {
				done++
			}
		}
	}
	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	w("# %s\n\n", mdText(title))
	w("Status report generated %s · state `%s`\n\n", e.nowISO(), s.StateHash[:12])
	w("**Goal:** %s\n\n**Status:** %s · %d of %d steps completed (%d%%)\n\n", mdText(p.Goal), p.Status, done, total, pct)

	// Progress per phase.
	phases := []ojson.Value{}
	w("## Progress\n\n| Phase | Status | Completed |\n| --- | --- | --- |\n")
	for _, ph := range p.Phases {
		n, d := 0, 0
		for _, st := range ph.Steps {
			if st.Status != "cancelled" {
				n++
				if st.Status == "completed" {
					d++
				}
			}
		}
		w("| %s %s | %s | %d/%d |\n", ph.ID, mdCell(ph.Title), ph.Status, d, n)
		phases = append(phases, ojson.NewObject(5).Set("id", ojson.StringValue(ph.ID)).Set("title", ojson.StringValue(ph.Title)).
			Set("status", ojson.StringValue(ph.Status)).Set("completed", ojson.IntValue(int64(d))).Set("steps", ojson.IntValue(int64(n))).Value())
	}
	w("\n")

	// Evidence and the commits it verified.
	var views map[model.StepRef]*evidence.StepView
	if ev, err := e.loadEvidence(id); err == nil && ev.ledger != nil {
		views = ev.ledger.Views(e.currentTree(ev.ledger, e.lanesOf(id), nil))
	}
	var links map[model.StepRef]map[string]CommitLink
	if commits > 0 && views != nil {
		links, _ = e.EvidenceCommits(ctx, id, commits)
	}
	evidenceOf := func(ref model.StepRef) string {
		v := views[ref]
		if v == nil {
			return ""
		}
		out := v.State
		for _, c := range v.Commands {
			if l, ok := links[ref][c.Record.Command]; ok {
				return out + ", verified in " + l.Commit[:12]
			}
		}
		return out
	}

	// Open work, ready steps first when dependencies are recorded.
	var g *index.Graph
	if dv := e.dependencies(s, nil); dv.deps != nil {
		g = e.graph(index.Build(p), dv)
	}
	open := []ojson.Value{}
	w("## Open work\n\n")
	n := 0
	for _, ph := range p.Phases {
		for _, st := range ph.Steps {
			if !isActive(st.Status) {
				continue
			}
			ref := model.StepRef{PhaseID: ph.ID, StepID: st.ID}
			ready := g == nil || g.Ready(index.StepKey{PhaseID: ph.ID, StepID: st.ID})
			line := fmt.Sprintf("- **%s** `%s/%s` %s", st.Status, ph.ID, st.ID, mdText(st.Title))
			if !ready {
				line += " (waiting on prerequisites)"
			}
			if ev := evidenceOf(ref); ev != "" {
				line += " · evidence: " + ev
			}
			w("%s\n", line)
			n++
			b := ojson.NewObject(6).Set("phaseId", ojson.StringValue(ph.ID)).Set("stepId", ojson.StringValue(st.ID)).
				Set("title", ojson.StringValue(st.Title)).Set("status", ojson.StringValue(st.Status)).Set("ready", ojson.BoolValue(ready))
			if ev := evidenceOf(ref); ev != "" {
				b.Set("evidence", ojson.StringValue(ev))
			}
			open = append(open, b.Value())
		}
	}
	if n == 0 {
		w("Nothing open.\n")
	}
	w("\n")
	out := ojson.NewObject(12).
		Set("id", ojson.StringValue(id)).
		Set("title", ojson.StringValue(title)).
		Set("goal", ojson.StringValue(p.Goal)).
		Set("status", ojson.StringValue(p.Status)).
		Set("stateHash", ojson.StringValue(s.StateHash)).
		Set("stepsCompleted", ojson.IntValue(int64(done))).
		Set("steps", ojson.IntValue(int64(total))).
		Set("phases", ojson.ArrayValue(phases)).
		Set("openWork", ojson.ArrayValue(open))

	if cp, ok := criticalPathValue(g); ok {
		steps, _ := cp.Get("steps")
		var parts []string
		for _, st := range steps.Elems() {
			ph, _ := st.Get("phaseId")
			sid, _ := st.Get("stepId")
			parts = append(parts, "`"+ph.Str()+"/"+sid.Str()+"`")
		}
		w("## Critical path\n\n%s\n\n", strings.Join(parts, " → "))
		out.Set("criticalPath", cp)
	}

	// Verified steps: completed work and the commits it was tested in.
	if views != nil {
		verified := []ojson.Value{}
		w("## Evidence\n\n| Step | State | Command | Commit |\n| --- | --- | --- | --- |\n")
		for _, ref := range planOrder(p, views) {
			for _, c := range views[ref].Commands {
				commit := ""
				b := ojson.NewObject(5).Set("phaseId", ojson.StringValue(ref.PhaseID)).Set("stepId", ojson.StringValue(ref.StepID)).
					Set("command", ojson.StringValue(c.Record.Command)).Set("state", ojson.StringValue(c.State))
				if l, ok := links[ref][c.Record.Command]; ok {
					commit = l.Commit[:12] + " (" + l.Match + ")"
					b.Set("commit", ojson.StringValue(l.Commit)).Set("match", ojson.StringValue(l.Match))
				}
				w("| `%s/%s` | %s | `%s` | %s |\n", ref.PhaseID, ref.StepID, c.State, mdCell(c.Record.Command), commit)
				verified = append(verified, b.Value())
			}
		}
		w("\n")
		out.Set("evidence", ojson.ArrayValue(verified))
	}

	if q, ok := planQuality(p); ok {
		w("## Plan quality\n\n")
		if v, ok := q.Get("stepsWithoutValidation"); ok {
			n, _ := v.Get("count")
			w("- %s open steps have no validation.\n", n.NumberLiteral())
		}
		if v, ok := q.Get("stepsWithoutCommand"); ok {
			n, _ := v.Get("count")
			w("- %s open steps have a validation that names no command to run.\n", n.NumberLiteral())
		}
		if v, ok := q.Get("highFindingsWithoutOpenWork"); ok {
			w("- %s open blocker/critical/major findings, but no step is open to resolve them.\n", v.NumberLiteral())
		}
		w("\n")
		out.Set("quality", q)
	}

	// Open blocker, critical and major findings.
	buckets := index.BuildBuckets(p)
	if high := buckets.High(); len(high) > 0 {
		fs := []ojson.Value{}
		w("## Open findings\n\n")
		for _, i := range high {
			f := p.Findings[i]
			w("- **%s** %s\n", f.Severity, mdText(f.Title))
			fs = append(fs, ojson.NewObject(2).Set("severity", ojson.StringValue(f.Severity)).Set("title", ojson.StringValue(f.Title)).Value())
		}
		w("\n")
		out.Set("highFindings", ojson.ArrayValue(fs))
	}

	if l := e.lanesOf(id); l != nil && len(l.Lanes) > 0 {
		ls := []ojson.Value{}
		w("## Lanes\n\n| Lane | State | Steps | Claims |\n| --- | --- | --- | --- |\n")
		for _, ln := range l.Lanes {
			var steps []string
			for _, r := range ln.Steps {
				steps = append(steps, r.PhaseID+"/"+r.StepID)
			}
			w("| %s | %s | %s | %s |\n", ln.ID, ln.State, mdCell(strings.Join(steps, ", ")), mdCell(strings.Join(ln.Claims, ", ")))
			ls = append(ls, laneSummary(ln))
		}
		w("\n")
		out.Set("lanes", ojson.ArrayValue(ls))
	}

	// Recent activity from the change log.
	if lg, err := history.Load(e.absRel(historyRel(id))); err == nil && len(lg.Entries) > 0 {
		recent := lg.Entries
		if len(recent) > ReportActivity {
			recent = recent[len(recent)-ReportActivity:]
		}
		acts := []ojson.Value{}
		w("## Recent activity\n\n")
		for i := len(recent) - 1; i >= 0; i-- {
			en := recent[i]
			w("- %s %s `%s`: %s\n", en.At, en.Source, en.Op, mdText(describeChanges(en.Changes)))
			acts = append(acts, en.Value())
		}
		w("\n")
		out.Set("recentActivity", ojson.ArrayValue(acts))
		if since := e.sinceCheckpoint(s); since != nil {
			out.Set("sinceCheckpoint", *since)
		}
	}
	return out.Value(), md.String(), nil
}

func laneSummary(ln lanes.Lane) ojson.Value {
	steps := make([]string, len(ln.Steps))
	for i, r := range ln.Steps {
		steps[i] = r.PhaseID + "/" + r.StepID
	}
	return ojson.NewObject(4).Set("laneId", ojson.StringValue(ln.ID)).Set("state", ojson.StringValue(ln.State)).
		Set("steps", ojson.StringsValue(steps)).Set("claims", ojson.StringsValue(orEmpty(ln.Claims))).Value()
}

// describeChanges is a one-line summary of an entry's changes.
func describeChanges(cs []history.Change) string {
	if len(cs) == 0 {
		return "no plan change"
	}
	var parts []string
	for i, c := range cs {
		if i == 6 {
			parts = append(parts, fmt.Sprintf("and %d more", len(cs)-i))
			break
		}
		switch {
		case c.Op == "appended":
			parts = append(parts, fmt.Sprintf("%s +%d", c.Path, c.Count))
		case c.From != "" || c.To != "":
			parts = append(parts, c.Path+" "+c.From+"→"+c.To)
		default:
			parts = append(parts, c.Path+" "+c.Op)
		}
	}
	return strings.Join(parts, "; ")
}

// mdText keeps plan text on one line.
func mdText(s string) string { return strings.Join(strings.Fields(s), " ") }

// mdCell is mdText safe inside a table cell.
func mdCell(s string) string { return strings.ReplaceAll(mdText(s), "|", `\|`) }
