package engine

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Input normalization shared by create/update (reference behaviour pinned
// by testdata/vectors/mutations and black-box probes): strings are trimmed
// with the ECMAScript whitespace set; string lists drop blanks and
// duplicates, keeping first occurrences; blank optional strings are
// omitted; a supplied id is normalized (an unnormalizable id is an error),
// an omitted id is generated.

func trimDedupe(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range list {
		t := model.TrimJS(s)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// appendDedupe appends trimmed nonblank items not already present.
func appendDedupe(base, add []string) []string {
	out := append([]string(nil), base...)
	seen := map[string]bool{}
	for _, s := range base {
		seen[s] = true
	}
	for _, s := range add {
		t := model.TrimJS(s)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func optTrim(v ojson.Value, key string) *string {
	x, ok := v.Get(key)
	if !ok {
		return nil
	}
	t := model.TrimJS(x.Str())
	if t == "" {
		return nil
	}
	return &t
}

func getStr(v ojson.Value, key string) (string, bool) {
	x, ok := v.Get(key)
	if !ok {
		return "", false
	}
	return x.Str(), true
}

func getList(v ojson.Value, key string) ([]string, bool) {
	x, ok := v.Get(key)
	if !ok {
		return nil, false
	}
	out := make([]string, len(x.Elems()))
	for i, e := range x.Elems() {
		out[i] = e.Str()
	}
	return out, true
}

func getObjs(v ojson.Value, key string) []ojson.Value {
	x, _ := v.Get(key)
	return x.Elems()
}

var idWords = []string{"amber", "birch", "cedar", "dawn", "ember", "fern", "grove", "harbor", "iris", "juniper", "kestrel", "lark", "maple", "north", "oak", "pine", "quartz", "reef", "river", "sage", "stone", "tide", "umber", "vale", "willow", "yarrow", "path", "field", "brook", "cliff"}

func randInt(n int64) int64 {
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		panic(err)
	}
	return v.Int64()
}

// IDSource, when set (tests only), supplies generated ids.
var IDSource func(prefix string) string

// maxSlugUnits bounds a title slug so a disambiguating suffix still fits
// the 80-unit id limit and ids stay readable.
const maxSlugUnits = 48

// titleSlug is the normalized id form of a title, cut at a word boundary
// to maxSlugUnits ("" when the title has no [a-z0-9]).
func titleSlug(title string) string {
	slug, err := model.NormalizeID(title)
	if err != nil {
		return ""
	}
	if len(slug) > maxSlugUnits {
		slug = slug[:maxSlugUnits]
		if i := strings.LastIndexByte(slug, '-'); i >= maxSlugUnits/2 {
			slug = slug[:i]
		}
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

// generateID returns a readable id for a new phase or step: the slug of
// its title, with a short "-2", "-3", ... suffix
// only when that id is already taken. A title without [a-z0-9] falls back
// to the earlier "<prefix>-<word>-<word>-<6 digits>" form. Existing ids
// never change.
func generateID(prefix, title string, taken ...map[string]bool) string {
	if IDSource != nil {
		return IDSource(prefix)
	}
	used := func(id string) bool {
		for _, t := range taken {
			if t[id] {
				return true
			}
		}
		return false
	}
	if slug := titleSlug(title); slug != "" {
		if !used(slug) {
			return slug
		}
		for n := 2; ; n++ {
			if id := slug + "-" + strconv.Itoa(n); !used(id) {
				return id
			}
		}
	}
	for {
		id := fmt.Sprintf("%s-%s-%s-%06d", prefix, idWords[randInt(int64(len(idWords)))], idWords[randInt(int64(len(idWords)))], randInt(1000000))
		if !used(id) {
			return id
		}
	}
}

// idScope tracks the ids a generated id must avoid: those already taken
// in its scope, explicit ids supplied in the same input (reserved) and,
// for steps, every step id in the plan so a generated step id is also
// unambiguous for stepId-only references and Markdown markers.
type idScope struct {
	taken    map[string]bool
	reserved map[string]bool
	plan     map[string]bool // plan-wide step ids (steps only; may be nil)
}

// explicitIDs collects the normalizable explicit ids of an input list.
func explicitIDs(list []ojson.Value) map[string]bool {
	out := map[string]bool{}
	for _, v := range list {
		if raw, ok := getStr(v, "id"); ok {
			if id, err := model.NormalizeID(raw); err == nil {
				out[id] = true
			}
		}
	}
	return out
}

// planStepIDs is the set of every step id in the plan.
func planStepIDs(p *model.Plan) map[string]bool {
	out := map[string]bool{}
	for i := range p.Phases {
		for j := range p.Phases[i].Steps {
			out[p.Phases[i].Steps[j].ID] = true
		}
	}
	return out
}

// stepFromInput builds a step; position is the 1-based number used in the
// reference's missing-title message.
func stepFromInput(v ojson.Value, position int, sc idScope) (model.Step, error) {
	var st model.Step
	title := ""
	if t, ok := getStr(v, "title"); ok {
		title = model.TrimJS(t)
	}
	if title == "" {
		return st, fmt.Errorf("Step %d is missing a title", position)
	}
	if raw, ok := getStr(v, "id"); ok {
		id, err := model.NormalizeID(raw)
		if err != nil {
			return st, err
		}
		st.ID = id
	} else {
		st.ID = generateID("step", title, sc.taken, sc.reserved, sc.plan)
	}
	if sc.taken[st.ID] {
		return st, fmt.Errorf("Duplicate step id: %s", st.ID)
	}
	sc.taken[st.ID] = true
	if sc.plan != nil {
		sc.plan[st.ID] = true
	}
	st.Title = title
	st.Target = optTrim(v, "target")
	st.Action = optTrim(v, "action")
	st.Validation = optTrim(v, "validation")
	st.Status = "draft"
	if s, ok := getStr(v, "status"); ok {
		st.Status = s
	}
	return st, nil
}

func phaseFromInput(v ojson.Value, position int, sc idScope, planSteps map[string]bool) (model.Phase, error) {
	var ph model.Phase
	title := ""
	if t, ok := getStr(v, "title"); ok {
		title = model.TrimJS(t)
	}
	if title == "" {
		return ph, fmt.Errorf("Phase %d is missing a title", position)
	}
	if raw, ok := getStr(v, "id"); ok {
		id, err := model.NormalizeID(raw)
		if err != nil {
			return ph, err
		}
		ph.ID = id
	} else {
		ph.ID = generateID("phase", title, sc.taken, sc.reserved)
	}
	if sc.taken[ph.ID] {
		return ph, fmt.Errorf("Duplicate phase id: %s", ph.ID)
	}
	sc.taken[ph.ID] = true
	ph.Title = title
	ph.Status = "draft"
	if s, ok := getStr(v, "status"); ok {
		ph.Status = s
	}
	steps := getObjs(v, "steps")
	stepScope := idScope{taken: map[string]bool{}, reserved: explicitIDs(steps), plan: planSteps}
	ph.Steps = []model.Step{}
	for i, sv := range steps {
		st, err := stepFromInput(sv, i+1, stepScope)
		if err != nil {
			return ph, err
		}
		ph.Steps = append(ph.Steps, st)
	}
	return ph, nil
}

// phasesFromInput builds a complete phase list (create, or update
// {phases} as a full replacement).
func phasesFromInput(list []ojson.Value) ([]model.Phase, error) {
	sc := idScope{taken: map[string]bool{}, reserved: explicitIDs(list)}
	planSteps := map[string]bool{}
	for _, pv := range list {
		for id := range explicitIDs(getObjs(pv, "steps")) {
			planSteps[id] = true
		}
	}
	out := []model.Phase{}
	for i, pv := range list {
		ph, err := phaseFromInput(pv, i+1, sc, planSteps)
		if err != nil {
			return nil, err
		}
		out = append(out, ph)
	}
	return out, nil
}

func findingFromInput(v ojson.Value, position int) (model.Finding, error) {
	var f model.Finding
	t, _ := getStr(v, "title")
	f.Title = model.TrimJS(t)
	if f.Title == "" {
		return f, fmt.Errorf("Finding %d is missing a title", position)
	}
	f.Severity, _ = getStr(v, "severity")
	f.Detail = optTrim(v, "detail")
	f.Source = optTrim(v, "source")
	st := "open"
	if s, ok := getStr(v, "status"); ok {
		st = s
	}
	f.Status = &st
	return f, nil
}

func findingsFromInput(list []ojson.Value, offset int) ([]model.Finding, error) {
	out := []model.Finding{}
	for i, fv := range list {
		f, err := findingFromInput(fv, offset+i+1)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// backslashError refuses a new link containing '\' (the manifest would
// rewrite it to '/', aliasing another path).
func backslashError(field, raw string) error {
	return fmt.Errorf("%s: Linked path contains a backslash and has an ambiguous manifest identity: %s", field, raw)
}

// normalizeSpecList trims, normalizes, dedupes and refuses backslashes.
func (e *Engine) normalizeSpecList(field string, list []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for i, raw := range list {
		t := model.TrimJS(raw)
		if t == "" {
			continue
		}
		if strings.Contains(t, `\`) {
			return nil, backslashError(field+"."+strconv.Itoa(i), raw)
		}
		rel, err := snapshot.NormalizeSpecFile(e.Root, t)
		if err != nil {
			return nil, err
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		// A new link must name an existing file, like the dependency
		// checks, before anything is authorized.
		if err := e.specExists(field+"."+strconv.Itoa(i), rel); err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, nil
}

// SpecFileMissingError refuses a specFiles link to a file that does not
// exist.
type SpecFileMissingError struct{ Field, Rel string }

func (e *SpecFileMissingError) Error() string {
	return e.Field + ": Linked spec file does not exist: " + e.Rel + ". Create the file first, or leave it out of the list."
}

func (e *Engine) specExists(field, rel string) error {
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	a, err := r.ReadFile(rel)
	if err != nil {
		return err
	}
	if !a.Exists {
		return &SpecFileMissingError{Field: field, Rel: rel}
	}
	return nil
}

// MaxNoteBytes is the per-note size limit for new notes. Stored notes are
// never changed.
const MaxNoteBytes = 16 << 10

// NoteTooLargeError refuses a new note above MaxNoteBytes.
type NoteTooLargeError struct {
	Field string
	Size  int
}

func (e *NoteTooLargeError) Error() string {
	return e.Field + ": Note is " + strconv.Itoa(e.Size) + " bytes, above the " + strconv.Itoa(MaxNoteBytes) +
		"-byte (16 KiB) per-note limit. Keep notes short; put long evidence in a file and reference its path."
}

// checkNotes applies the per-note limit to new (trimmed) notes.
func checkNotes(field string, list []string) error {
	for i, n := range list {
		if t := model.TrimJS(n); len(t) > MaxNoteBytes {
			return &NoteTooLargeError{Field: field + "." + strconv.Itoa(i), Size: len(t)}
		}
	}
	return nil
}

// normalizePlanFileInput trims and applies the plan-file policy.
func (e *Engine) normalizePlanFileInput(raw string) (string, error) {
	t := model.TrimJS(raw)
	if strings.Contains(t, `\`) {
		return "", backslashError("planFile", raw)
	}
	return snapshot.NormalizePlanFile(e.Root, t)
}

// withNewline is the reference's explicit-Markdown rule: content is
// written with exactly one appended newline when it lacks a final one.
func withNewline(s string) []byte {
	if strings.HasSuffix(s, "\n") {
		return []byte(s)
	}
	return []byte(s + "\n")
}

// d7Check reports ids that cannot produce a Markdown marker, with their
// field paths (the reference fails without a path).
func d7Check(p *model.Plan) error {
	var issues []string
	check := func(path, id string) {
		if model.Blank(id) {
			issues = append(issues, path+": Must not be empty")
		} else if _, err := model.NormalizeID(id); err != nil {
			issues = append(issues, path+": "+err.Error())
		}
	}
	for i := range p.Phases {
		pp := "phases." + strconv.Itoa(i)
		check(pp+".id", p.Phases[i].ID)
		for j := range p.Phases[i].Steps {
			check(pp+".steps."+strconv.Itoa(j)+".id", p.Phases[i].Steps[j].ID)
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return &D7Error{Issues: issues}
}

// D7Error lists unrenderable ids with field paths.
type D7Error struct{ Issues []string }

func (e *D7Error) Error() string {
	return strings.Join(e.Issues, "; ")
}

// gatedStatuses require a structurally executable plan.
var gatedStatuses = map[string]bool{"in_progress": true, "review": true, "completed": true}

// StatusGateError is the status gate refusal: create/update may not set the
// plan status to in_progress, review or completed while the
// executable-structure rules (x-shiori-structure-rules) fail on the
// resulting plan. draft, blocked and cancelled stay allowed with incomplete
// structure.
type StatusGateError struct {
	Status string
	Issues []model.Issue
}

func (e *StatusGateError) Error() string {
	return "Refusing to set workplan status to " + e.Status + " while executable-structure validation fails: " +
		model.JoinIssues(e.Issues) + ". Complete the listed fields first; draft, blocked and cancelled are allowed with incomplete structure."
}

// StructuredIssues exposes the field-path issues to the CLI and protocol.
func (e *StatusGateError) StructuredIssues() []model.Issue { return e.Issues }

// statusGate checks the resulting plan when the input sets a gated status.
func statusGate(status string, p *model.Plan) error {
	if !gatedStatuses[status] {
		return nil
	}
	id := p.ID
	strs := model.ValidateStructure(p, &id)
	if len(strs) == 0 {
		return nil
	}
	issues := make([]model.Issue, len(strs))
	for i, s := range strs {
		path, msg, _ := strings.Cut(s, ": ")
		issues[i] = model.Issue{Path: []string{path}, Message: msg}
	}
	return &StatusGateError{Status: status, Issues: issues}
}

// StepStatusGateError is the step-level status gate refusal: an update
// may not set a step to in_progress, review or completed while that step
// fails the per-step structure rules (id, title, action, validation);
// create applies the same
// rule to every step it creates. draft, blocked and cancelled stay
// allowed; completing the fields in the same call is accepted.
type StepStatusGateError struct {
	Steps  []string // "<phaseId>/<stepId> -> <status>"
	Issues []model.Issue
}

func (e *StepStatusGateError) Error() string {
	return "Refusing to set step status while the step's executable structure is incomplete: " + strings.Join(e.Steps, ", ") +
		". Missing: " + model.JoinIssues(e.Issues) +
		". Complete the listed fields first (or in the same update); draft, blocked and cancelled are allowed with incomplete structure."
}

// StructuredIssues exposes the field-path issues to the CLI and protocol.
func (e *StepStatusGateError) StructuredIssues() []model.Issue { return e.Issues }

// stepStatusGate checks every step of the resulting plan whose gated
// status this call set: a step that is new (by phase and step id) or whose
// status changed to in_progress, review or completed. Steps whose gated
// status is unchanged are not re-checked, so an existing plan stays
// editable and repairable.
func stepStatusGate(old, p *model.Plan) error {
	before := map[[2]string]string{}
	for i := range old.Phases {
		for j := range old.Phases[i].Steps {
			k := [2]string{old.Phases[i].ID, old.Phases[i].Steps[j].ID}
			if _, ok := before[k]; !ok {
				before[k] = old.Phases[i].Steps[j].Status
			}
		}
	}
	set := map[string]string{} // path prefix -> step label
	var order []string
	for i := range p.Phases {
		for j := range p.Phases[i].Steps {
			st := &p.Phases[i].Steps[j]
			if !gatedStatuses[st.Status] {
				continue
			}
			if prev, ok := before[[2]string{p.Phases[i].ID, st.ID}]; ok && prev == st.Status {
				continue
			}
			prefix := "phases." + strconv.Itoa(i) + ".steps." + strconv.Itoa(j) + "."
			set[prefix] = p.Phases[i].ID + "/" + st.ID + " -> " + st.Status
			order = append(order, prefix)
		}
	}
	if len(set) == 0 {
		return nil
	}
	id := p.ID
	var issues []model.Issue
	failed := map[string]bool{}
	for _, s := range model.ValidateStructure(p, &id) {
		path, msg, _ := strings.Cut(s, ": ")
		for prefix := range set {
			if strings.HasPrefix(path, prefix) {
				issues = append(issues, model.Issue{Path: []string{path}, Message: msg})
				failed[prefix] = true
				break
			}
		}
	}
	if len(issues) == 0 {
		return nil
	}
	var steps []string
	for _, prefix := range order {
		if failed[prefix] {
			steps = append(steps, set[prefix])
		}
	}
	return &StepStatusGateError{Steps: steps, Issues: issues}
}
