// Package lanes implements worktree lanes (spec 06 X3, spec 04): the
// parent-owned sidecar <id>.lanes.json that records which steps and paths
// a lane owns, its lifecycle state, its baseline and its checkout. It is
// a record, never authority to run git, create or remove worktrees.
package lanes

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Suffix is the sidecar file suffix after the plan id.
const Suffix = ".lanes.json"

// SchemaVersion is the format version this package writes.
const SchemaVersion = 1

// Lane states.
const (
	Claimed     = "claimed"
	Prepared    = "prepared"
	Running     = "running"
	Review      = "review"
	Integrating = "integrating"
	Merged      = "merged"
	Abandoned   = "abandoned"
)

// States lists every state in lifecycle order.
var States = []string{Claimed, Prepared, Running, Review, Integrating, Merged, Abandoned}

var transitions = map[string][]string{
	Claimed:     {Prepared, Abandoned},
	Prepared:    {Running, Abandoned},
	Running:     {Review, Abandoned},
	Review:      {Running, Integrating, Abandoned},
	Integrating: {Merged, Review, Abandoned},
}

// CanTransition reports whether from -> to is a lifecycle step.
func CanTransition(from, to string) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Next lists the states a lane in state s may move to.
func Next(s string) []string { return transitions[s] }

// Active reports a lane that still owns its steps and claims.
func Active(state string) bool { return state != Merged && state != Abandoned }

// IDPattern is a lane id.
var IDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

var oidRE = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// Baseline is the parent working state a lane started from.
type Baseline struct {
	TreeOID *string // working tree (uncommitted included), workplan dir excluded
	Head    *string // HEAD commit
	Dirty   int     // paths whose working state differs from HEAD
}

// Checkout is where the lane's work happens (nil: the parent checkout).
type Checkout struct {
	Path   string // absolute
	Branch *string
}

// Event is one state change.
type Event struct {
	State, At, Source string
}

// Lane is one lane record.
type Lane struct {
	ID        string
	State     string
	Steps     []model.StepRef
	Claims    []string
	Baseline  Baseline
	Checkout  *Checkout
	History   []Event
	CreatedAt string
	UpdatedAt string
}

// Ledger is a decoded lanes sidecar.
type Ledger struct {
	ID        string
	UpdatedAt string
	Lanes     []Lane
}

// Find returns the lane with id.
func (l *Ledger) Find(id string) *Lane {
	for i := range l.Lanes {
		if l.Lanes[i].ID == id {
			return &l.Lanes[i]
		}
	}
	return nil
}

// OwnerOf returns the active lane that owns a step.
func (l *Ledger) OwnerOf(ref model.StepRef) *Lane {
	for i := range l.Lanes {
		if !Active(l.Lanes[i].State) {
			continue
		}
		for _, s := range l.Lanes[i].Steps {
			if s == ref {
				return &l.Lanes[i]
			}
		}
	}
	return nil
}

// Decode validates a lanes document; issues are "path: message" strings.
func Decode(v ojson.Value) (*Ledger, []string) {
	var issues []string
	add := func(p, msg string) { issues = append(issues, p+": "+msg) }
	if v.Kind() != ojson.Object {
		return nil, []string{": Invalid input: expected object, received " + v.TypeName()}
	}
	l := &Ledger{}
	sv, _ := v.Get("schemaVersion")
	if f, ok := sv.Float(); sv.Kind() != ojson.Number || !ok || f != SchemaVersion {
		add("schemaVersion", "Invalid input: expected "+strconv.Itoa(SchemaVersion))
	}
	l.ID = str(v, "id", add)
	l.UpdatedAt = datetime(v, "updatedAt", add)
	lv, _ := v.Get("lanes")
	if lv.Kind() != ojson.Array {
		add("lanes", "Invalid input: expected array, received "+lv.TypeName())
		return nil, issues
	}
	seen := map[string]bool{}
	for i, e := range lv.Elems() {
		p := "lanes." + strconv.Itoa(i)
		sub := func(k, msg string) { add(p+"."+k, msg) }
		if e.Kind() != ojson.Object {
			add(p, "Invalid input: expected object, received "+e.TypeName())
			continue
		}
		var ln Lane
		ln.ID = str(e, "laneId", sub)
		if ln.ID != "" && !IDPattern.MatchString(ln.ID) {
			sub("laneId", "Invalid lane id")
		}
		if seen[ln.ID] {
			sub("laneId", "Duplicate lane id "+ln.ID)
		}
		seen[ln.ID] = true
		ln.State = str(e, "state", sub)
		if ln.State != "" && !contains(States, ln.State) {
			sub("state", "Invalid option: expected one of "+quoteAll(States))
		}
		sv, _ := e.Get("steps")
		if sv.Kind() != ojson.Array {
			sub("steps", "Invalid input: expected array, received "+sv.TypeName())
		}
		for j, r := range sv.Elems() {
			rp := "steps." + strconv.Itoa(j)
			rs := func(k, msg string) { sub(rp+"."+k, msg) }
			ln.Steps = append(ln.Steps, model.StepRef{PhaseID: str(r, "phaseId", rs), StepID: str(r, "stepId", rs)})
		}
		cv, _ := e.Get("claims")
		if cv.Kind() != ojson.Array {
			sub("claims", "Invalid input: expected array, received "+cv.TypeName())
		}
		for j, c := range cv.Elems() {
			if c.Kind() != ojson.String || c.Str() == "" {
				sub("claims."+strconv.Itoa(j), "Invalid input: expected nonempty string")
				continue
			}
			ln.Claims = append(ln.Claims, c.Str())
		}
		bv, _ := e.Get("baseline")
		if bv.Kind() != ojson.Object {
			sub("baseline", "Invalid input: expected object, received "+bv.TypeName())
		} else {
			bs := func(k, msg string) { sub("baseline."+k, msg) }
			ln.Baseline.TreeOID = nullableOID(bv, "treeOid", bs)
			ln.Baseline.Head = nullableOID(bv, "head", bs)
			d, _ := bv.Get("dirty")
			if f, ok := d.Float(); d.Kind() != ojson.Number || !ok || f < 0 || f != float64(int(f)) {
				bs("dirty", "Invalid input: expected nonnegative int")
			} else {
				ln.Baseline.Dirty = int(f)
			}
		}
		if co, ok := e.Get("checkout"); !ok {
			sub("checkout", "Invalid input: expected object or null, received undefined")
		} else if co.Kind() == ojson.Object {
			cs := func(k, msg string) { sub("checkout."+k, msg) }
			ln.Checkout = &Checkout{Path: str(co, "path", cs)}
			if br, ok := co.Get("branch"); ok && br.Kind() == ojson.String {
				b := br.Str()
				ln.Checkout.Branch = &b
			} else if ok && br.Kind() != ojson.Null {
				cs("branch", "Invalid input: expected string or null")
			}
		} else if co.Kind() != ojson.Null {
			sub("checkout", "Invalid input: expected object or null, received "+co.TypeName())
		}
		hv, _ := e.Get("history")
		for j, h := range hv.Elems() {
			hs := func(k, msg string) { sub("history."+strconv.Itoa(j)+"."+k, msg) }
			ev := Event{State: str(h, "state", hs), At: datetime(h, "at", hs), Source: str(h, "source", hs)}
			if ev.State != "" && !contains(States, ev.State) {
				hs("state", "Invalid option: expected one of "+quoteAll(States))
			}
			ln.History = append(ln.History, ev)
		}
		ln.CreatedAt = datetime(e, "createdAt", sub)
		ln.UpdatedAt = datetime(e, "updatedAt", sub)
		l.Lanes = append(l.Lanes, ln)
	}
	if len(issues) > 0 {
		return nil, issues
	}
	return l, nil
}

