package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// Compaction: update → checkpoint → preview → authorized apply. The
// preview token binds the state hash, reason, exact selection, removed
// contents (their digest) and Markdown treatment. Unlike the reference,
// neither the token nor the archive path depends on the absolute project
// root, so the same state previewed at another root yields the same token.

const (
	msgFreshCheckpoint = "A fresh version-2 multiartifact checkpoint is required; write a checkpoint after the latest plan update, then preview again"
	msgWrongToken      = "previewToken does not match the current snapshot, reason, exact selection, removals, or Markdown treatment; preview again before applying"
	notePreviewUnits   = 180
	checkpointRefCap   = 10
)

var protectedFields = []string{"scope", "nonGoals", "constraints", "unfinished phases and steps", "open findings", "handwritten Markdown"}

type compactPlan struct {
	s          *snapshot.Snapshot
	reason     string
	phaseIDs   []string
	noteIdx    []int
	findingIdx []int
	phases     []model.Phase
	gen        bool
	removed    ojson.Value // without digest
	digest     string
	token      string
	archiveRel string
	next       *model.Plan
	in         *storage.Intent
	cpAfter    []byte
	freshness  string
	canonical  ojson.Value
	roll       *advisor.Rollover // note rollover selection, when requested
	jsonAfter  []byte
	mdAfter    []byte
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if model.IsECMAScriptSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func notePreview(s string) string {
	c := collapseSpace(s)
	if ojson.UTF16Len(c) <= notePreviewUnits {
		return c
	}
	t, _ := ojson.TruncateUTF16(c, notePreviewUnits+1)
	return t
}

func intsOf(v ojson.Value, key string) []int {
	x, _ := v.Get(key)
	out := []int{}
	for _, e := range x.Elems() {
		f, _ := e.Float()
		out = append(out, int(f))
	}
	return out
}

// compactSelect validates the selection and computes the token and the
// complete apply intent (shared by preview and apply).
func (e *Engine) compactSelect(data ojson.Value, apply bool) (*compactPlan, error) {
	rawID, _ := getStr(data, "id")
	id, err := normalizeRequested(rawID)
	if err != nil {
		return nil, err
	}
	s, err := e.loadFresh(id)
	if err != nil {
		if apply {
			return nil, e.repairHint(id, err)
		}
		return nil, err
	}
	if s.Journal.Exists {
		return nil, e.errPendingJournal(id)
	}
	if err := model.UniqueIDError(s.Plan); err != nil {
		return nil, err
	}
	cp := &compactPlan{s: s}
	cp.reason, _ = getStr(data, "archiveReason")
	if model.Blank(cp.reason) {
		return nil, errors.New("archiveReason cannot be empty")
	}
	if apply {
		if c, _ := getStr(data, "confirmation"); c != input.ConfirmArchive {
			return nil, errors.New(input.MsgApplyConfirm)
		}
	}
	cp.phaseIDs, _ = getList(data, "completedPhaseIds")
	if cp.phaseIDs == nil {
		cp.phaseIDs = []string{}
	}
	cp.noteIdx = intsOf(data, "noteIndexes")
	cp.findingIdx = intsOf(data, "resolvedFindingIndexes")
	rollIn, hasRoll := data.Get("noteRollover")
	dupInts := func(xs []int) bool {
		seen := map[int]bool{}
		for _, x := range xs {
			if seen[x] {
				return true
			}
			seen[x] = true
		}
		return false
	}
	if dupInts(cp.noteIdx) {
		return nil, errors.New("noteIndexes contains duplicate indexes")
	}
	if dupInts(cp.findingIdx) {
		return nil, errors.New("resolvedFindingIndexes contains duplicate indexes")
	}
	seenPh := map[string]bool{}
	for _, pid := range cp.phaseIDs {
		if seenPh[pid] {
			return nil, errors.New("completedPhaseIds contains duplicates")
		}
		seenPh[pid] = true
	}
	p := s.Plan
	// noteRollover selects the notes.
	if hasRoll {
		if _, ok := data.Get("noteIndexes"); ok {
			return nil, errors.New(input.MsgRolloverWithIndexes)
		}
		keep := advisor.DefaultRolloverKeep
		if k, ok := rollIn.Get("keepLatest"); ok {
			f, _ := k.Float()
			keep = int(f)
		}
		pins := intsOf(rollIn, "pinNoteIndexes")
		if dupInts(pins) {
			return nil, errors.New("noteRollover.pinNoteIndexes contains duplicate indexes")
		}
		for _, i := range pins {
			if i >= len(p.Notes) {
				return nil, fmt.Errorf("noteRollover.pinNoteIndexes: Note index out of range: %d", i)
			}
		}
		r := advisor.SelectRollover(p, keep, pins)
		cp.roll = &r
		cp.noteIdx = append([]int{}, r.Selected()...)
	}
	selPhases := []ojson.Value{}
	removedPhases := []ojson.Value{}
	for _, pid := range cp.phaseIDs {
		pi := -1
		for i := range p.Phases {
			if p.Phases[i].ID == pid {
				pi = i
				break
			}
		}
		if pi < 0 {
			return nil, fmt.Errorf("Phase not found: %s", pid)
		}
		ph := p.Phases[pi]
		ok := ph.Status == "completed" && len(ph.Steps) > 0
		for _, st := range ph.Steps {
			ok = ok && st.Status == "completed"
		}
		if !ok {
			return nil, fmt.Errorf("Only phases with every step explicitly completed can be archived: %s", pid)
		}
		cp.phases = append(cp.phases, ph)
		selPhases = append(selPhases, ojson.NewObject(3).Set("id", ojson.StringValue(ph.ID)).Set("title", ojson.StringValue(ph.Title)).Set("stepCount", ojson.IntValue(int64(len(ph.Steps)))).Value())
		removedPhases = append(removedPhases, ph.ToValue())
	}
	selNotes, removedNotes := []ojson.Value{}, []ojson.Value{}
	for _, i := range cp.noteIdx {
		if i >= len(p.Notes) {
			return nil, fmt.Errorf("Note index out of range: %d", i)
		}
		n := p.Notes[i]
		selNotes = append(selNotes, ojson.NewObject(3).Set("index", ojson.IntValue(int64(i))).Set("preview", ojson.StringValue(notePreview(n))).Set("contentHash", ojson.StringValue(sha256Hex([]byte(ojson.Quote(n))))).Value())
		removedNotes = append(removedNotes, ojson.NewObject(2).Set("index", ojson.IntValue(int64(i))).Set("text", ojson.StringValue(n)).Value())
	}
	selFindings, removedFindings := []ojson.Value{}, []ojson.Value{}
	for _, i := range cp.findingIdx {
		if i >= len(p.Findings) {
			return nil, fmt.Errorf("Review finding index out of range: %d", i)
		}
		f := p.Findings[i]
		if f.Status == nil || *f.Status != "resolved" {
			return nil, fmt.Errorf("Only findings explicitly marked resolved can be archived: %d", i)
		}
		fv := f.ToValue()
		selFindings = append(selFindings, ojson.NewObject(4).Set("index", ojson.IntValue(int64(i))).Set("severity", ojson.StringValue(f.Severity)).Set("title", ojson.StringValue(f.Title)).Set("contentHash", ojson.StringValue(sha256Hex(ojson.Compact(fv)))).Value())
		removedFindings = append(removedFindings, ojson.NewObject(2).Set("index", ojson.IntValue(int64(i))).Set("finding", fv).Value())
	}
	if apply && len(cp.phaseIDs)+len(cp.noteIdx)+len(cp.findingIdx) == 0 {
		return nil, errors.New("Select at least one completed phase, note, or resolved finding to archive")
	}
	cp.gen, err = e.generatedMarkdown(s)
	if err != nil {
		return nil, err
	}
	cp.removed = ojson.NewObject(3).
		Set("completedPhaseIds", ojson.ArrayValue(removedPhases)).
		Set("noteIndexes", ojson.ArrayValue(removedNotes)).
		Set("resolvedFindingIndexes", ojson.ArrayValue(removedFindings)).Value()
	cp.digest = digestWith("workplan-compact-removals-v1", cp.removed)
	treatment := cp.treatment()
	canonical := ojson.NewObject(4).
		Set("completedPhaseIds", ojson.StringsValue(cp.phaseIDs)).
		Set("noteIndexes", intsValue(cp.noteIdx)).
		Set("resolvedFindingIndexes", intsValue(cp.findingIdx))
	if cp.roll != nil {
		// The token binds the rollover parameters as well as the notes
		// they resolved to.
		canonical.Set("noteRollover", cp.roll.InputValue())
	}
	cp.canonical = canonical.Value()
	cp.token = "v1-" + digestWith("workplan-compact-token-v1", ojson.NewObject(6).
		Set("workplanId", ojson.StringValue(id)).
		Set("stateHash", ojson.StringValue(s.StateHash)).
		Set("archiveReason", ojson.StringValue(cp.reason)).
		Set("canonicalSelection", cp.canonical).
		Set("removalsDigest", ojson.StringValue(cp.digest)).
		Set("linkedMarkdownTreatment", ojson.StringValue(treatment)).Value())
	hexTok := strings.TrimPrefix(cp.token, "v1-")
	cp.archiveRel = snapshot.WorkplanDir + "/archive/" + id + "/state-" + s.StateHash[:12] + "-" + hexTok[:12] + ".json"
	cp.freshness = classifyCheckpoint(s).freshness

	// The complete apply result.
	next := p.Clone()
	drop := map[string]bool{}
	for _, pid := range cp.phaseIDs {
		drop[pid] = true
	}
	next.Phases = next.Phases[:0:0]
	for _, ph := range p.Phases {
		if !drop[ph.ID] {
			next.Phases = append(next.Phases, ph)
		}
	}
	dropN := map[int]bool{}
	for _, i := range cp.noteIdx {
		dropN[i] = true
	}
	next.Notes = []string{}
	for i, n := range p.Notes {
		if !dropN[i] {
			next.Notes = append(next.Notes, n)
		}
	}
	next.Notes = appendDedupe(next.Notes, []string{"Compaction archive: " + cp.archiveRel})
	dropF := map[int]bool{}
	for _, i := range cp.findingIdx {
		dropF[i] = true
	}
	next.Findings = []model.Finding{}
	for i, f := range p.Findings {
		if !dropF[i] {
			next.Findings = append(next.Findings, f)
		}
	}
	now := e.nowISO()
	next.UpdatedAt = now
	cp.next = next
	return cp, e.compactIntent(cp, id, hexTok[:16], now)
}

func (cp *compactPlan) treatment() string {
	if cp.gen {
		return "generated-refresh"
	}
	return "preserved"
}

func intsValue(xs []int) ojson.Value {
	out := make([]ojson.Value, len(xs))
	for i, x := range xs {
		out[i] = ojson.IntValue(int64(x))
	}
	return ojson.ArrayValue(out)
}

func digestWith(domain string, v ojson.Value) string {
	return sha256Hex([]byte(domain + "\n" + string(ojson.Compact(v)) + "\n"))
}

func artifactString(a snapshot.Artifact) ojson.Value {
	if !a.Exists {
		return ojson.NullValue()
	}
	return ojson.StringValue(bytesToString(a.Bytes))
}

func (e *Engine) compactIntent(cp *compactPlan, id, tx, now string) error {
	s := cp.s
	archive := ojson.NewObject(7).
		Set("archiveVersion", ojson.IntValue(1)).
		Set("workplanId", ojson.StringValue(id)).
		Set("archivedAt", ojson.StringValue(now)).
		Set("reason", ojson.StringValue(cp.reason)).
		Set("previewToken", ojson.StringValue(cp.token)).
		Set("removed", ojson.ObjectValue(append(append([]ojson.Member{}, cp.removed.Members()...), ojson.Member{Key: "digest", Value: ojson.StringValue(cp.digest)}))).
		Set("source", ojson.NewObject(8).
			Set("workplanJsonPath", ojson.StringValue(s.JSON.Rel)).
			Set("workplanJson", artifactString(s.JSON)).
			Set("linkedMarkdownPath", ojson.StringValue(s.Plan.PlanFile)).
			Set("linkedMarkdown", artifactString(s.Markdown)).
			Set("checkpointPath", ojson.StringValue(s.Checkpoint.Rel)).
			Set("checkpoint", artifactString(s.Checkpoint)).
			Set("dependencyPath", ojson.StringValue(s.Dependencies.Rel)).
			Set("dependencies", artifactString(s.Dependencies)).Value()).Value()
	jsonAfter := cp.next.EncodeStored()
	mdAfter := s.Markdown.Bytes
	if cp.gen {
		var err error
		if mdAfter, err = e.render(cp.next); err != nil {
			return err
		}
	}
	cp.jsonAfter, cp.mdAfter = jsonAfter, mdAfter
	specs := []targetSpec{
		{rel: cp.archiveRel, kind: "archive", after: append(ojson.Pretty(archive), '\n'), afterOK: true},
		{rel: s.JSON.Rel, kind: "plan", before: s.JSON.Bytes, beforeOK: true, after: jsonAfter, afterOK: true, forceWrite: true},
	}
	if cp.gen {
		// Generated Markdown is refreshed; preserved (handwritten or
		// missing) Markdown is not part of the intent at all.
		specs = append(specs, targetSpec{rel: s.Plan.PlanFile, kind: "markdown", before: s.Markdown.Bytes, beforeOK: true, after: mdAfter, afterOK: true, forceWrite: true})
	}
	if s.Dependencies.Exists {
		deps := e.dependencies(s, nil)
		if deps.deps == nil {
			return errors.New("Invalid dependency metadata: " + strings.Join(deps.issues, "; "))
		}
		specs = append(specs, targetSpec{rel: s.Dependencies.Rel, kind: "dependencies", before: s.Dependencies.Bytes, beforeOK: true, after: prunedDependencies(deps.deps, cp.phases, now), afterOK: true, forceWrite: true})
	}
	// Refreshed checkpoint bound to the post-compaction plan manifest.
	ov := map[string][]byte{s.JSON.Rel: jsonAfter}
	if s.Markdown.Exists {
		ov[s.Plan.PlanFile] = mdAfter
	}
	post, err := snapshot.LoadOverlay(e.Root, id, e.Limits, ov)
	if err != nil {
		return err
	}
	cpv := classifyCheckpoint(s)
	if cpv.cp != nil {
		old := cpv.cp
		refs := appendDedupe(old.References, []string{cp.archiveRel})
		if len(refs) > checkpointRefCap {
			refs = refs[len(refs)-checkpointRefCap:]
		}
		c := &model.Checkpoint{SchemaVersion: 2, ID: id, SourceUpdatedAt: cp.next.UpdatedAt, PlanHash: post.PlanHash,
			Manifest: manifestEntries(post.PlanManifest), CreatedAt: old.CreatedAt, UpdatedAt: now, Status: cp.next.Status,
			Summary: old.Summary, Current: old.Current, NextAction: old.NextAction, Blockers: orEmpty(old.Blockers),
			RecentValidation: orEmpty(old.RecentValidation), Guardrails: orEmpty(old.Guardrails), References: refs}
		cp.cpAfter = append(ojson.Pretty(model.CheckpointValue(c, true)), '\n')
		specs = append(specs, targetSpec{rel: s.Checkpoint.Rel, kind: "checkpoint", before: s.Checkpoint.Bytes, beforeOK: true, after: cp.cpAfter, afterOK: true, forceWrite: true})
	}
	reads := readsOf(s.StateManifest)
	reads = append(reads, storage.ReadEntry{Rel: cp.archiveRel, Missing: true})
	cp.in = e.buildIntent("compact:apply", id, tx, specs, reads)
	return e.checkTargetPaths(cp.in)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// prunedDependencies drops entries whose source was archived and records
// terminal summaries for archived prerequisites still referenced.
func prunedDependencies(d *model.Dependencies, removed []model.Phase, now string) []byte {
	gone := map[model.StepRef]model.TerminalSummary{}
	var order []model.StepRef
	for _, ph := range removed {
		for _, st := range ph.Steps {
			r := model.StepRef{PhaseID: ph.ID, StepID: st.ID}
			gone[r] = model.TerminalSummary{StepRef: r, Title: st.Title, Status: st.Status}
			order = append(order, r)
		}
	}
	out := &model.Dependencies{ID: d.ID, UpdatedAt: now, TerminalSummaries: append([]model.TerminalSummary{}, d.TerminalSummaries...)}
	need := map[model.StepRef]bool{}
	for _, en := range d.Entries {
		if _, ok := gone[en.StepRef]; ok {
			continue
		}
		out.Entries = append(out.Entries, en)
		for _, r := range en.DependsOn {
			if _, ok := gone[r]; ok {
				need[r] = true
			}
		}
	}
	have := map[model.StepRef]bool{}
	for _, t := range out.TerminalSummaries {
		have[t.StepRef] = true
	}
	for _, r := range order {
		if need[r] && !have[r] {
			out.TerminalSummaries = append(out.TerminalSummaries, gone[r])
		}
	}
	return model.EncodeDependencies(out)
}

func (cp *compactPlan) counts() ojson.Value {
	open := 0
	for _, f := range cp.next.Findings {
		if f.Open() {
			open++
		}
	}
	return ojson.NewObject(4).
		Set("phaseCount", ojson.IntValue(int64(len(cp.next.Phases)))).
		Set("noteCount", ojson.IntValue(int64(len(cp.next.Notes)))).
		Set("findingCount", ojson.IntValue(int64(len(cp.next.Findings)))).
		Set("openFindingCount", ojson.IntValue(int64(open))).Value()
}

// CompactPreview implements the read-only preview (workplan_compact
// mode=preview and workplan_compact_preview). It never writes.
func (e *Engine) CompactPreview(data ojson.Value) (ojson.Value, error) {
	cp, err := e.compactSelect(data, false)
	if err != nil {
		return ojson.Value{}, err
	}
	s := cp.s
	res := cp.in.Resources()
	// Present targets in staging order (archive first).
	writes := []string{}
	for _, t := range cp.in.Targets {
		writes = append(writes, e.absRel(t.Rel))
	}
	resources := append(append(append([]string{}, writes...), res.Staging...), res.Journal)
	resources = append(resources, lockFirst(res.Lock, cp.in)...)
	resources = append(resources, e.previewDirs(cp.in)...)
	lockPaths := []string{}
	for _, l := range cp.in.Locks {
		lockPaths = append(lockPaths, e.absRel(l.Rel))
	}
	selected := compactSelected(cp)
	out := ojson.NewObject(18).
		Set("mode", ojson.StringValue("preview")).
		Set("workplanId", ojson.StringValue(s.ID)).
		Set("confirmationRequiredForApply", ojson.StringValue(input.ConfirmArchive)).
		Set("requiresFreshCheckpoint", ojson.BoolValue(true)).
		Set("checkpointFreshness", ojson.StringValue(cp.freshness)).
		Set("archiveReason", ojson.StringValue(cp.reason)).
		Set("previewToken", ojson.StringValue(cp.token)).
		Set("planHash", ojson.StringValue(s.PlanHash)).
		Set("stateHash", ojson.StringValue(s.StateHash)).
		Set("canonicalSelection", cp.canonical).
		Set("selected", selected)
	// The rollover breakdown and the exact byte savings of the apply
	// intent, only in rollover mode.
	if cp.roll != nil {
		out.Set("noteRollover", cp.roll.PreviewValue()).
			Set("estimatedSavings", cp.savingsValue())
	}
	// Archived steps that remaining steps still depend on; apply
	// keeps them as terminal summaries. Present only when there are any.
	if ap := e.archivedPrerequisites(cp); len(ap) > 0 {
		out.Set("archivedPrerequisites", ojson.ArrayValue(ap))
	}
	return out.
		Set("removals", ojson.NewObject(4).
			Set("digest", ojson.StringValue(cp.digest)).
			Set("phaseCount", ojson.IntValue(int64(len(cp.phaseIDs)))).
			Set("noteCount", ojson.IntValue(int64(len(cp.noteIdx)))).
			Set("resolvedFindingCount", ojson.IntValue(int64(len(cp.findingIdx)))).Value()).
		Set("activeAfterCompaction", cp.counts()).
		Set("protected", ojson.StringsValue(protectedFields)).
		Set("linkedMarkdownTreatment", ojson.StringValue(cp.treatment())).
		Set("archivePath", ojson.StringValue(e.absRel(cp.archiveRel))).
		Set("writeIntent", ojson.NewObject(5).
			Set("writePaths", ojson.StringsValue(writes)).
			Set("journalPath", ojson.StringValue(res.Journal)).
			Set("lockPaths", ojson.StringsValue(lockPaths)).
			Set("stagingPaths", ojson.StringsValue(res.Staging)).
			Set("resources", ojson.StringsValue(resources)).Value()).Value(), nil
}

// archivedPrerequisites lists each selected step with dependents that
// stay in the plan (valid sidecar only), in selection order.
func (e *Engine) archivedPrerequisites(cp *compactPlan) []ojson.Value {
	if len(cp.phases) == 0 {
		return nil
	}
	ix := index.Build(cp.s.Plan)
	g := e.graph(ix, e.dependencies(cp.s, ix))
	if g == nil {
		return nil
	}
	gone := map[index.StepKey]bool{}
	for _, ph := range cp.phases {
		for _, st := range ph.Steps {
			gone[index.StepKey{PhaseID: ph.ID, StepID: st.ID}] = true
		}
	}
	var out []ojson.Value
	for _, ph := range cp.phases {
		for _, st := range ph.Steps {
			k := index.StepKey{PhaseID: ph.ID, StepID: st.ID}
			var deps []ojson.Value
			for _, d := range g.Dependents(k) {
				if !gone[d] {
					deps = append(deps, stepRefValue(d))
				}
			}
			if len(deps) == 0 {
				continue
			}
			out = append(out, ojson.NewObject(4).
				Set("phaseId", ojson.StringValue(ph.ID)).
				Set("stepId", ojson.StringValue(st.ID)).
				Set("status", ojson.StringValue(st.Status)).
				Set("dependents", ojson.ArrayValue(deps)).Value())
		}
	}
	return out
}

func compactSelected(cp *compactPlan) ojson.Value {
	p := cp.s.Plan
	phases := []ojson.Value{}
	for _, ph := range cp.phases {
		phases = append(phases, ojson.NewObject(3).Set("id", ojson.StringValue(ph.ID)).Set("title", ojson.StringValue(ph.Title)).Set("stepCount", ojson.IntValue(int64(len(ph.Steps)))).Value())
	}
	notes := []ojson.Value{}
	for _, i := range cp.noteIdx {
		n := p.Notes[i]
		notes = append(notes, ojson.NewObject(3).Set("index", ojson.IntValue(int64(i))).Set("preview", ojson.StringValue(notePreview(n))).Set("contentHash", ojson.StringValue(sha256Hex([]byte(ojson.Quote(n))))).Value())
	}
	findings := []ojson.Value{}
	for _, i := range cp.findingIdx {
		f := p.Findings[i]
		findings = append(findings, ojson.NewObject(4).Set("index", ojson.IntValue(int64(i))).Set("severity", ojson.StringValue(f.Severity)).Set("title", ojson.StringValue(f.Title)).Set("contentHash", ojson.StringValue(sha256Hex(ojson.Compact(f.ToValue())))).Value())
	}
	return ojson.NewObject(3).Set("completedPhases", ojson.ArrayValue(phases)).Set("notes", ojson.ArrayValue(notes)).Set("resolvedFindings", ojson.ArrayValue(findings)).Value()
}

// PrepareCompact prepares workplan_compact. Preview returns an Output
// without an intent (read-only); apply returns the bound intent.
func (e *Engine) PrepareCompact(data ojson.Value) (*Prepared, error) {
	mode, _ := getStr(data, "mode")
	if mode != "apply" {
		v, err := e.CompactPreview(data)
		if err != nil {
			return nil, err
		}
		return &Prepared{Tool: "workplan_compact", result: func(bool) (Output, error) { return Output{Value: v}, nil }}, nil
	}
	rawID, _ := getStr(data, "id")
	id, err := normalizeRequested(rawID)
	if err != nil {
		return nil, err
	}
	cp, err := e.compactSelect(data, true)
	if err != nil {
		return nil, err
	}
	if cp.freshness != FreshnessFresh {
		return nil, errors.New(msgFreshCheckpoint)
	}
	if _, err := e.loadForMutation(id, optHash(data)); err != nil {
		return nil, err
	}
	if tok, _ := getStr(data, "previewToken"); tok != cp.token {
		return nil, errors.New(msgWrongToken)
	}
	in := cp.in
	prep := &Prepared{Tool: "workplan_compact", Intent: in}
	prep.result = func(sync bool) (Output, error) {
		post, err := e.postSnapshot(in, id)
		if err != nil {
			return Output{}, err
		}
		out := ojson.NewObject(12).
			Set("compacted", ojson.BoolValue(true)).
			Set("workplanId", ojson.StringValue(id)).
			Set("archivePath", ojson.StringValue(e.absRel(cp.archiveRel))).
			Set("previewToken", ojson.StringValue(cp.token)).
			Set("archived", ojson.NewObject(3).
				Set("phaseCount", ojson.IntValue(int64(len(cp.phaseIDs)))).
				Set("noteCount", ojson.IntValue(int64(len(cp.noteIdx)))).
				Set("resolvedFindingCount", ojson.IntValue(int64(len(cp.findingIdx)))).Value()).
			Set("activeAfterCompaction", cp.counts()).
			Set("linkedMarkdownUpdated", ojson.BoolValue(cp.gen)).
			Set("checkpointRefreshed", ojson.BoolValue(cp.cpAfter != nil))
		if cp.roll != nil {
			out.Set("savings", cp.savingsValue()) // rollover mode only
		}
		return Output{Value: out.
			Set("planHash", ojson.StringValue(post.PlanHash)).
			Set("stateHash", ojson.StringValue(post.StateHash)).
			Set("directorySync", dirSyncValue(sync)).Value()}, nil
	}
	return e.logged(prep, cp.s, nil), nil
}

// lockFirst lists the lock files before the lock-protocol auxiliaries.
func lockFirst(all []string, in *storage.Intent) []string {
	out := []string{}
	locks := map[string]bool{}
	for _, l := range in.Locks {
		p := abs(in.Root, l.Rel)
		locks[p] = true
		out = append(out, p)
	}
	for _, p := range all {
		if !locks[p] {
			out = append(out, p)
		}
	}
	return out
}

func abs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// previewDirs presents directories as the reference does: each target's
// directory in target order, then their parents, never the root.
func (e *Engine) previewDirs(in *storage.Intent) []string {
	seen := map[string]bool{}
	var level []string
	for _, t := range in.Targets {
		d := path.Dir(t.Rel)
		if !seen[d] {
			seen[d] = true
			level = append(level, d)
		}
	}
	out := []string{}
	for len(level) > 0 {
		var next []string
		for _, d := range level {
			out = append(out, e.absRel(d))
			if p := path.Dir(d); p != "." && !seen[p] {
				seen[p] = true
				next = append(next, p)
			}
		}
		level = next
	}
	return out
}

// savingsValue reports the exact plan JSON and Markdown bytes before and
// after the compaction intent (the new updatedAt included).
func (cp *compactPlan) savingsValue() ojson.Value {
	j := advisor.ByteDelta{Before: len(cp.s.JSON.Bytes), After: len(cp.jsonAfter)}
	m := advisor.ByteDelta{Before: len(cp.s.Markdown.Bytes), After: len(cp.s.Markdown.Bytes)}
	if cp.gen {
		m.After = len(cp.mdAfter)
	}
	t := advisor.ByteDelta{Before: j.Before + m.Before, After: j.After + m.After}
	return ojson.NewObject(3).
		Set("json", j.Value()).
		Set("markdown", m.Value(ojson.Member{Key: "treatment", Value: ojson.StringValue(cp.treatment())})).
		Set("total", t.Value()).Value()
}
