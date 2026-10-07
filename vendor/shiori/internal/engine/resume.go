package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/resume"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Resume defaults and bounds.
const (
	DefaultResumeMaxChars = 12000
	DefaultResumeLimit    = 20
	MinResumeMaxChars     = 4096
	MaxResumeMaxChars     = 64000
)

const (
	instructionFresh = "Start from the current step and recorded dependencies; recentValidation is evidence text marked unverified."
	instructionStale = "Checkpoint guidance is not fresh. Reconfirm unverified guardrails, blockers and references before relying on them; nextAction is withheld."
)

// instructionWithheld is the stale/legacy instruction when stored fields
// are withheld: the stale text, then where the withheld fields are and how
// to refresh the checkpoint without losing them. workplan_read does not
// return the checkpoint, so it names the sidecar file.
func instructionWithheld(checkpointRel string) string {
	return instructionStale + " Fields in checkpoint.withheld are hidden, not empty: never copy null/[] into a checkpoint. Full checkpoint: " +
		checkpointRel + " (workplan_read omits it). Refresh via workplan_checkpoint merge=true."
}

// withheldFields names the stored checkpoint fields a non-fresh resume
// packet does not show: summary and nextAction always (a stored
// checkpoint never has them blank), and each of guardrails, references
// and recentValidation that has entries. Blockers are shown, so they are
// never withheld.
func withheldFields(cp *model.Checkpoint) []string {
	out := []string{"summary", "nextAction"}
	if len(cp.Guardrails) > 0 {
		out = append(out, "guardrails")
	}
	if len(cp.References) > 0 {
		out = append(out, "references")
	}
	if len(cp.RecentValidation) > 0 {
		out = append(out, "recentValidation")
	}
	return out
}

// Resume implements workplan_resume: a bounded continuation packet whose
// complete serialized text (UTF-16 code units) never exceeds maxChars.
func (e *Engine) Resume(in input.ResumeInput) (ojson.Value, string, error) {
	id, err := normalizeRequested(in.ID)
	if err != nil {
		return ojson.Value{}, "", err
	}
	s, err := e.load(id)
	if err != nil {
		return ojson.Value{}, "", err
	}
	if s.Journal.Exists {
		v := recoveryPacket(s, "resume")
		return v, string(ojson.Pretty(v)), nil
	}
	if err := model.UniqueIDError(s.Plan); err != nil {
		return ojson.Value{}, "", err
	}
	m, err := e.resumePacket(s, in)
	if err != nil {
		return ojson.Value{}, "", err
	}
	return m.Render()
}

