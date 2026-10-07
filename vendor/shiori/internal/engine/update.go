package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// Update placeholder semantics (reference, pinned by
// mutations/update-placeholders): blank strings, empty lists and the
// status "draft" leave the stored value unchanged.

func nonblank(data ojson.Value, key string) (string, bool) {
	s, ok := getStr(data, key)
	if !ok || model.Blank(s) {
		return "", false
	}
	return model.TrimJS(s), true
}

func statusUpdate(data ojson.Value) (string, bool) {
	s, ok := getStr(data, "status")
	if !ok || s == "draft" {
		return "", false
	}
	return s, true
}

func has(data ojson.Value, key string) bool { _, ok := data.Get(key); return ok }

// PrepareUpdate prepares workplan_update (ordinary fields, or explicit
// recovery when "recovery" is set).
func (e *Engine) PrepareUpdate(data ojson.Value) (*Prepared, error) {
	rawID, _ := getStr(data, "id")
	if rec, ok := getStr(data, "recovery"); ok {
		return e.PrepareRecovery(rawID, rec, optHash(data))
	}
	s, rb, err := e.loadForUpdate(rawID, data)
	if err != nil {
		return nil, err
	}
	id := s.ID
	old := s.Plan
	if err := d7Check(old); err != nil {
		return nil, err
	}
	if sidecarsOnly(data) {
		return e.prepareSidecarsOnly(s, data, rb)
	}
	targeted := false
	for _, k := range []string{"updatePhases", "addPhases", "updateSteps", "addSteps", "dependencies"} {
		targeted = targeted || has(data, k)
	}
	if targeted {
		if err := model.UniqueIDError(old); err != nil {
			return nil, err
		}
	}
	p := old.Clone()
	if v, ok := nonblank(data, "title"); ok {
		p.Title = &v
	}
	if v, ok := nonblank(data, "goal"); ok {
		p.Goal = v
	}
	if v, ok := statusUpdate(data); ok {
		p.Status = v
	}
	for _, f := range []struct {
		key string
		dst *[]string
	}{{"scope", &p.Scope}, {"nonGoals", &p.NonGoals}, {"constraints", &p.Constraints}} {
		if l, ok := getList(data, f.key); ok {
			if n := trimDedupe(l); len(n) > 0 {
				*f.dst = n
			}
		}
	}
	touchSpecs := func() {
		if !p.HasSpecFiles {
			p.SpecFilesAdded = true
		}
	}
	if l, ok := getList(data, "specFiles"); ok {
		n, err := e.normalizeSpecList("specFiles", l)
		if err != nil {
			return nil, err
		}
		if len(n) > 0 {
			p.SpecFiles = n
			touchSpecs()
		}
	}
	if l, ok := getList(data, "addSpecFiles"); ok {
		n, err := e.normalizeSpecList("addSpecFiles", l)
		if err != nil {
			return nil, err
		}
		p.SpecFiles = appendDedupe(p.SpecFiles, n)
		touchSpecs()
	}
	if l, ok := getList(data, "removeSpecFiles"); ok {
		remove := map[string]bool{}
		for _, raw := range l {
			t := model.TrimJS(raw)
			if t == "" {
				continue
			}
			rel, err := snapshot.NormalizeSpecFile(e.Root, t)
			if err != nil {
				return nil, err
			}
			remove[rel] = true
		}
		kept := []string{}
		for _, sf := range p.SpecFiles {
			if !remove[sf] {
				kept = append(kept, sf)
			}
		}
		p.SpecFiles = kept
		touchSpecs()
	}
	if l, ok := getList(data, "addRelevantFiles"); ok {
		p.RelevantFiles = appendDedupe(p.RelevantFiles, l)
	}
	if list := getObjs(data, "phases"); len(list) > 0 {
		if p.Phases, err = phasesFromInput(list); err != nil {
			return nil, err
		}
	}
	if list := getObjs(data, "reviewFindings"); len(list) > 0 {
		if p.Findings, err = findingsFromInput(list, 0); err != nil {
			return nil, err
		}
	}
	findPhase := func(pid string) (int, error) {
		for i := range p.Phases {
			if p.Phases[i].ID == pid {
				return i, nil
			}
		}
		return -1, fmt.Errorf("Phase not found: %s", pid)
	}
	findStep := func(pi int, sid string) (int, error) {
		for j := range p.Phases[pi].Steps {
			if p.Phases[pi].Steps[j].ID == sid {
				return j, nil
			}
		}
		return -1, fmt.Errorf("Step not found in phase %s: %s", p.Phases[pi].ID, sid)
	}
	for _, up := range getObjs(data, "updatePhases") {
		pid, _ := getStr(up, "phaseId")
		pi, err := findPhase(pid)
		if err != nil {
			return nil, err
		}
		if v, ok := nonblank(up, "title"); ok {
			p.Phases[pi].Title = v
		}
		if v, ok := statusUpdate(up); ok {
			p.Phases[pi].Status = v
		}
	}
	afterCount := map[string]int{}
	var addedPhases []ojson.Value
	for _, ap := range getObjs(data, "addPhases") {
		pv, _ := ap.Get("phase")
		addedPhases = append(addedPhases, pv)
	}
	for _, ap := range getObjs(data, "addPhases") {
		taken := map[string]bool{}
		for i := range p.Phases {
			taken[p.Phases[i].ID] = true
		}
		pv, _ := ap.Get("phase")
		planSteps := planStepIDs(p)
		for id := range explicitIDs(getObjs(pv, "steps")) {
			planSteps[id] = true
		}
		ph, err := phaseFromInput(pv, len(p.Phases)+1, idScope{taken: taken, reserved: explicitIDs(addedPhases)}, planSteps)
		if err != nil {
			return nil, err
		}
		pos := len(p.Phases)
		if anchor, ok := getStr(ap, "afterPhaseId"); ok {
			ai, err := findPhase(anchor)
			if err != nil {
				return nil, err
			}
			pos = ai + 1 + afterCount[anchor]
			afterCount[anchor]++
		}
		p.Phases = append(p.Phases, model.Phase{})
		copy(p.Phases[pos+1:], p.Phases[pos:])
		p.Phases[pos] = ph
	}
	for _, us := range getObjs(data, "updateSteps") {
		pid, _ := getStr(us, "phaseId")
		sid, _ := getStr(us, "stepId")
		pi, err := findPhase(pid)
		if err != nil {
			return nil, err
		}
		si, err := findStep(pi, sid)
		if err != nil {
			return nil, err
		}
		st := &p.Phases[pi].Steps[si]
		if v, ok := nonblank(us, "title"); ok {
			st.Title = v
		}
		for _, f := range []struct {
			key string
			dst **string
		}{{"target", &st.Target}, {"action", &st.Action}, {"validation", &st.Validation}} {
			if v, ok := nonblank(us, f.key); ok {
				vv := v
				*f.dst = &vv
			}
		}
		if v, ok := statusUpdate(us); ok {
			st.Status = v
		}
	}
	stepAfter := map[string]int{}
	var addedSteps []ojson.Value
	for _, as := range getObjs(data, "addSteps") {
		sv, _ := as.Get("step")
		addedSteps = append(addedSteps, sv)
	}
	for _, as := range getObjs(data, "addSteps") {
		pid, _ := getStr(as, "phaseId")
		pi, err := findPhase(pid)
		if err != nil {
			return nil, err
		}
		ph := &p.Phases[pi]
		taken := map[string]bool{}
		for j := range ph.Steps {
			taken[ph.Steps[j].ID] = true
		}
		sv, _ := as.Get("step")
		st, err := stepFromInput(sv, len(ph.Steps)+1, idScope{taken: taken, reserved: explicitIDs(addedSteps), plan: planStepIDs(p)})
		if err != nil {
			return nil, err
		}
		pos := len(ph.Steps)
		if anchor, ok := getStr(as, "afterStepId"); ok {
			ai, err := findStep(pi, anchor)
			if err != nil {
				return nil, err
			}
			key := pid + "\x00" + anchor
			pos = ai + 1 + stepAfter[key]
			stepAfter[key]++
		}
		ph.Steps = append(ph.Steps, model.Step{})
		copy(ph.Steps[pos+1:], ph.Steps[pos:])
		ph.Steps[pos] = st
	}
	if list := getObjs(data, "addReviewFindings"); len(list) > 0 {
		add, err := findingsFromInput(list, len(p.Findings))
		if err != nil {
			return nil, err
		}
		p.Findings = append(p.Findings, add...)
	}
	if l, ok := getList(data, "appendNotes"); ok {
		if err := checkNotes("appendNotes", l); err != nil {
			return nil, err
		}
		p.Notes = appendDedupe(p.Notes, l)
	}

	// Dependency sidecar: full replacement, validated against the result.
	var depsAfter []byte
	var depsFinal *model.Dependencies // the sidecar the result is checked against
	depsWritten := has(data, "dependencies")
	if depsWritten {
		d := &model.Dependencies{ID: id}
		for _, ev := range getObjs(data, "dependencies") {
			var en model.DependencyEntry
			en.PhaseID, _ = getStr(ev, "phaseId")
			en.StepID, _ = getStr(ev, "stepId")
			for _, rv := range getObjs(ev, "dependsOn") {
				var r model.StepRef
				r.PhaseID, _ = getStr(rv, "phaseId")
				r.StepID, _ = getStr(rv, "stepId")
				en.DependsOn = append(en.DependsOn, r)
			}
			d.Entries = append(d.Entries, en)
		}
		// An entry must name at least one prerequisite.
		var issues []string
		for i, en := range d.Entries {
			if len(en.DependsOn) == 0 {
				issues = append(issues, "dependencies."+strconv.Itoa(i)+".dependsOn: Dependency entry must list at least one prerequisite")
			}
		}
		issues = append(issues, index.ValidateDependencies(index.Build(p), d)...)
		if len(issues) > 0 {
			return nil, errors.New("Invalid dependency metadata: " + strings.Join(issues, "; "))
		}
		d.UpdatedAt = e.nowISO()
		depsAfter = model.EncodeDependencies(d)
		depsFinal = d
	} else if s.Dependencies.Exists {
		// Replacing phases re-validates the stored sidecar in the
		// same prepared write; links it would leave dangling are refused.
		if dv := e.dependencies(s, nil); dv.deps != nil {
			after := index.ValidateDependencies(index.Build(p), dv.deps)
			if len(getObjs(data, "phases")) > 0 {
				before := map[string]bool{}
				for _, is := range dv.issues {
					before[is] = true
				}
				var added []string
				for _, is := range after {
					if !before[is] {
						added = append(added, is)
					}
				}
				if len(added) > 0 {
					return nil, errors.New("Invalid dependency metadata: " + strings.Join(added, "; ") +
						"; the phase replacement would leave these dependency links dangling. Replace the dependencies in the same update.")
				}
			}
			if len(after) == 0 {
				depsFinal = dv.deps
			}
		}
	}
	var warnings []string
	if depsFinal != nil {
		g := index.NewGraph(index.Build(p), depsFinal)
		if depsWritten {
			warnings = graphWarnings(g)
		} else {
			warnings = statusChangeWarnings(old, g)
		}
	}
	ln, lnLedger, lnAfter, lnResult, lnWarnings, err := e.updateLanes(id, data, p)
	if err != nil {
		return nil, err
	}
	ev, evAfter, evResult, evWarnings, err := e.updateEvidence(id, data, old, p, lnLedger)
	if err != nil {
		return nil, err
	}
	lk, lkAfter, lkWarnings, err := e.updateLinks(id, data)
	if err != nil {
		return nil, err
	}
	warnings = append(append(append(warnings, lnWarnings...), evWarnings...), lkWarnings...)
	p.UpdatedAt = e.nowISO()
	if v, ok := statusUpdate(data); ok {
		if err := statusGate(v, p); err != nil {
			return nil, err
		}
	}
	// Steps this call moves to a gated status must carry their own
	// required structure.
	if err := stepStatusGate(old, p); err != nil {
		return nil, err
	}

	// Linked Markdown.
	gen, err := e.generatedMarkdown(s)
	if err != nil {
		return nil, err
	}
	newPF := old.PlanFile
	if raw, ok := getStr(data, "planFile"); ok && !model.Blank(raw) {
		if newPF, err = e.normalizePlanFileInput(raw); err != nil {
			return nil, err
		}
	}
	p.PlanFile = newPF
	moved := newPF != old.PlanFile
	replaceMD := false
	if v, ok := data.Get("replaceMarkdown"); ok {
		replaceMD = v.Bool()
	}
	var mdAfter []byte
	mdWrite := false
	if explicit, ok := nonblankRaw(data, "planMarkdown"); ok {
		if s.Markdown.Exists && !gen && !replaceMD {
			return nil, errors.New(msgHandwrittenUpdate)
		}
		mdAfter, mdWrite = withNewline(explicit), true
	} else if moved {
		if !s.Markdown.Exists {
			return nil, errors.New("Cannot move workplan link because source Markdown is missing; supply a nonblank planMarkdown explicitly")
		}
		if gen {
			if mdAfter, err = e.render(p); err != nil {
				return nil, err
			}
		} else {
			mdAfter = s.Markdown.Bytes
		}
		mdWrite = true
	} else if gen {
		if mdAfter, err = e.render(p); err != nil {
			return nil, err
		}
		mdWrite = true
	}
	mdRendered := mdWrite && gen && !has(data, "planMarkdown")

	specs := []targetSpec{{rel: s.JSON.Rel, kind: "plan", before: s.JSON.Bytes, beforeOK: true, after: p.EncodeStored(), afterOK: true}}
	reads := readsOf(s.StateManifest)
	if mdWrite {
		r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
		cur := s.Markdown
		if moved {
			if cur, err = r.ReadFile(newPF); err != nil {
				return nil, err
			}
			if cur.Exists {
				return nil, fmt.Errorf("Refusing to overwrite existing workplan artifact: %s", cur.Path)
			}
			reads = append(reads, storage.ReadEntry{Rel: newPF, Missing: true})
		}
		specs = append(specs, targetSpec{rel: newPF, kind: "markdown", before: cur.Bytes, beforeOK: cur.Exists, after: mdAfter, afterOK: true})
	}
	if depsAfter != nil {
		specs = append(specs, targetSpec{rel: s.Dependencies.Rel, kind: "dependencies", before: s.Dependencies.Bytes, beforeOK: s.Dependencies.Exists, after: depsAfter, afterOK: true})
	}
	if lnAfter != nil {
		specs = append(specs, targetSpec{rel: ln.rel, kind: "lanes", before: ln.art.Bytes, beforeOK: ln.exists, after: lnAfter, afterOK: true})
	}
	if evAfter != nil {
		specs = append(specs, targetSpec{rel: ev.rel, kind: "evidence", before: ev.art.Bytes, beforeOK: ev.exists, after: evAfter, afterOK: true})
	}
	if lkAfter != nil {
		specs = append(specs, targetSpec{rel: lk.rel, kind: "links", before: lk.art.Bytes, beforeOK: lk.exists, after: lkAfter, afterOK: true})
	}
	var extra []history.Change
	if has(data, "planMarkdown") || moved {
		extra = append(extra, history.Change{Path: "markdown", Op: "changed"})
	}
	if depsAfter != nil {
		extra = append(extra, history.Change{Path: "dependencies", Op: "changed"})
	}
	if err := rb.check(old, p, specs, extra); err != nil {
		return nil, err
	}
	tx := storage.NewUUID()
	in := e.buildIntent("update", id, tx, specs, reads)
	if err := e.checkTargetPaths(in); err != nil {
		return nil, err
	}
	all, mds := newTargets(in)
	if err := e.claimCheck(id, all, mds); err != nil {
		return nil, err
	}
	prep := &Prepared{Tool: "workplan_update", Intent: in}
	prep.recheck = func() error { return e.claimCheck(id, all, mds) }
	prep.result = func(sync bool) (Output, error) {
		if mdRendered {
			e.rememberRendered(in, s.JSON.Rel, newPF, p)
		}
		e.seedPlan(in, s.JSON.Rel, p)
		summary, planHash, stateHash, err := e.postSummary(s, in, p)
		if err != nil {
			return Output{}, err
		}
		b := ojson.NewObject(8).
			Set("updated", ojson.BoolValue(true)).
			Set("path", ojson.StringValue(e.absRel(s.JSON.Rel))).
			Set("planPath", ojson.StringValue(e.absRel(newPF))).
			Set("workplan", summary).
			Set("planHash", ojson.StringValue(planHash)).
			Set("stateHash", ojson.StringValue(stateHash)).
			Set("directorySync", dirSyncValue(sync))
		// Non-failing order warnings, only when present.
		if len(warnings) > 0 {
			b.Set("warnings", ojson.StringsValue(warnings))
		}
		if lnResult != nil {
			b.Set("lanes", *lnResult)
		}
		if evResult != nil {
			b.Set("evidence", *evResult)
		}
		rb.result(b)
		return Output{Value: b.Value()}, nil
	}
	return e.logged(prep, s, p, extra...), nil
}

// nonblankRaw returns an untrimmed string when it is nonblank.
func nonblankRaw(data ojson.Value, key string) (string, bool) {
	s, ok := getStr(data, key)
	if !ok || model.Blank(s) {
		return "", false
	}
	return s, true
}

// PrepareRecovery is implemented in recovery.go.
