// Package evidence implements the evidence ledger (spec 06 X2),
// <id>.evidence.json: per step, the commands run, their exit codes and
// output digests, and the git tree they ran against.
package evidence

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Suffix is the sidecar file suffix after the plan id.
const Suffix = ".evidence.json"

// SchemaVersion is the ledger format version this package writes.
const SchemaVersion = 1

// Record sources, set by the trusted caller, never by model input.
const (
	SourceAgent  = "agent"   // native tool call: the exit code is asserted by the agent
	SourceCLI    = "cli"     // operator CLI: asserted by the operator
	SourceCLIRun = "cli-run" // operator CLI ran the command itself
)

var sources = []string{SourceAgent, SourceCLI, SourceCLIRun}

// Retention: newest records kept per (step, command) and overall.
const (
	KeepPerCommand = 5
	KeepTotal      = 2000
)

var (
	hashRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
	oidRE  = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
)

// ScopeEntry is a scope path and the digest of its index entries in the
// recorded tree (nil: absent).
type ScopeEntry struct {
	Path   string
	Digest *string
}

// Record is one evidence record.
type Record struct {
	PhaseID, StepID string
	Command         string
	ExitCode        int64
	OutputDigest    *string // sha256 of the output, when supplied
	Summary         *string
	TreeOID         *string // nil when the root is not in a git work tree
	Scope           []ScopeEntry
	Lane            *string // the lane whose checkout it ran in (nil: the project root)
	Source          string
	RecordedAt      string
}

// Passed reports a zero exit code.
func (r Record) Passed() bool { return r.ExitCode == 0 }

// Ref is the record's step.
func (r Record) Ref() model.StepRef { return model.StepRef{PhaseID: r.PhaseID, StepID: r.StepID} }

// Ledger is a decoded evidence sidecar.
type Ledger struct {
	ID        string
	UpdatedAt string
	Records   []Record
}

// Decode validates a ledger; issues are "path: message" strings.
func Decode(v ojson.Value) (*Ledger, []string) {
	var issues []string
	add := func(path, msg string) { issues = append(issues, path+": "+msg) }
	if v.Kind() != ojson.Object {
		return nil, []string{": Invalid input: expected object, received " + v.TypeName()}
	}
	l := &Ledger{}
	sv, _ := v.Get("schemaVersion")
	if f, ok := sv.Float(); sv.Kind() != ojson.Number || !ok || f != SchemaVersion {
		add("schemaVersion", "Invalid input: expected "+strconv.Itoa(SchemaVersion))
	}
	l.ID = str(v, "id", true, add)
	l.UpdatedAt = str(v, "updatedAt", true, add)
	if l.UpdatedAt != "" && !model.ValidDatetime(l.UpdatedAt) {
		add("updatedAt", "Invalid ISO datetime")
	}
	rv, ok := v.Get("records")
	if !ok || rv.Kind() != ojson.Array {
		add("records", "Invalid input: expected array, received "+rv.TypeName())
		return nil, issues
	}
	for i, e := range rv.Elems() {
		p := "records." + strconv.Itoa(i)
		if e.Kind() != ojson.Object {
			add(p, "Invalid input: expected object, received "+e.TypeName())
			continue
		}
		sub := func(path, msg string) { add(p+"."+path, msg) }
		var r Record
		r.PhaseID = str(e, "phaseId", true, sub)
		r.StepID = str(e, "stepId", true, sub)
		r.Command = str(e, "command", true, sub)
		ec, ok := e.Get("exitCode")
		if f, fok := ec.Float(); !ok || ec.Kind() != ojson.Number || !fok || f != float64(int64(f)) {
			sub("exitCode", "Invalid input: expected int")
		} else {
			r.ExitCode = int64(f)
		}
		r.OutputDigest = nullableMatch(e, "outputDigest", hashRE, "/^[a-f0-9]{64}$/", sub)
		if sv, ok := e.Get("summary"); ok {
			if sv.Kind() != ojson.String {
				sub("summary", "Invalid input: expected string, received "+sv.TypeName())
			} else {
				s := sv.Str()
				r.Summary = &s
			}
		}
		r.TreeOID = nullableMatch(e, "treeOid", oidRE, "a 40- or 64-hex git object id", sub)
		if sc, ok := e.Get("scope"); ok {
			if sc.Kind() != ojson.Array {
				sub("scope", "Invalid input: expected array, received "+sc.TypeName())
			}
			for j, se := range sc.Elems() {
				sp := "scope." + strconv.Itoa(j)
				if se.Kind() != ojson.Object {
					sub(sp, "Invalid input: expected object, received "+se.TypeName())
					continue
				}
				ssub := func(path, msg string) { sub(sp+"."+path, msg) }
				entry := ScopeEntry{Path: str(se, "path", true, ssub)}
				entry.Digest = nullableMatch(se, "digest", hashRE, "/^[a-f0-9]{64}$/", ssub)
				r.Scope = append(r.Scope, entry)
			}
		}
		if lv, ok := e.Get("lane"); ok {
			if lv.Kind() != ojson.String || lv.Str() == "" {
				sub("lane", "Invalid input: expected nonempty string")
			} else {
				ln := lv.Str()
				r.Lane = &ln
			}
		}
		r.Source = str(e, "source", true, sub)
		if r.Source != "" && !contains(sources, r.Source) {
			sub("source", "Invalid option: expected one of "+quoteAll(sources))
		}
		r.RecordedAt = str(e, "recordedAt", true, sub)
		if r.RecordedAt != "" && !model.ValidDatetime(r.RecordedAt) {
			sub("recordedAt", "Invalid ISO datetime")
		}
		l.Records = append(l.Records, r)
	}
	if len(issues) > 0 {
		return nil, issues
	}
	return l, nil
}