func (e *Engine) resumePacket(s *snapshot.Snapshot, in input.ResumeInput) (*resume.Packet, error) {
	p := s.Plan
	ix := index.Build(p)
	m := &resume.Packet{
		Path: s.JSON.Path, PlanFile: p.PlanFile,
		PlanHash: s.PlanHash, StateHash: s.StateHash,
		PlanFresh: len(s.MissingPlanArtifacts) == 0,
		ID:        p.ID, Title: p.Title, Goal: p.Goal, Status: p.Status,
		Scope: p.Scope, NonGoals: p.NonGoals, Constraints: p.Constraints,
		RelevantFiles: p.RelevantFiles, UpdatedAt: p.UpdatedAt,
		MaxChars: DefaultResumeMaxChars, Limit: DefaultResumeLimit,
		PhaseFilter: in.PhaseID, StepFilter: in.StepID,
		HistoricalNotes: len(p.Notes),
	}
	if in.MaxChars != nil {
		m.MaxChars = *in.MaxChars
	}
	if in.Limit != nil {
		m.Limit = *in.Limit
	}

	// Filters.
	phaseIdx := -1
	if in.PhaseID != nil {
		i, ok := ix.PhaseByID[*in.PhaseID]
		if !ok {
			return nil, fmt.Errorf("Phase not found: %s", *in.PhaseID)
		}
		phaseIdx = i
	}
	if in.StepID != nil {
		n := 0
		for i := range p.Phases {
			if phaseIdx >= 0 && i != phaseIdx {
				continue
			}
			for j := range p.Phases[i].Steps {
				if p.Phases[i].Steps[j].ID == *in.StepID {
					n++
				}
			}
		}
		if n != 1 {
			return nil, fmt.Errorf("Step filter must identify exactly one step: %s", *in.StepID)
		}
	}

	// Checkpoint.
	cv := classifyCheckpoint(s)
	m.CheckpointExists = cv.exists
	m.Freshness = cv.freshness
	m.CheckpointFresh = cv.freshness == FreshnessFresh
	var warnings []string
	// Cancelled-prerequisite warnings go after the checkpoint issue (when
	// there is one) and before the unverified checkpoint lines, so the
	// pinned list cap does not hide current plan facts behind them.
	depAt := 0
	if cv.cp != nil {
		sua := cv.cp.SourceUpdatedAt
		m.SourceUpdatedAt = &sua
		m.Blockers = cv.cp.Blockers
		m.BlockersTotal = len(cv.cp.Blockers)
		m.GuardrailsTotal = len(cv.cp.Guardrails)
		m.ReferencesTotal = len(cv.cp.References)
		if m.CheckpointFresh {
			sum, next := cv.cp.Summary, cv.cp.NextAction
			m.Summary, m.NextAction = &sum, &next
			m.Guardrails = cv.cp.Guardrails
			m.References = cv.cp.References
			m.RecentValidation = cv.cp.RecentValidation
		} else {
			warnings = append(warnings, cv.issue)
			depAt = 1
			m.Withheld = withheldFields(cv.cp)
			// A stale checkpoint names what changed, compactly (the
			// doctor issue has the full detail).
			if cv.freshness == FreshnessStale {
				d := staleDiagnostic(cv.staleChanged)
				m.Diagnostic = &d
			}
			for _, g := range cv.cp.Guardrails {
				warnings = append(warnings, "Unverified checkpoint guardrail: "+g)
			}
			for _, b := range cv.cp.Blockers {
				warnings = append(warnings, "Unverified checkpoint blocker: "+b)
			}
			for _, r := range cv.cp.References {
				warnings = append(warnings, "Unverified checkpoint reference: "+r)
			}
		}
	} else if cv.freshness == FreshnessInvalid {
		d := cv.diagnostic
		m.Diagnostic = &d
		warnings = append(warnings, d)
		depAt = 1
	}
	switch {
	case m.CheckpointFresh:
		m.Instruction = instructionFresh
	case m.Withheld != nil:
		m.Instruction = instructionWithheld(snapshot.SidecarRel(p.ID, ".checkpoint.json"))
	default:
		m.Instruction = instructionStale
	}

	// Current step: a fresh checkpoint's recorded position when it still
	// resolves; otherwise the first in-progress step, else the first
	// unfinished step. Stale guidance never drives it.
	var cur *index.StepKey
	if m.CheckpointFresh && cv.cp.Current != nil {
		k := index.StepKey{PhaseID: cv.cp.Current.PhaseID, StepID: cv.cp.Current.StepID}
		if _, ok := ix.StepByKey[k]; ok {
			cur = &k
		}
	}
	if cur == nil {
		cur = firstActive(p)
	}
	if cur != nil {
		loc := ix.StepByKey[*cur]
		ph := &p.Phases[loc.Phase]
		st := &ph.Steps[loc.Step]
		m.Current = &resume.Current{
			PhaseID: ph.ID, PhaseTitle: ph.Title, PhaseStatus: ph.Status,
			StepID: st.ID, StepTitle: st.Title, StepStatus: st.Status,
			Target: st.Target, Action: st.Action, Validation: st.Validation,
		}
	}

	// Dependencies.
	dv := e.dependencies(s, ix)
	m.DepsRecorded = dv.recorded
	m.DepsValid = len(dv.issues) == 0
	for _, is := range dv.issues {
		warnings = append(warnings, "Dependency metadata warning: "+is)
	}
	if dv.deps != nil && cur != nil {
		for _, en := range dv.deps.Entries {
			if en.PhaseID == cur.PhaseID && en.StepID == cur.StepID {
				m.CurrentDeps = append(m.CurrentDeps, en.DependsOn...)
			}
		}
	}
	// Readiness of the current step and the open items; open steps behind
	// a cancelled prerequisite are flagged.
	g := e.graph(ix, dv)
	if g != nil {
		if cur != nil {
			m.Current.Readiness = resume.ReadinessOf(g, *cur)
		}
		var cancelled []string
		for _, k := range g.Steps() {
			st, _ := g.Status(k)
			if !index.IsOpen(st) {
				continue
			}
			if c := cancelledOf(g.Unmet(k)); len(c) > 0 {
				cancelled = append(cancelled, "Dependency order warning: "+strings.TrimPrefix(cancelledWarning(k, c), "dependencies: "))
			}
		}
		if len(cancelled) > 0 {
			warnings = append(warnings[:depAt:depAt], append(cancelled, warnings[depAt:]...)...)
		}
		// The critical path's length and the first open step on it, when
		// it chains at least two open steps.
		if cp := g.Critical(); len(cp.Steps) >= 2 {
			for _, k := range cp.Steps {
				if st, _ := g.Status(k); index.IsOpen(st) {
					m.Critical = &resume.Critical{Length: len(cp.Steps), Next: k}
					break
				}
			}
		}
	}
	m.Warnings = warnings
	// Advice only; its compact form is shown only when it costs no page
	// content (Packet.Render).
	if a := e.compactionAdvice(s, cv.freshness, false); a != nil {
		v := a.CompactValue()
		m.Compaction = &v
	}
	m.Since = e.sinceCheckpoint(s)
	if w := e.waitingOnPlans(p.ID); len(w) > 0 {
		v := ojson.StringsValue(w)
		m.WaitingOn = &v
	}
	if l := e.lanesOf(p.ID); l != nil {
		v := resumeLanes(l, cur)
		m.Lanes = &v
	}
	if ev, err := e.loadEvidence(p.ID); err == nil && ev.ledger != nil {
		v := resumeEvidence(p, ev.ledger.Views(e.currentTree(ev.ledger, e.lanesOf(p.ID), nil)), cur)
		m.Evidence = &v
	}

	// Findings.
	buckets := index.BuildBuckets(p)
	m.ResolvedFindings = buckets.Resolved
	for _, i := range buckets.High() {
		m.High = append(m.High, resume.FindingOf(p, i))
		m.HighCounts[index.SeverityRank[p.Findings[i].Severity]]++
	}
	low := buckets.Low()
	m.FindingsTotal = len(m.High) + len(low)

	// Page: active work (excluding current), low findings, references.
	activeFiltered := 0
	for i := range p.Phases {
		if phaseIdx >= 0 && i != phaseIdx {
			continue
		}
		ph := &p.Phases[i]
		for j := range ph.Steps {
			st := &ph.Steps[j]
			if !isActive(st.Status) {
				continue
			}
			if in.StepID != nil && st.ID != *in.StepID {
				continue
			}
			if cur != nil && ph.ID == cur.PhaseID && st.ID == cur.StepID {
				continue
			}
			activeFiltered++
			m.Items = append(m.Items, resume.Item{Kind: "active-work",
				PhaseID: ph.ID, PhaseTitle: ph.Title, PhaseStatus: ph.Status,
				StepID: st.ID, Title: st.Title, Status: st.Status,
				Target: st.Target, Action: st.Action, Validation: st.Validation,
				Readiness: resume.ReadinessOf(g, index.StepKey{PhaseID: ph.ID, StepID: st.ID})})
		}
	}
	if g != nil {
		// Ready work first, ranked by how many open steps it unblocks
		// (ties keep plan order), then blocked work in plan order.
		sort.SliceStable(m.Items, func(i, j int) bool {
			a, b := m.Items[i].Readiness, m.Items[j].Readiness
			if a.Ready != b.Ready {
				return a.Ready
			}
			return a.Ready && a.Unblocks > b.Unblocks
		})
	}
	m.ActiveWorkTotal = activeFiltered
	if cur != nil {
		m.ActiveWorkTotal++
	}
	for _, i := range low {
		m.Items = append(m.Items, resume.Item{Kind: "finding", Finding: resume.FindingOf(p, i)})
	}
	for _, r := range p.RelevantFiles {
		m.Items = append(m.Items, resume.Item{Kind: "reference", Reference: r, Source: "current-plan"})
	}
	m.ReferencesPageTotal = len(p.RelevantFiles)
	if m.CheckpointFresh {
		for _, r := range cv.cp.References {
			m.Items = append(m.Items, resume.Item{Kind: "reference", Reference: r, Source: "checkpoint"})
		}
		m.ReferencesPageTotal += len(cv.cp.References)
	}

	// Cursor.
	if in.Cursor != nil {
		c, err := resume.ParseCursor(*in.Cursor)
		if err != nil {
			return nil, err
		}
		if c.StateHash != s.StateHash || c.MaxChars != m.MaxChars || c.Limit != m.Limit ||
			!resume.EqualPtr(c.PhaseID, resume.FilterDigest(in.PhaseID)) || !resume.EqualPtr(c.StepID, resume.FilterDigest(in.StepID)) || c.Offset > len(m.Items) {
			return nil, resume.ErrCursorStale
		}
		m.Offset = c.Offset
	}
	return m, nil
}

func isActive(status string) bool { return status != "completed" && status != "cancelled" }

func firstActive(p *model.Plan) *index.StepKey {
	for i := range p.Phases {
		for j := range p.Phases[i].Steps {
			if p.Phases[i].Steps[j].Status == "in_progress" {
				return &index.StepKey{PhaseID: p.Phases[i].ID, StepID: p.Phases[i].Steps[j].ID}
			}
		}
	}
	for i := range p.Phases {
		for j := range p.Phases[i].Steps {
			if isActive(p.Phases[i].Steps[j].Status) {
				return &index.StepKey{PhaseID: p.Phases[i].ID, StepID: p.Phases[i].Steps[j].ID}
			}
		}
	}
	return nil
}
