package input

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Surface selects the tool-input contract. The native surface rejects
// workspaceRoot; the standalone core/CLI surface accepts it.
type Surface int

const (
	SurfaceCore Surface = iota
	SurfaceNative
)

// InputError is an input validation failure; Error() is the reference text
// "Invalid <tool> input: path: message; ...".
type InputError struct {
	Tool   string
	Issues []model.Issue
}

func (e *InputError) Error() string {
	return "Invalid " + e.Tool + " input: " + model.JoinIssues(e.Issues)
}

var (
	idPattern = regexp.MustCompile(`[A-Za-z0-9]`)
	hashRE    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type inputParser struct {
	tool    string
	members map[string]ojson.Value
	order   []string
	known   map[string]bool
	issues  []model.Issue
}

func newInputParser(tool string, v ojson.Value, surface Surface, keys ...string) (*inputParser, *InputError) {
	p := &inputParser{tool: tool, members: map[string]ojson.Value{}, known: map[string]bool{}}
	if v.Kind() != ojson.Object {
		return nil, &InputError{Tool: tool, Issues: []model.Issue{{Message: "Invalid input: expected object, received " + v.TypeName()}}}
	}
	if surface == SurfaceCore {
		p.known["workspaceRoot"] = true
	}
	for _, k := range keys {
		p.known[k] = true
	}
	for _, m := range v.UniqueMembers() {
		p.members[m.Key] = m.Value
		p.order = append(p.order, m.Key)
	}
	return p, nil
}

func (p *inputParser) add(key, msg string) {
	var path []string
	if key != "" {
		path = []string{key}
	}
	p.issues = append(p.issues, model.Issue{Path: path, Message: msg})
}

func (p *inputParser) optString(key string) *string {
	v, ok := p.members[key]
	if !ok {
		return nil
	}
	if v.Kind() != ojson.String {
		p.add(key, "Invalid input: expected string, received "+v.TypeName())
		return nil
	}
	s := v.Str()
	return &s
}

// id applies the workplanId rules: trim, nonblank, /[a-z0-9]/i.
func (p *inputParser) id(key string, required bool) *string {
	v, ok := p.members[key]
	if !ok {
		if required {
			p.add(key, "Invalid input: expected string, received undefined")
		}
		return nil
	}
	if v.Kind() != ojson.String {
		p.add(key, "Invalid input: expected string, received "+v.TypeName())
		return nil
	}
	s := model.TrimJS(v.Str())
	bad := false
	if s == "" {
		p.add(key, "Too small: expected string to have >=1 characters")
		bad = true
	}
	if !idPattern.MatchString(s) {
		p.add(key, "Invalid string: must match pattern /[a-z0-9]/i")
		bad = true
	}
	if bad {
		return nil
	}
	return &s
}

func (p *inputParser) optInt(key string, min, max int) *int {
	v, ok := p.members[key]
	if !ok {
		return nil
	}
	if v.Kind() != ojson.Number {
		p.add(key, "Invalid input: expected number, received "+v.TypeName())
		return nil
	}
	f, _ := v.Float()
	if f != float64(int64(f)) || f > 9007199254740991 || f < -9007199254740991 {
		p.add(key, "Invalid input: expected int, received number")
		return nil
	}
	n := int(f)
	if n < min {
		p.add(key, "Too small: expected number to be >="+strconv.Itoa(min))
		return nil
	}
	if n > max {
		p.add(key, "Too big: expected number to be <="+strconv.Itoa(max))
		return nil
	}
	return &n
}

func (p *inputParser) optBool(key string) *bool {
	v, ok := p.members[key]
	if !ok {
		return nil
	}
	if v.Kind() != ojson.Bool {
		p.add(key, "Invalid input: expected boolean, received "+v.TypeName())
		return nil
	}
	b := v.Bool()
	return &b
}

func (p *inputParser) finish() *InputError {
	var unknown []string
	for _, k := range p.order {
		if !p.known[k] {
			unknown = append(unknown, strconv.Quote(k))
		}
	}
	switch len(unknown) {
	case 0:
	case 1:
		p.add("", "Unrecognized key: "+unknown[0])
	default:
		p.add("", "Unrecognized keys: "+strings.Join(unknown, ", "))
	}
	if len(p.issues) > 0 {
		return &InputError{Tool: p.tool, Issues: p.issues}
	}
	return nil
}

// Inputs for the read-only tools. Pointer fields are optional.

type ReadInput struct {
	WorkspaceRoot   *string
	ID              string
	PhaseID         *string
	StepID          *string
	IncludeMarkdown *bool
	// IncludeNotes adds findings and notes to a filtered read; an
	// unfiltered read always carries the whole document.
	IncludeNotes *bool
}

type ListInput struct{ WorkspaceRoot *string }

type InspectInput struct {
	WorkspaceRoot *string
	ID            string
	PhaseID       *string
	Limit         *int
	Cursor        *string
}

type ValidateInput struct {
	WorkspaceRoot *string
	ID            string
}

type ResumeInput struct {
	WorkspaceRoot *string
	ID            string
	MaxChars      *int
	Limit         *int
	Cursor        *string
	PhaseID       *string
	StepID        *string
}

type DoctorInput struct {
	WorkspaceRoot *string
	ID            *string
	Limit         *int
	// RuntimeFacts are host facts injected by a trusted adapter (never
	// model input); nil renders every fact as unknown.
	RuntimeFacts *ojson.Value
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ParseReadInput validates workplan_read input.
func ParseReadInput(v ojson.Value, s Surface) (ReadInput, error) {
	p, err := newInputParser("read", v, s, "id", "phaseId", "stepId", "includeMarkdown", "includeNotes")
	if err != nil {
		return ReadInput{}, err
	}
	var in ReadInput
	if s == SurfaceCore {
		in.WorkspaceRoot = p.optString("workspaceRoot")
	}
	in.ID = deref(p.id("id", true))
	in.PhaseID = p.optString("phaseId")
	in.StepID = p.optString("stepId")
	in.IncludeMarkdown = p.optBool("includeMarkdown")
	in.IncludeNotes = p.optBool("includeNotes")
	if e := p.finish(); e != nil {
		return ReadInput{}, e
	}
	return in, nil
}

// ParseListInput validates workplan_list input.
func ParseListInput(v ojson.Value, s Surface) (ListInput, error) {
	p, err := newInputParser("list", v, s)
	if err != nil {
		return ListInput{}, err
	}
	var in ListInput
	if s == SurfaceCore {
		in.WorkspaceRoot = p.optString("workspaceRoot")
	}
	if e := p.finish(); e != nil {
		return ListInput{}, e
	}
	return in, nil
}

// ParseInspectInput validates workplan_inspect input.
func ParseInspectInput(v ojson.Value, s Surface) (InspectInput, error) {
	p, err := newInputParser("inspect", v, s, "id", "phaseId", "limit", "cursor")
	if err != nil {
		return InspectInput{}, err
	}
	var in InspectInput
	if s == SurfaceCore {
		in.WorkspaceRoot = p.optString("workspaceRoot")
	}
	in.ID = deref(p.id("id", true))
	in.PhaseID = p.optString("phaseId")
	in.Limit = p.optInt("limit", 1, 500)
	in.Cursor = p.optString("cursor")
	if e := p.finish(); e != nil {
		return InspectInput{}, e
	}
	return in, nil
}

// ParseValidateInput validates workplan_validate input.
func ParseValidateInput(v ojson.Value, s Surface) (ValidateInput, error) {
	p, err := newInputParser("validate", v, s, "id")
	if err != nil {
		return ValidateInput{}, err
	}
	var in ValidateInput
	if s == SurfaceCore {
		in.WorkspaceRoot = p.optString("workspaceRoot")
	}
	in.ID = deref(p.id("id", true))
	if e := p.finish(); e != nil {
		return ValidateInput{}, e
	}
	return in, nil
}

// ParseResumeInput validates workplan_resume input.
func ParseResumeInput(v ojson.Value, s Surface) (ResumeInput, error) {
	p, err := newInputParser("resume", v, s, "id", "maxChars", "limit", "cursor", "phaseId", "stepId")
	if err != nil {
		return ResumeInput{}, err
	}
	var in ResumeInput
	if s == SurfaceCore {
		in.WorkspaceRoot = p.optString("workspaceRoot")
	}
	in.ID = deref(p.id("id", true))
	in.MaxChars = p.optInt("maxChars", 4096, 64000)
	in.Limit = p.optInt("limit", 1, 100)
	in.Cursor = p.optString("cursor")
	in.PhaseID = p.optString("phaseId")
	in.StepID = p.optString("stepId")
	if e := p.finish(); e != nil {
		return ResumeInput{}, e
	}
	return in, nil
}

// ParseDoctorInput validates workplan_doctor input.
func ParseDoctorInput(v ojson.Value, s Surface) (DoctorInput, error) {
	p, err := newInputParser("doctor", v, s, "id", "limit")
	if err != nil {
		return DoctorInput{}, err
	}
	var in DoctorInput
	if s == SurfaceCore {
		in.WorkspaceRoot = p.optString("workspaceRoot")
	}
	in.ID = p.id("id", false)
	in.Limit = p.optInt("limit", 1, 100)
	if e := p.finish(); e != nil {
		return DoctorInput{}, e
	}
	return in, nil
}
