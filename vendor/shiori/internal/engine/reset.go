package engine

import (
	"errors"
	"strings"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// workplan_reset modes:
//
//   - "draft" resets status only: the plan, every phase and every step go
//     back to draft and the checkpoint sidecar (execution state) is
//     removed. Phases, steps, notes, review findings, scope, constraints
//     and the dependency graph are kept, so the dependency sidecar stays
//     consistent. Generated (or missing) Markdown is regenerated;
//     handwritten Markdown is kept unless replaceMarkdown=true.
//   - "wipe" clears phases, findings and (unless preserveNotes) notes. It
//     is a preview → confirm flow like compaction: a call without
//     previewToken/confirmation is a read-only preview; the apply call
//     needs the exact preview token and confirmation=WIPE_PLAN_CONTENT,
//     archives the complete originals under archive/<id>/ first and
//     removes the checkpoint and dependency sidecars in the same
//     transaction.
//   - "markdown-only" regenerates the linked Markdown from the stored JSON
//     (unchanged). Handwritten Markdown is replaced only with
//     replaceMarkdown=true. When nothing would change, no intent is
//     prepared and no authorization is requested.

const (
	msgWipeWrongToken = "previewToken does not match the current snapshot, removed content, notes selection, or Markdown treatment; preview the wipe again before applying"
	wipeReason        = "workplan_reset mode=wipe"
)

// PrepareReset prepares workplan_reset.
func (e *Engine) PrepareReset(data ojson.Value) (*Prepared, error) {
	rawID, _ := getStr(data, "id")
	s, err := e.loadForMutation(rawID, optHash(data))
	if err != nil {
		return nil, err
	}
	id := s.ID
	if err := d7Check(s.Plan); err != nil {
		return nil, err
	}
	mode, _ := getStr(data, "mode")
	preserveNotes, replaceMD := false, false
	if v, ok := data.Get("preserveNotes"); ok {
		preserveNotes = v.Bool()
	}
	if v, ok := data.Get("replaceMarkdown"); ok {
		replaceMD = v.Bool()
	}
	_, hasToken := data.Get("previewToken")
	_, hasConfirm := data.Get("confirmation")
	if mode != "wipe" && (hasToken || hasConfirm) {
		return nil, errors.New(input.MsgWipeOnlyOptions)
	}
	gen, err := e.generatedMarkdown(s)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "wipe":
		return e.prepareWipe(s, data, preserveNotes, replaceMD, gen)
	case "draft":
		return e.prepareDraftReset(s, replaceMD, gen)
	}
	if s.Markdown.Exists && !gen && !replaceMD {
		return nil, errors.New(msgHandwrittenReset)
	}
	md, err := e.render(s.Plan)
	if err != nil {
		return nil, err
	}
	specs := []targetSpec{{rel: s.Plan.PlanFile, kind: "markdown", before: s.Markdown.Bytes, beforeOK: s.Markdown.Exists, after: md, afterOK: true}}
	in := e.buildIntent("reset:"+mode, id, storage.NewUUID(), specs, readsOf(s.StateManifest))
	if err := e.checkTargetPaths(in); err != nil {
		return nil, err
	}
	return e.resetResult(&Prepared{Tool: "workplan_reset", Intent: in}, s, mode, s.Plan.PlanFile, nil), nil
}

// resetResult renders the reset output (reference shape; the members the
// reference lacks appear only when they apply).
func (e *Engine) resetResult(prep *Prepared, s *snapshot.Snapshot, mode, planFile string, extra func(b *ojson.Builder)) *Prepared {
	in, id := prep.Intent, s.ID
	prep.result = func(sync bool) (Output, error) {
		post, err := e.postSnapshot(in, id)
		if err != nil {
			return Output{}, err
		}
		b := ojson.NewObject(12).
			Set("reset", ojson.BoolValue(true)).
			Set("mode", ojson.StringValue(mode)).
			Set("path", ojson.StringValue(e.absRel(s.JSON.Rel))).
			Set("planPath", ojson.StringValue(e.absRel(planFile)))
		if extra != nil {
			extra(b)
		}
		return Output{Value: b.
			Set("workplan", post.Plan.Summary()).
			Set("planHash", ojson.StringValue(post.PlanHash)).
			Set("stateHash", ojson.StringValue(post.StateHash)).
			Set("directorySync", dirSyncValue(sync)).Value()}, nil
	}
	return e.logged(prep, s, nil)
}

