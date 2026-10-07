package engine

import (
	"os"
	"sort"
	"strings"

	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/lanes"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// DefaultDoctorLimit is the doctor page size default.
const DefaultDoctorLimit = 50

const msgInvalidPlans = "One or more primary plans are invalid; see per-plan diagnostics"

// Doctor implements workplan_doctor: read-only diagnostics of roots,
// classification, freshness, locks, pending journals and recovery
// pointers. Host facts it cannot prove stay unknown (null). It never asks
// for approval, repairs, or changes anything.
func (e *Engine) Doctor(in input.DoctorInput) (ojson.Value, error) {
	limit := DefaultDoctorLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	var filterID *string
	if in.ID != nil {
		n, err := normalizeRequested(*in.ID)
		if err != nil {
			return ojson.Value{}, err
		}
		filterID = &n
	}
	l, err := e.scanDir()
	if err != nil {
		return ojson.Value{}, err
	}
	var topIssues []string
	if !l.exists {
		topIssues = append(topIssues, "Workplan directory is missing: "+e.dir())
	}

	// Journals first: a journal-only plan (primary JSON absent) is
	// reported with its interrupted-state hash.
	type pending struct {
		entry   dirEntry
		id      string
		journal *model.Journal
	}
	var journals []pending
	var locks []dirEntry
	var sidecars []dirEntry
	for _, sc := range l.sidecars {
		switch sc.kind {
		case kindArchive:
			continue
		case kindTransaction:
			id := strings.TrimSuffix(sc.name, ".transaction.json")
			pj := pending{entry: sc, id: id}
			r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
			if a, err := r.ReadFile(snapshot.WorkplanDir + "/" + sc.name); err == nil && a.Exists {
				if parsed, err := ojson.Parse(a.Bytes); err == nil {
					if j, ok := model.DecodeJournal(parsed.Value); ok {
						pj.journal = j
					}
				}
			}
			journals = append(journals, pj)
		case kindLock:
			locks = append(locks, sc)
		}
		sidecars = append(sidecars, sc)
	}

	names := l.primary
	primarySet := map[string]bool{}
	for _, n := range names {
		primarySet[n] = true
	}
	if filterID != nil {
		names = nil
		if primarySet[*filterID] {
			names = []string{*filterID}
		}
	}
	plans := []ojson.Value{}
	anyInvalid := false
	returned := 0
	for _, name := range names {
		if returned >= limit {
			break
		}
		returned++
		entry := e.doctorPlan(name)
		if v, _ := entry.Get("valid"); !v.Bool() {
			anyInvalid = true
		}
		plans = append(plans, entry)
	}
	// Journal-only plans (no primary JSON) get an entry that carries
	// the read-only interrupted-state hash so explicit recovery can supply
	// its expectedHash. It uses only the existing plan-entry fields.
	journalOnly := 0
	for _, pj := range journals {
		if primarySet[pj.id] || (filterID != nil && *filterID != pj.id) {
			continue
		}
		journalOnly++
		if returned >= limit {
			continue
		}
		returned++
		var targets []string
		if pj.journal != nil {
			for _, t := range pj.journal.Targets {
				targets = append(targets, t.Path)
			}
		}
		b := ojson.NewObject(5).
			Set("id", ojson.StringValue(pj.id)).
			Set("valid", ojson.BoolValue(false)).
			Set("issues", ojson.StringsValue([]string{"Primary workplan not found: " + pj.id}))
		if sh, err := snapshot.InterruptedStateHash(e.Root, pj.id, e.Limits, targets); err == nil {
			b.Set("stateHash", ojson.StringValue(sh))
		}
		b.Set("recoveryRequired", ojson.BoolValue(true))
		plans = append(plans, b.Value())
	}
	if filterID != nil && len(names) == 0 {
		topIssues = append([]string{"Primary workplan not found: " + *filterID}, topIssues...)
	}

	lockValues := []ojson.Value{}
	for i, lk := range locks {
		if i >= limit {
			break
		}
		v, diag := e.lockValue(lk.name)
		lockValues = append(lockValues, v)
		if diag != "" {
			topIssues = append(topIssues, e.absRel(snapshot.WorkplanDir+"/"+lk.name)+": "+diag)
		}
	}
	pendingValues := []ojson.Value{}
	for i, pj := range journals {
		if i >= limit {
			break
		}
		b := ojson.NewObject(5).
			Set("path", ojson.StringValue(e.absRel(snapshot.WorkplanDir+"/"+pj.entry.name)))
		if pj.journal != nil {
			b.Set("workplanId", ojson.StringValue(pj.journal.WorkplanID)).
				Set("valid", ojson.BoolValue(true)).
				Set("transactionId", ojson.StringValue(pj.journal.TransactionID)).
				Set("targetCount", ojson.IntValue(int64(len(pj.journal.Targets))))
		} else {
			b.Set("workplanId", ojson.StringValue(pj.id)).
				Set("valid", ojson.BoolValue(false)).
				Set("transactionId", ojson.NullValue()).
				Set("targetCount", ojson.NullValue())
		}
		pendingValues = append(pendingValues, b.Value())
	}
	if anyInvalid {
		topIssues = append(topIssues, msgInvalidPlans)
	}
	sidecarValues := []ojson.Value{}
	for i, sc := range sidecars {
		if i >= limit {
			break
		}
		sidecarValues = append(sidecarValues, sidecarValue(sc))
	}
	if topIssues == nil {
		topIssues = []string{}
	}
	planCount := len(l.primary) + journalOnly
	strays := e.strayArtifacts(l)
	out := ojson.NewObject(23).
		Set("requestedRoot", ojson.StringValue(e.RequestedRoot)).
		Set("canonicalRoot", ojson.StringValue(e.Root)).
		Set("directory", ojson.StringValue(e.dir())).
		Set("planCount", ojson.IntValue(int64(planCount))).
		Set("returnedPlans", ojson.IntValue(int64(len(plans)))).
		Set("omittedPlans", ojson.IntValue(int64(len(names)+journalOnly-len(plans)))).
		Set("plans", ojson.ArrayValue(plans)).
		Set("sidecars", ojson.ArrayValue(sidecarValues)).
		Set("sidecarCount", ojson.IntValue(int64(len(sidecars)))).
		Set("omittedSidecars", ojson.IntValue(int64(len(sidecars)-len(sidecarValues)))).
		Set("locks", ojson.ArrayValue(lockValues)).
		Set("lockCount", ojson.IntValue(int64(len(locks)))).
		Set("omittedLocks", ojson.IntValue(int64(len(locks)-len(lockValues)))).
		Set("pendingTransactions", ojson.ArrayValue(pendingValues)).
		Set("pendingTransactionCount", ojson.IntValue(int64(len(journals)))).
		Set("omittedPendingTransactions", ojson.IntValue(int64(len(journals)-len(pendingValues)))).
		Set("runtimeFacts", runtimeFacts(in.RuntimeFacts)).
		Set("issues", ojson.StringsValue(topIssues))
	// Non-failing stray-artifact warnings, present only when there are
	// any. Nothing is moved or deleted.
	if len(strays) > 0 {
		listed := []ojson.Value{}
		warnings := []string{}
		for i, st := range strays {
			if i < limit {
				listed = append(listed, ojson.NewObject(3).
					Set("name", ojson.StringValue(st.name)).
					Set("kind", ojson.StringValue(st.kind)).
					Set("suggestion", ojson.StringValue(st.suggestion)).Value())
			}
			warnings = append(warnings, st.warning())
		}
		if len(warnings) > limit {
			warnings = warnings[:limit]
		}
		out.Set("strayArtifacts", ojson.ArrayValue(listed)).
			Set("strayArtifactCount", ojson.IntValue(int64(len(strays)))).
			Set("omittedStrayArtifacts", ojson.IntValue(int64(len(strays)-len(listed)))).
			Set("warnings", ojson.StringsValue(warnings))
	}
	// The cross-plan view, only when some plan records links.
	if filterID == nil {
		if pf, ok, err := e.Portfolio(); err == nil && ok {
			out.Set("portfolio", pf)
		}
	}
	return out.Set("readOnly", ojson.BoolValue(true)).Value(), nil
}

// strayArtifact is a workplan-root file that belongs to no plan.
type strayArtifact struct {
	name, kind, suggestion string
	id                     string // orphaned sidecar's or stale copy's plan id
	linked                 string // stale copy: the plan's current planFile
}

func (s strayArtifact) warning() string {
	switch s.kind {
	case "orphaned-sidecar":
		return "Orphaned sidecar " + snapshot.WorkplanDir + "/" + s.name + ": no primary plan " + s.id + ".json. " + s.suggestion
	case "stale-markdown":
		return "Stale Markdown copy " + snapshot.WorkplanDir + "/" + s.name + ": plan " + s.id + " now links " + s.linked +
			" (left behind when its planFile moved). It is not part of the plan's hashes and no longer updated. " + s.suggestion
	}
	return "Unclassified file " + snapshot.WorkplanDir + "/" + s.name + " in the workplan root. " + s.suggestion
}

const straySuggestion = "If it is history, move it under " + snapshot.WorkplanDir + "/archive/ (doctor never moves or deletes files)."

// strayArtifacts lists, in UTF-16 order, checkpoint/dependency sidecars
// without a primary plan and root-level files that classify as nothing
// and are not linked Markdown. Journals without a primary are recovery
// state, not strays; temporary and lock files are classified.
func (e *Engine) strayArtifacts(l dirListing) []strayArtifact {
	primary := map[string]bool{}
	for _, n := range l.primary {
		primary[n] = true
	}
	var out []strayArtifact
	for _, sc := range l.sidecars {
		var suffix string
		switch sc.kind {
		case kindCheckpoint:
			suffix = ".checkpoint.json"
		case kindDependencies:
			suffix = ".dependencies.json"
		case kindEvidence:
			suffix = evidence.Suffix
		case kindLanes:
			suffix = lanes.Suffix
		case kindHistory:
			suffix = history.Suffix
		case kindLinks:
			suffix = LinksSuffix
		default:
			continue
		}
		id := strings.TrimSuffix(sc.name, suffix)
		if !primary[id] {
			out = append(out, strayArtifact{name: sc.name, kind: "orphaned-sidecar", id: id, suggestion: straySuggestion})
		}
	}
	var linked map[string]bool
	var planFiles map[string]string
	for _, name := range l.other {
		if strings.HasSuffix(name, ".md") {
			if linked == nil {
				linked, planFiles = e.linkedMarkdown(l.primary)
			}
			if linked[snapshot.WorkplanDir+"/"+name] {
				continue
			}
			if id := strings.TrimSuffix(name, ".md"); primary[id] {
				// <id>.md of a readable plan that links another file is a
				// stale copy left by a planFile move. When the plan cannot
				// be read its link is unknown.
				pf, readable := planFiles[id]
				if !readable {
					continue
				}
				out = append(out, strayArtifact{name: name, kind: "stale-markdown", id: id, linked: pf, suggestion: straySuggestion})
				continue
			}
		}
		out = append(out, strayArtifact{name: name, kind: "unclassified", suggestion: straySuggestion})
	}
	sort.SliceStable(out, func(i, j int) bool { return ojson.CompareUTF16(out[i].name, out[j].name) < 0 })
	return out
}

// linkedMarkdown is the set of planFile links of the readable primary
// plans, and each readable plan's link by id (read-only; unreadable plans
// contribute nothing).
func (e *Engine) linkedMarkdown(names []string) (map[string]bool, map[string]string) {
	out := map[string]bool{}
	byID := map[string]string{}
	for _, n := range names {
		id, err := model.NormalizeID(n)
		if err != nil || id != n {
			continue
		}
		if s, err := e.load(id); err == nil {
			out[s.Plan.PlanFile] = true
			byID[id] = s.Plan.PlanFile
		}
	}
	return out, byID
}

// doctorPlan diagnoses one primary plan by its listed file name.
func (e *Engine) doctorPlan(name string) ojson.Value {
	id, err := model.NormalizeID(name)
	journalExists := false
	if err == nil {
		r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
		if a, rerr := r.ReadFile(snapshot.SidecarRel(id, ".transaction.json")); rerr == nil {
			journalExists = a.Exists
		}
	}
	var s *snapshot.Snapshot
	if err == nil {
		s, err = e.load(id)
	}
	if err != nil {
		b := ojson.NewObject(6).
			Set("id", ojson.StringValue(name)).
			Set("valid", ojson.BoolValue(false)).
			Set("issues", ojson.StringsValue([]string{err.Error()}))
		// Raw-byte hashes for a repair.
		if id != "" && id == name {
			if u := e.unreadable(id, err); u != nil {
				b.Set("planHash", ojson.StringValue(u.PlanHash)).
					Set("stateHash", ojson.StringValue(u.StateHash))
				// The link a repair keeps.
				if pf := e.recoverPlanFile(id, u.JSON.Bytes); pf != "" {
					b.Set("recoveredPlanFile", ojson.StringValue(pf))
				}
			}
		}
		return b.Set("recoveryRequired", ojson.BoolValue(journalExists)).Value()
	}
	p := s.Plan
	issues := model.ValidateStructure(p, &name)
	var missing []string
	missing = append(missing, s.MissingPlanArtifacts...)
	if len(missing) > 0 {
		issues = append(issues, "Missing linked artifacts: "+strings.Join(missing, ", "))
	}
	if s.Markdown.Exists && model.Blank(string(s.Markdown.Bytes)) {
		issues = append(issues, "Linked Markdown is empty: "+s.Markdown.Rel)
	}
	for _, sp := range s.Specs {
		if sp.Exists && model.Blank(string(sp.Bytes)) {
			issues = append(issues, "Linked spec is empty: "+sp.Rel)
		}
	}
	dv := e.dependencies(s, nil)
	for _, is := range dv.issues {
		issues = append(issues, "dependencies: "+is)
	}
	cv := classifyCheckpoint(s)
	if cv.issue != "" {
		issue := "checkpoint: " + cv.issue
		if cv.staleDetail != "" {
			issue += ". " + cv.staleDetail
		}
		issues = append(issues, issue)
	}
	if s.Journal.Exists {
		issues = append(issues, "Recovery required: "+s.Journal.Rel)
	}
	if issues == nil {
		issues = []string{}
	}
	b := ojson.NewObject(10).
		Set("id", ojson.StringValue(name)).
		Set("valid", ojson.BoolValue(len(issues) == 0)).
		Set("issues", ojson.StringsValue(issues))
	w := e.validationWarnings(s, dv)
	if note := e.wipedNote(s); note != "" {
		w = append(w, note)
	}
	if len(w) > 0 {
		b.Set("warnings", ojson.StringsValue(w)) // additive
	}
	b.
		Set("workplan", p.Summary()).
		Set("planHash", ojson.StringValue(s.PlanHash)).
		Set("stateHash", ojson.StringValue(s.StateHash)).
		Set("checkpointFreshness", ojson.StringValue(cv.freshness)).
		Set("dependenciesRecorded", ojson.BoolValue(dv.recorded))
	// The advisory critical path, only when a valid sidecar chains at
	// least two open steps.
	if dv.deps != nil && len(dv.issues) == 0 {
		if cp, ok := criticalPathValue(e.graph(index.Build(p), dv)); ok {
			b.Set("criticalPath", cp)
		}
	}
	// The full compaction advice, only when recommended. Advice only;
	// nothing is archived.
	if a := e.compactionAdvice(s, cv.freshness, true); a != nil {
		b.Set("compactionRecommended", a.DetailValue(p.ID))
	}
	// An invalid ledger never makes the plan invalid.
	if ev, err := e.loadEvidence(p.ID); err == nil && ev.exists {
		b.Set("evidence", e.doctorEvidence(p, ev))
		// Plan quality matters once a plan records evidence (as with the
		// completion warnings, other plans get none).
		if q, ok := planQuality(p); ok {
			b.Set("quality", q)
		}
	}
	if lv, err := e.loadLanes(p.ID); err == nil && lv.exists {
		var g *index.Graph
		if dv.deps != nil && len(dv.issues) == 0 {
			g = e.graph(index.Build(p), dv)
		}
		b.Set("lanes", e.laneDoctor(p, lv, g))
	}
	if hv, ok := e.historyDoctor(s); ok {
		b.Set("history", hv)
	}
	return b.Set("recoveryRequired", ojson.BoolValue(s.Journal.Exists)).Value()
}

// lockValue reports a lock owner without judging liveness from age.
func (e *Engine) lockValue(name string) (ojson.Value, string) {
	rel := snapshot.WorkplanDir + "/" + name
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	a, err := r.ReadFile(rel)
	b := ojson.NewObject(4).Set("path", ojson.StringValue(e.absRel(rel)))
	if err != nil || !a.Exists {
		b.Set("present", ojson.BoolValue(false)).Set("owner", ojson.NullValue()).Set("diagnostic", ojson.NullValue())
		return b.Value(), ""
	}
	b.Set("present", ojson.BoolValue(true))
	parsed, perr := ojson.Parse(a.Bytes)
	if perr != nil {
		diag := "Ambiguous lock owner: " + perr.Error()
		b.Set("owner", ojson.NullValue()).Set("diagnostic", ojson.StringValue(diag))
		return b.Value(), diag
	}
	lo, ok := model.DecodeLockOwner(parsed.Value)
	if !ok {
		diag := "Ambiguous lock owner: metadata does not match lock-owner-v1"
		b.Set("owner", ojson.NullValue()).Set("diagnostic", ojson.StringValue(diag))
		return b.Value(), diag
	}
	b.Set("owner", ojson.NewObject(4).
		Set("hostname", ojson.StringValue(lo.Hostname)).
		Set("pid", ojson.IntValue(lo.PID)).
		Set("nonce", ojson.StringValue(lo.Nonce)).
		Set("startedAt", ojson.StringValue(lo.StartedAt)).Value()).
		Set("diagnostic", ojson.NullValue())
	return b.Value(), ""
}

// runtimeFacts are host facts the core cannot prove; they stay unknown
// until the adapter supplies them. Supplied facts are
// sanitized exactly as the reference does: every field is type-checked,
// lists and strings are bounded (UTF-16 code units), and anything else
// becomes null/unknown.
func runtimeFacts(facts *ojson.Value) ojson.Value {
	var f ojson.Value
	if facts != nil && facts.Kind() == ojson.Object {
		f = *facts
	}
	get := func(v ojson.Value, keys ...string) ojson.Value {
		for _, k := range keys {
			if v.Kind() != ojson.Object {
				return ojson.Value{}
			}
			v, _ = v.Get(k)
		}
		return v
	}
	null := ojson.NullValue()
	str := func(v ojson.Value, max int) ojson.Value {
		if v.Kind() != ojson.String {
			return null
		}
		return ojson.StringValue(sliceUTF16(v.Str(), max))
	}
	boolean := func(v ojson.Value) ojson.Value {
		if v.Kind() != ojson.Bool {
			return null
		}
		return v
	}
	strList := func(v ojson.Value) ojson.Value {
		if v.Kind() != ojson.Array {
			return null
		}
		out := []ojson.Value{}
		for _, el := range v.Elems() {
			if el.Kind() == ojson.String && len(out) < 100 {
				out = append(out, ojson.StringValue(sliceUTF16(el.Str(), 300)))
			}
		}
		return ojson.ArrayValue(out)
	}
	rules := null
	if rv := get(f, "permission", "rules"); rv.Kind() == ojson.Array {
		out := []ojson.Value{}
		elems := rv.Elems()
		if len(elems) > 100 {
			elems = elems[:100]
		}
		for _, el := range elems {
			if el.Kind() != ojson.Object {
				continue
			}
			res, _ := el.Get("resource")
			if res.Kind() != ojson.String {
				continue
			}
			dec := "unknown"
			if d, _ := el.Get("decision"); d.Kind() == ojson.String {
				switch d.Str() {
				case "allow", "deny", "ask", "unknown":
					dec = d.Str()
				}
			}
			b := ojson.NewObject(3).Set("resource", ojson.StringValue(sliceUTF16(res.Str(), 500))).Set("decision", ojson.StringValue(dec))
			if src, _ := el.Get("source"); src.Kind() == ojson.String {
				b.Set("source", ojson.StringValue(sliceUTF16(src.Str(), 200)))
			}
			out = append(out, b.Value())
		}
		rules = ojson.ArrayValue(out)
	}
	status := "unknown"
	if st := get(f, "permission", "status"); st.Kind() == ojson.String && st.Str() == "known" {
		status = "known"
	}
	out := ojson.NewObject(5).
		Set("registrations", ojson.NewObject(2).
			Set("effective", strList(get(f, "registrations", "effective"))).
			Set("configured", strList(get(f, "registrations", "configured"))).Value()).
		Set("plugin", ojson.NewObject(4).
			Set("id", str(get(f, "plugin", "id"), 120)).
			Set("configured", boolean(get(f, "plugin", "configured"))).
			Set("effective", boolean(get(f, "plugin", "effective"))).
			Set("canonicalLocation", str(get(f, "plugin", "canonicalLocation"), 500)).Value()).
		Set("permission", ojson.NewObject(5).
			Set("status", ojson.StringValue(status)).
			Set("agent", str(get(f, "permission", "agent"), 120)).
			Set("sessionID", str(get(f, "permission", "sessionID"), 120)).
			Set("rules", rules).
			Set("detail", str(get(f, "permission", "detail"), 500)).Value()).
		Set("builtinPlan", ojson.NewObject(2).
			Set("configured", boolean(get(f, "builtinPlan", "configured"))).
			Set("effective", boolean(get(f, "builtinPlan", "effective"))).Value())
	// The host version the adapter runs on, the versions it was verified
	// against and whether writes are enabled. Present only when the adapter
	// supplies it.
	if h := get(f, "host"); h.Kind() == ojson.Object {
		writes := null
		if w := get(h, "writes"); w.Kind() == ojson.String && (w.Str() == "enabled" || w.Str() == "disabled") {
			writes = w
		}
		out.Set("host", ojson.NewObject(5).
			Set("opencodeVersion", str(get(h, "opencodeVersion"), 60)).
			Set("verified", boolean(get(h, "verified"))).
			Set("verifiedVersions", strList(get(h, "verifiedVersions"))).
			Set("writes", writes).
			Set("detail", str(get(h, "detail"), 500)).Value())
	}
	return out.Value()
}

// sliceUTF16 keeps the first max UTF-16 code units of s (JavaScript
// String.prototype.slice(0, max)). A surrogate pair cut in half is
// dropped entirely, because a lone surrogate is not representable in
// UTF-8 (declared presentation difference for host strings only).
func sliceUTF16(s string, max int) string {
	n := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > max {
			return s[:i]
		}
		n += w
	}
	return s
}