func str(v ojson.Value, key string, required bool, add func(string, string)) string {
	x, ok := v.Get(key)
	if !ok {
		if required {
			add(key, "Invalid input: expected string, received undefined")
		}
		return ""
	}
	if x.Kind() != ojson.String {
		add(key, "Invalid input: expected string, received "+x.TypeName())
		return ""
	}
	if required && model.Blank(x.Str()) {
		add(key, "Too small: expected string to have >=1 characters")
	}
	return x.Str()
}

func nullableMatch(v ojson.Value, key string, re *regexp.Regexp, want string, add func(string, string)) *string {
	x, ok := v.Get(key)
	if !ok {
		add(key, "Invalid input: expected string or null, received undefined")
		return nil
	}
	if x.Kind() == ojson.Null {
		return nil
	}
	if x.Kind() != ojson.String || !re.MatchString(x.Str()) {
		add(key, "Invalid input: expected "+want)
		return nil
	}
	s := x.Str()
	return &s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func quoteAll(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, "|")
}

// Value is the stored form of a record.
func (r Record) Value() ojson.Value {
	b := ojson.NewObject(10).
		Set("phaseId", ojson.StringValue(r.PhaseID)).
		Set("stepId", ojson.StringValue(r.StepID)).
		Set("command", ojson.StringValue(r.Command)).
		Set("exitCode", ojson.IntValue(r.ExitCode)).
		Set("outputDigest", ojson.NullableString(r.OutputDigest))
	if r.Summary != nil {
		b.Set("summary", ojson.StringValue(*r.Summary))
	}
	b.Set("treeOid", ojson.NullableString(r.TreeOID))
	if len(r.Scope) > 0 {
		sc := make([]ojson.Value, len(r.Scope))
		for i, s := range r.Scope {
			sc[i] = ojson.NewObject(2).
				Set("path", ojson.StringValue(s.Path)).
				Set("digest", ojson.NullableString(s.Digest)).Value()
		}
		b.Set("scope", ojson.ArrayValue(sc))
	}
	if r.Lane != nil {
		b.Set("lane", ojson.StringValue(*r.Lane))
	}
	return b.
		Set("source", ojson.StringValue(r.Source)).
		Set("recordedAt", ojson.StringValue(r.RecordedAt)).Value()
}

// Encode renders the stored sidecar (pretty JSON, trailing newline).
func Encode(l *Ledger) []byte {
	recs := make([]ojson.Value, len(l.Records))
	for i, r := range l.Records {
		recs[i] = r.Value()
	}
	v := ojson.NewObject(4).
		Set("schemaVersion", ojson.IntValue(SchemaVersion)).
		Set("id", ojson.StringValue(l.ID)).
		Set("updatedAt", ojson.StringValue(l.UpdatedAt)).
		Set("records", ojson.ArrayValue(recs)).Value()
	return append(ojson.Pretty(v), '\n')
}

// Append adds records (oldest first) and drops the oldest beyond retention.
func (l *Ledger) Append(add ...Record) {
	all := append(append([]Record{}, l.Records...), add...)
	type key struct{ p, s, c string }
	seen := map[key]int{}
	keep := make([]bool, len(all))
	kept := 0
	for i := len(all) - 1; i >= 0; i-- {
		k := key{all[i].PhaseID, all[i].StepID, all[i].Command}
		if seen[k] >= KeepPerCommand || kept >= KeepTotal {
			continue
		}
		seen[k]++
		keep[i] = true
		kept++
	}
	out := make([]Record, 0, kept)
	for i, r := range all {
		if keep[i] {
			out = append(out, r)
		}
	}
	l.Records = out
}