// prepareDraftReset resets statuses only.
func (e *Engine) prepareDraftReset(s *snapshot.Snapshot, replaceMD, gen bool) (*Prepared, error) {
	p := s.Plan.Clone()
	p.Status = "draft"
	for i := range p.Phases {
		p.Phases[i].Status = "draft"
		for j := range p.Phases[i].Steps {
			p.Phases[i].Steps[j].Status = "draft"
		}
	}
	p.UpdatedAt = e.nowISO()
	specs := []targetSpec{{rel: s.JSON.Rel, kind: "plan", before: s.JSON.Bytes, beforeOK: true, after: p.EncodeStored(), afterOK: true}}
	if !s.Markdown.Exists || gen || replaceMD {
		md, err := e.render(p)
		if err != nil {
			return nil, err
		}
		specs = append(specs, targetSpec{rel: p.PlanFile, kind: "markdown", before: s.Markdown.Bytes, beforeOK: s.Markdown.Exists, after: md, afterOK: true})
	}
	cpRemoved := s.Checkpoint.Exists
	if cpRemoved {
		specs = append(specs, targetSpec{rel: s.Checkpoint.Rel, kind: "checkpoint", before: s.Checkpoint.Bytes, beforeOK: true})
	}
	in := e.buildIntent("reset:draft", s.ID, storage.NewUUID(), specs, readsOf(s.StateManifest))
	if err := e.checkTargetPaths(in); err != nil {
		return nil, err
	}
	var extra func(*ojson.Builder)
	if cpRemoved {
		extra = func(b *ojson.Builder) { b.Set("checkpointRemoved", ojson.BoolValue(true)) }
	}
	return e.resetResult(&Prepared{Tool: "workplan_reset", Intent: in}, s, "draft", p.PlanFile, extra), nil
}

// wipePlan is the complete wipe selection shared by preview and apply.
type wipePlan struct {
	next       *model.Plan
	removed    ojson.Value
	digest     string
	token      string
	treatment  string
	archiveRel string
	in         *storage.Intent
}

