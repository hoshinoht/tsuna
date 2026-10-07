package evidence

import (
	"github.com/hoshinoht/shiori/internal/gitview"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Step evidence states, worst first.
const (
	StateFailing = "failing" // the latest record of some command failed
	StateStale   = "stale"   // passing, but the code it ran against changed
	StateUnknown = "unknown" // passing, but the tree cannot be compared
	StateFresh   = "fresh"   // every command's latest record passed on the current tree
	StateNone    = "none"    // no record for the step
)

var rank = map[string]int{StateFailing: 0, StateStale: 1, StateUnknown: 2, StateFresh: 3, StateNone: 4}

// Worse reports whether state a is worse than b.
func Worse(a, b string) bool { return rank[a] < rank[b] }

// Current is the tree records are compared against, or why there is none.
type Current struct {
	Tree  *gitview.Tree
	Err   error
	Lanes map[string]*gitview.Tree // active lane checkouts
}

// RecordState classifies one record against the current tree.
func (c Current) RecordState(r Record) string {
	if !r.Passed() {
		return StateFailing
	}
	// While its lane is active a record is compared with the lane's
	// checkout; afterwards with the project tree, so lane results go
	// stale until they are repeated on the combined state.
	tree := c.Tree
	if r.Lane != nil && c.Lanes[*r.Lane] != nil {
		tree = c.Lanes[*r.Lane]
	}
	if r.TreeOID == nil || tree == nil {
		return StateUnknown
	}
	if len(r.Scope) == 0 {
		if *r.TreeOID == tree.OID {
			return StateFresh
		}
		return StateStale
	}
	for _, s := range r.Scope {
		cur, ok := tree.Scope[s.Path]
		if !ok {
			return StateUnknown
		}
		if (cur == nil) != (s.Digest == nil) || (cur != nil && *cur != *s.Digest) {
			return StateStale
		}
	}
	return StateFresh
}

// Latest is a step's newest record of one command.
type Latest struct {
	Record Record
	State  string
	Count  int // records of this command for the step
}

// StepView is a step's latest record per command; State is the worst.
type StepView struct {
	Ref      model.StepRef
	State    string
	Commands []Latest
}

// Views groups the ledger by step.
func (l *Ledger) Views(c Current) map[model.StepRef]*StepView {
	out := map[model.StepRef]*StepView{}
	for _, r := range l.Records {
		v := out[r.Ref()]
		if v == nil {
			v = &StepView{Ref: r.Ref(), State: StateFresh}
			out[r.Ref()] = v
		}
		found := false
		for i := range v.Commands {
			if v.Commands[i].Record.Command == r.Command {
				v.Commands[i].Record = r
				v.Commands[i].Count++
				found = true
			}
		}
		if !found {
			v.Commands = append(v.Commands, Latest{Record: r, Count: 1})
		}
	}
	for _, v := range out {
		for i := range v.Commands {
			v.Commands[i].State = c.RecordState(v.Commands[i].Record)
			if Worse(v.Commands[i].State, v.State) {
				v.State = v.Commands[i].State
			}
		}
	}
	return out
}

// ScopePaths is the union of all records' scope paths.
func (l *Ledger) ScopePaths() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range l.Records {
		for _, s := range r.Scope {
			if !seen[s.Path] {
				seen[s.Path] = true
				out = append(out, s.Path)
			}
		}
	}
	return out
}

// Value renders a step view for inspect and doctor.
func (v *StepView) Value() ojson.Value {
	cmds := make([]ojson.Value, len(v.Commands))
	for i, l := range v.Commands {
		b := ojson.NewObject(7).
			Set("command", ojson.StringValue(l.Record.Command)).
			Set("exitCode", ojson.IntValue(l.Record.ExitCode)).
			Set("state", ojson.StringValue(l.State)).
			Set("source", ojson.StringValue(l.Record.Source)).
			Set("recordedAt", ojson.StringValue(l.Record.RecordedAt)).
			Set("treeOid", ojson.NullableString(l.Record.TreeOID))
		if l.Record.Lane != nil {
			b.Set("lane", ojson.StringValue(*l.Record.Lane))
		}
		if l.Count > 1 {
			b.Set("records", ojson.IntValue(int64(l.Count)))
		}
		cmds[i] = b.Value()
	}
	return ojson.NewObject(2).
		Set("state", ojson.StringValue(v.State)).
		Set("commands", ojson.ArrayValue(cmds)).Value()
}

// TreeValue describes the current tree (or why there is none).
func (c Current) TreeValue() ojson.Value {
	b := ojson.NewObject(2)
	if c.Tree != nil {
		b.Set("oid", ojson.StringValue(c.Tree.OID))
	} else {
		b.Set("oid", ojson.NullValue())
		if c.Err != nil {
			b.Set("error", ojson.StringValue(c.Err.Error()))
		}
	}
	return b.Value()
}