func str(v ojson.Value, key string, add func(string, string)) string {
	x, ok := v.Get(key)
	if !ok || x.Kind() != ojson.String {
		add(key, "Invalid input: expected string, received "+typeName(x, ok))
		return ""
	}
	if model.Blank(x.Str()) {
		add(key, "Too small: expected string to have >=1 characters")
	}
	return x.Str()
}

func datetime(v ojson.Value, key string, add func(string, string)) string {
	s := str(v, key, add)
	if s != "" && !model.ValidDatetime(s) {
		add(key, "Invalid ISO datetime")
	}
	return s
}

func nullableOID(v ojson.Value, key string, add func(string, string)) *string {
	x, ok := v.Get(key)
	if !ok {
		add(key, "Invalid input: expected string or null, received undefined")
		return nil
	}
	if x.Kind() == ojson.Null {
		return nil
	}
	if x.Kind() != ojson.String || !oidRE.MatchString(x.Str()) {
		add(key, "Invalid input: expected a 40- or 64-hex git object id")
		return nil
	}
	s := x.Str()
	return &s
}

func typeName(v ojson.Value, ok bool) string {
	if !ok {
		return "undefined"
	}
	return v.TypeName()
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

// Value is the stored form of a lane.
func (ln *Lane) Value() ojson.Value {
	steps := make([]ojson.Value, len(ln.Steps))
	for i, r := range ln.Steps {
		steps[i] = ojson.NewObject(2).Set("phaseId", ojson.StringValue(r.PhaseID)).Set("stepId", ojson.StringValue(r.StepID)).Value()
	}
	co := ojson.NullValue()
	if ln.Checkout != nil {
		co = ojson.NewObject(2).Set("path", ojson.StringValue(ln.Checkout.Path)).Set("branch", ojson.NullableString(ln.Checkout.Branch)).Value()
	}
	hist := make([]ojson.Value, len(ln.History))
	for i, h := range ln.History {
		hist[i] = ojson.NewObject(3).Set("state", ojson.StringValue(h.State)).Set("at", ojson.StringValue(h.At)).Set("source", ojson.StringValue(h.Source)).Value()
	}
	claims := ln.Claims
	if claims == nil {
		claims = []string{}
	}
	return ojson.NewObject(10).
		Set("laneId", ojson.StringValue(ln.ID)).
		Set("state", ojson.StringValue(ln.State)).
		Set("steps", ojson.ArrayValue(steps)).
		Set("claims", ojson.StringsValue(claims)).
		Set("baseline", ojson.NewObject(3).
			Set("treeOid", ojson.NullableString(ln.Baseline.TreeOID)).
			Set("head", ojson.NullableString(ln.Baseline.Head)).
			Set("dirty", ojson.IntValue(int64(ln.Baseline.Dirty))).Value()).
		Set("checkout", co).
		Set("history", ojson.ArrayValue(hist)).
		Set("createdAt", ojson.StringValue(ln.CreatedAt)).
		Set("updatedAt", ojson.StringValue(ln.UpdatedAt)).Value()
}

// Encode renders the stored sidecar (pretty JSON, trailing newline).
func Encode(l *Ledger) []byte {
	lanes := make([]ojson.Value, len(l.Lanes))
	for i := range l.Lanes {
		lanes[i] = l.Lanes[i].Value()
	}
	v := ojson.NewObject(4).
		Set("schemaVersion", ojson.IntValue(SchemaVersion)).
		Set("id", ojson.StringValue(l.ID)).
		Set("updatedAt", ojson.StringValue(l.UpdatedAt)).
		Set("lanes", ojson.ArrayValue(lanes)).Value()
	return append(ojson.Pretty(v), '\n')
}