func (e *Engine) wipeSelect(s *snapshot.Snapshot, preserveNotes, replaceMD, gen bool) (*wipePlan, error) {
	if s.Markdown.Exists && !gen && !replaceMD {
		return nil, errors.New(msgHandwrittenReset)
	}
	id := s.ID
	p := s.Plan
	w := &wipePlan{}
	next := p.Clone()
	next.Phases = []model.Phase{}
	next.Findings = []model.Finding{}
	phases := []ojson.Value{}
	for i := range p.Phases {
		phases = append(phases, p.Phases[i].ToValue())
	}
	findings := []ojson.Value{}
	for i := range p.Findings {
		findings = append(findings, p.Findings[i].ToValue())
	}
	notes := []string{}
	if !preserveNotes {
		notes = append(notes, p.Notes...)
		next.Notes = []string{}
	}
	next.Status = "draft"
	w.removed = ojson.NewObject(3).
		Set("phases", ojson.ArrayValue(phases)).
		Set("reviewFindings", ojson.ArrayValue(findings)).
		Set("notes", ojson.StringsValue(notes)).Value()
	w.digest = digestWith("workplan-reset-wipe-removals-v1", w.removed)
	switch {
	case !s.Markdown.Exists:
		w.treatment = "created"
	case gen:
		w.treatment = "generated-refresh"
	default:
		w.treatment = "replaced"
	}
	w.token = "v1-" + digestWith("workplan-reset-wipe-token-v1", ojson.NewObject(5).
		Set("workplanId", ojson.StringValue(id)).
		Set("stateHash", ojson.StringValue(s.StateHash)).
		Set("preserveNotes", ojson.BoolValue(preserveNotes)).
		Set("removalsDigest", ojson.StringValue(w.digest)).
		Set("linkedMarkdownTreatment", ojson.StringValue(w.treatment)).Value())
	hexTok := strings.TrimPrefix(w.token, "v1-")
	w.archiveRel = snapshot.WorkplanDir + "/archive/" + id + "/state-" + s.StateHash[:12] + "-" + hexTok[:12] + ".json"
	now := e.nowISO()
	next.UpdatedAt = now
	w.next = next
	archive := ojson.NewObject(8).
		Set("archiveVersion", ojson.IntValue(1)).
		Set("workplanId", ojson.StringValue(id)).
		Set("archivedAt", ojson.StringValue(now)).
		Set("reason", ojson.StringValue(wipeReason)).
		Set("operation", ojson.StringValue("reset:wipe")).
		Set("previewToken", ojson.StringValue(w.token)).
		Set("removed", ojson.ObjectValue(append(append([]ojson.Member{}, w.removed.Members()...), ojson.Member{Key: "digest", Value: ojson.StringValue(w.digest)}))).
		Set("source", ojson.NewObject(8).
			Set("workplanJsonPath", ojson.StringValue(s.JSON.Rel)).
			Set("workplanJson", artifactString(s.JSON)).
			Set("linkedMarkdownPath", ojson.StringValue(p.PlanFile)).
			Set("linkedMarkdown", artifactString(s.Markdown)).
			Set("checkpointPath", ojson.StringValue(s.Checkpoint.Rel)).
			Set("checkpoint", artifactString(s.Checkpoint)).
			Set("dependencyPath", ojson.StringValue(s.Dependencies.Rel)).
			Set("dependencies", artifactString(s.Dependencies)).Value()).Value()
	md, err := e.render(next)
	if err != nil {
		return nil, err
	}
	// The archive is published first, so the complete originals exist
	// before anything is pruned; the sidecars go in the same transaction.
	specs := []targetSpec{
		{rel: w.archiveRel, kind: "archive", after: append(ojson.Pretty(archive), '\n'), afterOK: true},
		{rel: s.JSON.Rel, kind: "plan", before: s.JSON.Bytes, beforeOK: true, after: next.EncodeStored(), afterOK: true, forceWrite: true},
		{rel: p.PlanFile, kind: "markdown", before: s.Markdown.Bytes, beforeOK: s.Markdown.Exists, after: md, afterOK: true},
	}
	if s.Checkpoint.Exists {
		specs = append(specs, targetSpec{rel: s.Checkpoint.Rel, kind: "checkpoint", before: s.Checkpoint.Bytes, beforeOK: true})
	}
	if s.Dependencies.Exists {
		specs = append(specs, targetSpec{rel: s.Dependencies.Rel, kind: "dependencies", before: s.Dependencies.Bytes, beforeOK: true})
	}
	reads := append(readsOf(s.StateManifest), storage.ReadEntry{Rel: w.archiveRel, Missing: true})
	w.in = e.buildIntent("reset:wipe", id, storage.NewUUID(), specs, reads)
	if err := e.checkTargetPaths(w.in); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *wipePlan) removalCounts(s *snapshot.Snapshot) ojson.Value {
	notes, _ := w.removed.Get("notes")
	return ojson.NewObject(7).
		Set("digest", ojson.StringValue(w.digest)).
		Set("phaseCount", ojson.IntValue(int64(len(s.Plan.Phases)))).
		Set("stepCount", ojson.IntValue(int64(s.Plan.StepCount()))).
		Set("findingCount", ojson.IntValue(int64(len(s.Plan.Findings)))).
		Set("noteCount", ojson.IntValue(int64(len(notes.Elems())))).
		Set("checkpointRemoved", ojson.BoolValue(s.Checkpoint.Exists)).
		Set("dependenciesRemoved", ojson.BoolValue(s.Dependencies.Exists)).Value()
}

