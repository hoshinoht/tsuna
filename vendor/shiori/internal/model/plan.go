package model

import (
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Statuses shared by plans, phases and steps (order is significant).
var Statuses = []string{"draft", "in_progress", "blocked", "review", "completed", "cancelled"}

// Severities, most to least severe.
var Severities = []string{"blocker", "critical", "major", "minor", "note", "question"}

// FindingStatuses; absent is NOT resolved.
var FindingStatuses = []string{"open", "resolved"}

// Plan is a compatibly decoded V2 plan document. Known fields are typed;
// unknown members are kept verbatim (raw JSON, number spelling, nesting) in
// source order.
type Plan struct {
	SchemaVersion ojson.Value // raw literal, decoded value is 2
	ID            string
	Kind          string
	Title         *string
	Goal          string
	Scope         []string
	NonGoals      []string
	Constraints   []string
	RelevantFiles []string
	PlanFile      string
	SpecFiles     []string
	HasSpecFiles  bool // false for legacy documents without the key
	// SpecFilesAdded marks a legacy document whose specFiles key was set
	// by a mutation; the writer then appends it after the unknown members
	// (the reference's object-extension order).
	SpecFilesAdded bool
	Phases         []Phase
	Findings       []Finding
	Notes          []string
	Status         string
	CreatedAt      string
	UpdatedAt      string
	Unknown        []ojson.Member

	// Duplicates lists repeated member names in the stored bytes. The
	// document stays readable (JavaScript last-wins semantics); mutations
	// refuse such documents with these paths.
	Duplicates []ojson.Duplicate
}

// Phase is one ordered phase.
type Phase struct {
	ID      string
	Title   string
	Status  string
	Steps   []Step
	Unknown []ojson.Member
}

// Step is one ordered step; its identity is (phaseId, stepId).
type Step struct {
	ID         string
	Title      string
	Target     *string
	Action     *string
	Validation *string
	Status     string
	Unknown    []ojson.Member
}

// Finding is one review finding.
type Finding struct {
	Severity string
	Title    string
	Detail   *string
	Source   *string
	Status   *string
	Unknown  []ojson.Member
}

// Open reports whether the finding is unresolved (absent status is open).
func (f Finding) Open() bool { return f.Status == nil || *f.Status != "resolved" }

var (
	planKnown    = set("schemaVersion", "id", "kind", "title", "goal", "scope", "nonGoals", "constraints", "relevantFiles", "planFile", "specFiles", "phases", "reviewFindings", "notes", "status", "createdAt", "updatedAt")
	phaseKnown   = set("id", "title", "status", "steps")
	stepKnown    = set("id", "title", "target", "action", "validation", "status")
	findingKnown = set("severity", "title", "detail", "source", "status")
)

// DecodePlan performs the compatible decode of a parsed plan document. On
// failure the error is a *DecodeError whose text is the reference message.
func DecodePlan(parsed ojson.Parsed) (*Plan, error) {
	var c checker
	root := parsed.Value
	ms, ok := c.object(fp{}, root)
	if !ok {
		return nil, &DecodeError{Issues: c.issues}
	}
	f := fieldMap(ms)
	p := &Plan{Duplicates: parsed.Duplicates}

	v, _ := f.get("schemaVersion")
	if c.literalNumber(at(nil, "schemaVersion"), v, 2) {
		p.SchemaVersion = v
	}
	v, _ = f.get("id")
	p.ID, _ = c.str(at(nil, "id"), v)
	v, _ = f.get("kind")
	p.Kind, _ = c.str(at(nil, "kind"), v)
	v, _ = f.get("title")
	p.Title, _ = c.nullableStr(at(nil, "title"), v)
	v, _ = f.get("goal")
	p.Goal, _ = c.str(at(nil, "goal"), v)
	v, _ = f.get("scope")
	p.Scope, _ = c.strList(at(nil, "scope"), v)
	v, _ = f.get("nonGoals")
	p.NonGoals, _ = c.strList(at(nil, "nonGoals"), v)
	v, _ = f.get("constraints")
	p.Constraints, _ = c.strList(at(nil, "constraints"), v)
	v, _ = f.get("relevantFiles")
	p.RelevantFiles, _ = c.strList(at(nil, "relevantFiles"), v)
	v, _ = f.get("planFile")
	p.PlanFile, _ = c.str(at(nil, "planFile"), v)
	if v, present := f.get("specFiles"); present {
		p.HasSpecFiles = true
		p.SpecFiles, _ = c.strList(at(nil, "specFiles"), v)
	}
	v, _ = f.get("phases")
	if elems, ok := c.array(at(nil, "phases"), v); ok {
		for i, e := range elems {
			p.Phases = append(p.Phases, decodePhase(&c, idx(child(nil, "phases"), i), e))
		}
	}
	v, _ = f.get("reviewFindings")
	if elems, ok := c.array(at(nil, "reviewFindings"), v); ok {
		for i, e := range elems {
			p.Findings = append(p.Findings, decodeFinding(&c, idx(child(nil, "reviewFindings"), i), e))
		}
	}
	v, _ = f.get("notes")
	p.Notes, _ = c.strList(at(nil, "notes"), v)
	v, _ = f.get("status")
	p.Status, _ = c.enum(at(nil, "status"), v, Statuses)
	v, _ = f.get("createdAt")
	p.CreatedAt, _ = c.str(at(nil, "createdAt"), v)
	v, _ = f.get("updatedAt")
	p.UpdatedAt, _ = c.str(at(nil, "updatedAt"), v)
	p.Unknown = unknownMembers(ms, planKnown)

	if len(c.issues) > 0 {
		return nil, &DecodeError{Issues: c.issues}
	}
	if p.SpecFiles == nil {
		p.SpecFiles = []string{}
	}
	return p, nil
}

func decodePhase(c *checker, path []string, v ojson.Value) Phase {
	var ph Phase
	ms, ok := c.object(fp{base: path}, v)
	if !ok {
		return ph
	}
	f := fieldMap(ms)
	x, _ := f.get("id")
	ph.ID, _ = c.str(at(path, "id"), x)
	x, _ = f.get("title")
	ph.Title, _ = c.str(at(path, "title"), x)
	x, _ = f.get("status")
	ph.Status, _ = c.enum(at(path, "status"), x, Statuses)
	x, _ = f.get("steps")
	if elems, ok := c.array(at(path, "steps"), x); ok {
		for i, e := range elems {
			ph.Steps = append(ph.Steps, decodeStep(c, idx(child(path, "steps"), i), e))
		}
	}
	ph.Unknown = unknownMembers(ms, phaseKnown)
	return ph
}

func decodeStep(c *checker, path []string, v ojson.Value) Step {
	var st Step
	ms, ok := c.object(fp{base: path}, v)
	if !ok {
		return st
	}
	f := fieldMap(ms)
	x, _ := f.get("id")
	st.ID, _ = c.str(at(path, "id"), x)
	x, _ = f.get("title")
	st.Title, _ = c.str(at(path, "title"), x)
	slots := new([3]string)
	x, present := f.get("target")
	st.Target = c.optStrInto(at(path, "target"), x, present, &slots[0])
	x, present = f.get("action")
	st.Action = c.optStrInto(at(path, "action"), x, present, &slots[1])
	x, present = f.get("validation")
	st.Validation = c.optStrInto(at(path, "validation"), x, present, &slots[2])
	x, _ = f.get("status")
	st.Status, _ = c.enum(at(path, "status"), x, Statuses)
	st.Unknown = unknownMembers(ms, stepKnown)
	return st
}

func decodeFinding(c *checker, path []string, v ojson.Value) Finding {
	var fd Finding
	ms, ok := c.object(fp{base: path}, v)
	if !ok {
		return fd
	}
	f := fieldMap(ms)
	x, _ := f.get("severity")
	fd.Severity, _ = c.enum(at(path, "severity"), x, Severities)
	x, _ = f.get("title")
	fd.Title, _ = c.str(at(path, "title"), x)
	slots := new([3]string)
	x, present := f.get("detail")
	fd.Detail = c.optStrInto(at(path, "detail"), x, present, &slots[0])
	x, present = f.get("source")
	fd.Source = c.optStrInto(at(path, "source"), x, present, &slots[1])
	if x, present := f.get("status"); present {
		s, ok := c.enum(at(path, "status"), x, FindingStatuses)
		if ok {
			slots[2] = s
			fd.Status = &slots[2]
		}
	}
	fd.Unknown = unknownMembers(ms, findingKnown)
	return fd
}

// StoredValue renders the document as a writer persists it: like ToValue,
// except that a legacy document without specFiles keeps it absent unless a
// mutation set it (then it follows the unknown members).
func (p *Plan) StoredValue() ojson.Value {
	v := p.ToValue()
	if p.HasSpecFiles || p.SpecFilesAdded {
		return v
	}
	ms := v.Members()
	return ojson.ObjectValue(ms[:len(ms)-1])
}

// EncodeStored is JSON.stringify(doc, null, 2) + "\n" of StoredValue.
func (p *Plan) EncodeStored() []byte { return append(ojson.Pretty(p.StoredValue()), '\n') }

// Clone returns a deep copy of the known fields (unknown member values
// are immutable and shared).
func (p *Plan) Clone() *Plan {
	c := *p
	c.Scope = append([]string(nil), p.Scope...)
	c.NonGoals = append([]string(nil), p.NonGoals...)
	c.Constraints = append([]string(nil), p.Constraints...)
	c.RelevantFiles = append([]string(nil), p.RelevantFiles...)
	c.SpecFiles = append([]string{}, p.SpecFiles...)
	c.Notes = append([]string(nil), p.Notes...)
	c.Findings = append([]Finding(nil), p.Findings...)
	c.Phases = make([]Phase, len(p.Phases))
	for i := range p.Phases {
		c.Phases[i] = p.Phases[i]
		c.Phases[i].Steps = append([]Step(nil), p.Phases[i].Steps...)
	}
	if p.Title != nil {
		t := *p.Title
		c.Title = &t
	}
	return &c
}

// ToValue renders the normalized document in the reference key order:
// known members in schema order (absent optional members stay absent),
// then unknown members in source order, then "specFiles": [] when the
// stored document had no specFiles key (the reference's in-memory
// normalization, never persisted by a read).
func (p *Plan) ToValue() ojson.Value {
	b := ojson.NewObject(20 + len(p.Unknown))
	b.Set("schemaVersion", p.SchemaVersion)
	b.Set("id", ojson.StringValue(p.ID))
	b.Set("kind", ojson.StringValue(p.Kind))
	b.Set("title", ojson.NullableString(p.Title))
	b.Set("goal", ojson.StringValue(p.Goal))
	b.Set("scope", ojson.StringsValue(p.Scope))
	b.Set("nonGoals", ojson.StringsValue(p.NonGoals))
	b.Set("constraints", ojson.StringsValue(p.Constraints))
	b.Set("relevantFiles", ojson.StringsValue(p.RelevantFiles))
	b.Set("planFile", ojson.StringValue(p.PlanFile))
	if p.HasSpecFiles {
		b.Set("specFiles", ojson.StringsValue(p.SpecFiles))
	}
	phases := make([]ojson.Value, len(p.Phases))
	for i := range p.Phases {
		phases[i] = p.Phases[i].ToValue()
	}
	b.Set("phases", ojson.ArrayValue(phases))
	findings := make([]ojson.Value, len(p.Findings))
	for i := range p.Findings {
		findings[i] = p.Findings[i].ToValue()
	}
	b.Set("reviewFindings", ojson.ArrayValue(findings))
	b.Set("notes", ojson.StringsValue(p.Notes))
	b.Set("status", ojson.StringValue(p.Status))
	b.Set("createdAt", ojson.StringValue(p.CreatedAt))
	b.Set("updatedAt", ojson.StringValue(p.UpdatedAt))
	for _, m := range p.Unknown {
		b.Set(m.Key, m.Value)
	}
	if !p.HasSpecFiles {
		b.Set("specFiles", ojson.StringsValue(p.SpecFiles))
	}
	return b.Value()
}

// ToValue renders the phase with known members first.
func (ph *Phase) ToValue() ojson.Value {
	b := ojson.NewObject(4 + len(ph.Unknown))
	b.Set("id", ojson.StringValue(ph.ID))
	b.Set("title", ojson.StringValue(ph.Title))
	b.Set("status", ojson.StringValue(ph.Status))
	steps := make([]ojson.Value, len(ph.Steps))
	for i := range ph.Steps {
		steps[i] = ph.Steps[i].ToValue()
	}
	b.Set("steps", ojson.ArrayValue(steps))
	for _, m := range ph.Unknown {
		b.Set(m.Key, m.Value)
	}
	return b.Value()
}

// ToValue renders the step with known members first.
func (st *Step) ToValue() ojson.Value {
	b := ojson.NewObject(6 + len(st.Unknown))
	b.Set("id", ojson.StringValue(st.ID))
	b.Set("title", ojson.StringValue(st.Title))
	if st.Target != nil {
		b.Set("target", ojson.StringValue(*st.Target))
	}
	if st.Action != nil {
		b.Set("action", ojson.StringValue(*st.Action))
	}
	if st.Validation != nil {
		b.Set("validation", ojson.StringValue(*st.Validation))
	}
	b.Set("status", ojson.StringValue(st.Status))
	for _, m := range st.Unknown {
		b.Set(m.Key, m.Value)
	}
	return b.Value()
}

// ToValue renders the finding with known members first.
func (fd *Finding) ToValue() ojson.Value {
	b := ojson.NewObject(5 + len(fd.Unknown))
	b.Set("severity", ojson.StringValue(fd.Severity))
	b.Set("title", ojson.StringValue(fd.Title))
	if fd.Detail != nil {
		b.Set("detail", ojson.StringValue(*fd.Detail))
	}
	if fd.Source != nil {
		b.Set("source", ojson.StringValue(*fd.Source))
	}
	if fd.Status != nil {
		b.Set("status", ojson.StringValue(*fd.Status))
	}
	for _, m := range fd.Unknown {
		b.Set(m.Key, m.Value)
	}
	return b.Value()
}

// StepCount is the total number of steps.
func (p *Plan) StepCount() int {
	n := 0
	for i := range p.Phases {
		n += len(p.Phases[i].Steps)
	}
	return n
}

// OpenFindingCount counts unresolved findings.
func (p *Plan) OpenFindingCount() int {
	n := 0
	for i := range p.Findings {
		if p.Findings[i].Open() {
			n++
		}
	}
	return n
}

// Summary is the compact "workplan" object used by list/inspect/validate/doctor.
func (p *Plan) Summary() ojson.Value {
	return ojson.NewObject(11).
		Set("id", ojson.StringValue(p.ID)).
		Set("kind", ojson.StringValue(p.Kind)).
		Set("title", ojson.NullableString(p.Title)).
		Set("goal", ojson.StringValue(p.Goal)).
		Set("status", ojson.StringValue(p.Status)).
		Set("planFile", ojson.StringValue(p.PlanFile)).
		Set("phaseCount", ojson.IntValue(int64(len(p.Phases)))).
		Set("stepCount", ojson.IntValue(int64(p.StepCount()))).
		Set("specFileCount", ojson.IntValue(int64(len(p.SpecFiles)))).
		Set("openFindingCount", ojson.IntValue(int64(p.OpenFindingCount()))).
		Set("updatedAt", ojson.StringValue(p.UpdatedAt)).
		Value()
}

// SummaryMembers returns Summary's members for embedding in a larger object.
func (p *Plan) SummaryMembers() []ojson.Member { return p.Summary().Members() }
