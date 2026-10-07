package engine

import (
	"errors"
	"fmt"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

func terminal(status string) bool { return status == "completed" || status == "cancelled" }

// defaultStep picks the checkpoint position within a phase: the first
// non-draft, non-terminal step, else the first draft step.
func defaultStep(ph *model.Phase) int {
	for j := range ph.Steps {
		if st := ph.Steps[j].Status; !terminal(st) && st != "draft" {
			return j
		}
	}
	for j := range ph.Steps {
		if ph.Steps[j].Status == "draft" {
			return j
		}
	}
	return -1
}

func position(ph *model.Phase, j int) *model.Position {
	st := &ph.Steps[j]
	return &model.Position{PhaseID: ph.ID, PhaseTitle: ph.Title, PhaseStatus: ph.Status, StepID: st.ID, StepTitle: st.Title, StepStatus: st.Status}
}

// checkpointPosition resolves the current position (reference rules).
func checkpointPosition(p *model.Plan, phaseID, stepID *string) (*model.Position, error) {
	if phaseID != nil {
		pi := -1
		for i := range p.Phases {
			if p.Phases[i].ID == *phaseID {
				pi = i
				break
			}
		}
		if pi < 0 {
			return nil, fmt.Errorf("Phase not found: %s", *phaseID)
		}
		ph := &p.Phases[pi]
		if terminal(ph.Status) {
			return nil, fmt.Errorf("Current checkpoint phase is terminal: %s", ph.ID)
		}
		if stepID == nil {
			if j := defaultStep(ph); j >= 0 {
				return position(ph, j), nil
			}
			return nil, nil
		}
		for j := range ph.Steps {
			if ph.Steps[j].ID == *stepID {
				if terminal(ph.Steps[j].Status) {
					return nil, errors.New("Current checkpoint step must not be completed or cancelled")
				}
				return position(ph, j), nil
			}
		}
		return nil, fmt.Errorf("Step not found in phase %s: %s", ph.ID, *stepID)
	}
	if stepID != nil {
		var found []struct{ i, j int }
		for i := range p.Phases {
			for j := range p.Phases[i].Steps {
				if p.Phases[i].Steps[j].ID == *stepID {
					found = append(found, struct{ i, j int }{i, j})
				}
			}
		}
		if len(found) != 1 {
			return nil, fmt.Errorf("Step id must identify exactly one phase: %s", *stepID)
		}
		ph := &p.Phases[found[0].i]
		if terminal(ph.Status) || terminal(ph.Steps[found[0].j].Status) {
			return nil, errors.New("Current checkpoint step must not be completed or cancelled")
		}
		return position(ph, found[0].j), nil
	}
	for i := range p.Phases {
		ph := &p.Phases[i]
		if terminal(ph.Status) {
			continue
		}
		if j := defaultStep(ph); j >= 0 {
			return position(ph, j), nil
		}
	}
	return nil, nil
}

func manifestEntries(entries []snapshot.Entry) []model.ManifestEntry {
	out := make([]model.ManifestEntry, len(entries))
	for i, en := range entries {
		out[i] = model.ManifestEntry{Path: en.Path, SHA256: en.SHA256, Missing: en.Missing}
	}
	return out
}

// PrepareCheckpoint prepares workplan_checkpoint (checkpoint v2 binding
// the complete plan manifest; evidence stays unverified).
func (e *Engine) PrepareCheckpoint(data ojson.Value) (*Prepared, error) {
	rawID, _ := getStr(data, "id")
	s, err := e.loadForMutation(rawID, optHash(data))
	if err != nil {
		return nil, err
	}
	id := s.ID
	p := s.Plan
	if err := model.UniqueIDError(p); err != nil {
		return nil, err
	}
	// merge=true keeps every omitted field
	// of the stored checkpoint (fresh, stale or legacy v1). A missing or
	// unreadable checkpoint merges as an empty one.
	old := classifyCheckpoint(s).cp
	merge := false
	if mv, ok := data.Get("merge"); ok && mv.Bool() {
		merge = true
	}
	base := old
	if !merge || base == nil {
		base = &model.Checkpoint{}
	}
	summary, ok := nonblank(data, "summary")
	if !ok && merge && !has(data, "summary") && !model.Blank(base.Summary) {
		summary, ok = base.Summary, true
	}
	if !ok {
		if merge && !has(data, "summary") {
			return nil, mergeMissingError("summary")
		}
		return nil, errors.New("Checkpoint summary cannot be empty")
	}
	next, ok := nonblank(data, "nextAction")
	if !ok && merge && !has(data, "nextAction") && !model.Blank(base.NextAction) {
		next, ok = base.NextAction, true
	}
	if !ok {
		if merge && !has(data, "nextAction") {
			return nil, mergeMissingError("nextAction")
		}
		return nil, errors.New("Checkpoint nextAction cannot be empty")
	}
	var phaseID, stepID *string
	if v, ok := getStr(data, "phaseId"); ok {
		phaseID = &v
	}
	if v, ok := getStr(data, "stepId"); ok {
		stepID = &v
	}
	var cur *model.Position
	var err2 error
	if merge && phaseID == nil && stepID == nil && base.Current != nil {
		// The stored position when it still resolves to an open step;
		// otherwise it is re-derived as when omitted.
		ph, st := base.Current.PhaseID, base.Current.StepID
		if cur, err2 = checkpointPosition(p, &ph, &st); err2 != nil {
			cur, err2 = checkpointPosition(p, nil, nil)
		}
	} else {
		cur, err2 = checkpointPosition(p, phaseID, stepID)
	}
	if err2 != nil {
		return nil, err2
	}
	now := e.nowISO()
	cp := &model.Checkpoint{SchemaVersion: 2, ID: id, SourceUpdatedAt: p.UpdatedAt, PlanHash: s.PlanHash,
		Manifest: manifestEntries(s.PlanManifest), CreatedAt: now, UpdatedAt: now, Status: p.Status,
		Summary: summary, Current: cur, NextAction: next}
	if old != nil && old.CreatedAt != "" {
		cp.CreatedAt = old.CreatedAt
	}
	for _, f := range []struct {
		key  string
		dst  *[]string
		keep []string
	}{{"blockers", &cp.Blockers, base.Blockers}, {"recentValidation", &cp.RecentValidation, base.RecentValidation}, {"guardrails", &cp.Guardrails, base.Guardrails}, {"references", &cp.References, base.References}} {
		l, present := getList(data, f.key)
		if !present && merge {
			l = f.keep
		}
		*f.dst = trimDedupe(l)
	}
	// appendValidation adds to recentValidation (exact duplicates
	// of an existing or earlier line are skipped).
	if av, ok := data.Get("appendValidation"); ok {
		add := []string{av.Str()}
		if av.Kind() == ojson.Array {
			add, _ = getList(data, "appendValidation")
		}
		cp.RecentValidation = appendDedupe(cp.RecentValidation, add)
	}
	var warnings []string
	if old != nil {
		warnings = checkpointDropWarnings(old, cp)
	}
	cpValue := model.CheckpointValue(cp, false)
	in := e.buildIntent("checkpoint", id, storage.NewUUID(), []targetSpec{{
		rel: s.Checkpoint.Rel, kind: "checkpoint", before: s.Checkpoint.Bytes, beforeOK: s.Checkpoint.Exists,
		after: append(ojson.Pretty(cpValue), '\n'), afterOK: true, forceWrite: true,
	}}, readsOf(s.StateManifest))
	if err := e.checkTargetPaths(in); err != nil {
		return nil, err
	}
	prep := &Prepared{Tool: "workplan_checkpoint", Intent: in}
	prep.result = func(sync bool) (Output, error) {
		_, planHash, stateHash, err := e.postSummary(s, in, s.Plan)
		if err != nil {
			return Output{}, err
		}
		out := ojson.NewObject(8).
			Set("checkpointPath", ojson.StringValue(e.absRel(s.Checkpoint.Rel))).
			Set("checkpoint", cpValue).
			Set("planFresh", ojson.BoolValue(len(s.MissingPlanArtifacts) == 0)).
			Set("evidenceStatus", ojson.StringValue("unverified")).
			Set("planHash", ojson.StringValue(planHash)).
			Set("stateHash", ojson.StringValue(stateHash)).
			Set("directorySync", dirSyncValue(sync))
		if len(warnings) > 0 {
			out.Set("warnings", ojson.StringsValue(warnings))
		}
		return Output{Value: out.Value()}, nil
	}
	return e.logged(prep, s, s.Plan), nil
}

// mergeMissingError refuses merge=true without a value to keep: the field
// is omitted and there is no readable stored checkpoint with it.
func mergeMissingError(field string) error {
	return &input.InputError{Tool: "checkpoint", Issues: []model.Issue{{Path: []string{field},
		Message: "merge=true keeps the stored " + field + ", but there is no readable stored checkpoint — pass " + field}}}
}

// checkpointDropWarnings compares the stored checkpoint with the one being
// written: a list whose previous entries are
// not all kept is reported with its counts before → after. Shorter summary
// or nextAction text is not a warning, and blank text is refused before
// this point, so prose never warns.
func checkpointDropWarnings(old, cp *model.Checkpoint) []string {
	var out []string
	for _, f := range []struct {
		key         string
		before, now []string
	}{{"guardrails", old.Guardrails, cp.Guardrails}, {"references", old.References, cp.References},
		{"recentValidation", old.RecentValidation, cp.RecentValidation}, {"blockers", old.Blockers, cp.Blockers}} {
		kept := map[string]bool{}
		for _, x := range f.now {
			kept[x] = true
		}
		dropped := 0
		for _, x := range f.before {
			if !kept[x] {
				dropped++
			}
		}
		if dropped == 0 {
			continue
		}
		noun := "entries"
		if dropped == 1 {
			noun = "entry"
		}
		out = append(out, fmt.Sprintf("%s: %d → %d (%d previous %s not kept; merge=true keeps omitted fields)", f.key, len(f.before), len(f.now), dropped, noun))
	}
	return out
}