// prepareWipe is the wipe preview (no intent, read-only) or apply.
func (e *Engine) prepareWipe(s *snapshot.Snapshot, data ojson.Value, preserveNotes, replaceMD, gen bool) (*Prepared, error) {
	tok, hasToken := getStr(data, "previewToken")
	conf, hasConfirm := getStr(data, "confirmation")
	if hasToken || hasConfirm {
		if conf != input.ConfirmWipe {
			return nil, errors.New(input.MsgWipeConfirm)
		}
		if !hasToken {
			return nil, errors.New(input.MsgWipeToken)
		}
	}
	w, err := e.wipeSelect(s, preserveNotes, replaceMD, gen)
	if err != nil {
		return nil, err
	}
	if !hasToken {
		v := e.wipePreview(s, w, preserveNotes)
		return &Prepared{Tool: "workplan_reset", result: func(bool) (Output, error) { return Output{Value: v}, nil }}, nil
	}
	if tok != w.token {
		return nil, errors.New(msgWipeWrongToken)
	}
	return e.resetResult(&Prepared{Tool: "workplan_reset", Intent: w.in}, s, "wipe", s.Plan.PlanFile, func(b *ojson.Builder) {
		b.Set("archivePath", ojson.StringValue(e.absRel(w.archiveRel))).
			Set("previewToken", ojson.StringValue(w.token)).
			Set("removed", w.removalCounts(s)).
			Set("linkedMarkdownTreatment", ojson.StringValue(w.treatment))
	}), nil
}

// wipePreview is the read-only wipe preview: exact removals, archive path,
// write intent and the token/confirmation the apply call needs.
func (e *Engine) wipePreview(s *snapshot.Snapshot, w *wipePlan, preserveNotes bool) ojson.Value {
	res := w.in.Resources()
	writes := []string{}
	deletes := []string{}
	for _, t := range w.in.Targets {
		if t.AfterExists {
			writes = append(writes, e.absRel(t.Rel))
		} else {
			deletes = append(deletes, e.absRel(t.Rel))
		}
	}
	lockPaths := []string{}
	for _, l := range w.in.Locks {
		lockPaths = append(lockPaths, e.absRel(l.Rel))
	}
	resources := append(append(append(append([]string{}, writes...), deletes...), res.Staging...), res.Journal)
	return ojson.NewObject(15).
		Set("mode", ojson.StringValue("wipe")).
		Set("preview", ojson.BoolValue(true)).
		Set("workplanId", ojson.StringValue(s.ID)).
		Set("confirmationRequiredForApply", ojson.StringValue(input.ConfirmWipe)).
		Set("previewToken", ojson.StringValue(w.token)).
		Set("planHash", ojson.StringValue(s.PlanHash)).
		Set("stateHash", ojson.StringValue(s.StateHash)).
		Set("preserveNotes", ojson.BoolValue(preserveNotes)).
		Set("removals", w.removalCounts(s)).
		Set("kept", ojson.StringsValue(wipeKept(preserveNotes))).
		Set("linkedMarkdownTreatment", ojson.StringValue(w.treatment)).
		Set("archivePath", ojson.StringValue(e.absRel(w.archiveRel))).
		Set("writeIntent", ojson.NewObject(6).
			Set("writePaths", ojson.StringsValue(writes)).
			Set("deletePaths", ojson.StringsValue(deletes)).
			Set("journalPath", ojson.StringValue(res.Journal)).
			Set("lockPaths", ojson.StringsValue(lockPaths)).
			Set("stagingPaths", ojson.StringsValue(res.Staging)).
			Set("resources", ojson.StringsValue(resources)).Value()).
		Set("nextStep", ojson.StringValue("Nothing was changed. To apply, call workplan_reset again with mode=wipe, the same preserveNotes and replaceMarkdown, expectedHash="+s.StateHash+", this previewToken and confirmation="+input.ConfirmWipe+". The complete originals are archived at archivePath before anything is removed.")).
		Set("readOnly", ojson.BoolValue(true)).Value()
}

func wipeKept(preserveNotes bool) []string {
	kept := []string{"id", "kind", "title", "goal", "scope", "nonGoals", "constraints", "relevantFiles", "planFile", "specFiles"}
	if preserveNotes {
		kept = append(kept, "notes")
	}
	return kept
}