// wipedNote explains an empty draft plan that a wipe left behind:
// validate still reports the missing phases as an
// issue, and doctor adds where the removed content is archived and what to
// do next. The newest reset:wipe archive under archive/<id>/ is named.
func (e *Engine) wipedNote(s *snapshot.Snapshot) string {
	p := s.Plan
	if len(p.Phases) != 0 || p.Status != "draft" {
		return ""
	}
	dirRel := snapshot.WorkplanDir + "/archive/" + s.ID
	st, err := os.Lstat(e.absRel(dirRel))
	if err != nil || !st.IsDir() {
		return ""
	}
	ents, err := os.ReadDir(e.absRel(dirRel))
	if err != nil {
		return ""
	}
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	best, bestAt := "", ""
	for _, de := range ents {
		name := de.Name()
		if !de.Type().IsRegular() || !strings.HasPrefix(name, "state-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		a, err := r.ReadFile(dirRel + "/" + name)
		if err != nil || !a.Exists {
			continue
		}
		parsed, err := ojson.Parse(a.Bytes)
		if err != nil {
			continue
		}
		op, _ := parsed.Value.Get("operation")
		wid, _ := parsed.Value.Get("workplanId")
		if op.Str() != "reset:wipe" || wid.Str() != s.ID {
			continue
		}
		at, _ := parsed.Value.Get("archivedAt")
		// Whole-second and millisecond timestamps compare by their
		// seconds prefix; ties go to the later name.
		key := strings.TrimSuffix(strings.SplitN(at.Str(), ".", 2)[0], "Z") + "\x00" + name
		if best == "" || key > bestAt {
			best, bestAt = dirRel+"/"+name, key
		}
	}
	if best == "" {
		return ""
	}
	return "phases: Plan was wiped (workplan_reset mode=wipe); the removed content is archived at " + best +
		". Add phases (workplan_update phases or addPhases) to continue; the missing-phases issue stays until then."
}
