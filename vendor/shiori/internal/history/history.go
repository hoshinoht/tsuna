// Package history implements the per-plan change log (spec 06 X4),
// <id>.history.jsonl: one hash-chained line per committed write, with the
// state hashes before and after and the plan elements it changed.
//
// The log is advisory. It sits outside the plan and state hashes, a lost
// append or an out-of-band edit shows up as a gap, and nothing trusts an
// entry beyond what its hashes connect.
package history

import (
	"bytes"
	"os"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
)

// Suffix is the sidecar file suffix after the plan id.
const Suffix = ".history.jsonl"

// Version is the entry format version.
const Version = 1

// RotateAt is the size at which the log moves to archive/<id>/; a write
// prepared within RotateMargin of it names the archive.
const (
	RotateAt     = 4 << 20
	RotateMargin = 256 << 10
)

// Sources of a write, set by the trusted caller.
const (
	SourceAgent = "agent"
	SourceCLI   = "cli"
	SourceMCP   = "mcp"
)

// Hashes are a plan's hashes at one point.
type Hashes struct{ PlanHash, StateHash string }

// Change is one changed element. Path is "goal", "notes",
// "findings/<index>", "phases/<phaseId>", "phases/<phaseId>/steps/<stepId>",
// "dependencies", "checkpoint", "markdown", "evidence", "lanes", ...
type Change struct {
	Path     string
	Op       string // added | removed | changed | appended | reordered
	From, To string // status changes only
	Count    int    // appended only
}

// Entry is one log line.
type Entry struct {
	Seq     int64
	Prev    string // chain digest of the previous line ("" for the first)
	At      string
	Op      string
	Source  string
	Tx      string
	Before  *Hashes // nil: the plan did not exist
	After   *Hashes // nil: the plan no longer exists
	Changes []Change

	hash string // this line's chain digest
}

// Hash is the entry line's chain digest.
func (e Entry) Hash() string { return e.hash }

// StateChange reports a write that changed the state hash.
func (e Entry) StateChange() bool {
	return e.Before == nil || e.After == nil || e.Before.StateHash != e.After.StateHash
}

// Payload encodes an entry without seq and prev (storage adds them).
func Payload(e Entry) []byte {
	ch := make([]ojson.Value, len(e.Changes))
	for i, c := range e.Changes {
		b := ojson.NewObject(5).Set("path", ojson.StringValue(c.Path)).Set("op", ojson.StringValue(c.Op))
		if c.From != "" || c.To != "" {
			b.Set("from", ojson.StringValue(c.From)).Set("to", ojson.StringValue(c.To))
		}
		if c.Op == "appended" {
			b.Set("count", ojson.IntValue(int64(c.Count)))
		}
		ch[i] = b.Value()
	}
	return ojson.Compact(ojson.NewObject(8).
		Set("v", ojson.IntValue(Version)).
		Set("at", ojson.StringValue(e.At)).
		Set("op", ojson.StringValue(e.Op)).
		Set("source", ojson.StringValue(e.Source)).
		Set("tx", ojson.StringValue(e.Tx)).
		Set("before", hashesValue(e.Before)).
		Set("after", hashesValue(e.After)).
		Set("changes", ojson.ArrayValue(ch)).Value())
}

func hashesValue(h *Hashes) ojson.Value {
	if h == nil {
		return ojson.NullValue()
	}
	return ojson.NewObject(2).Set("planHash", ojson.StringValue(h.PlanHash)).Set("stateHash", ojson.StringValue(h.StateHash)).Value()
}

// Value renders an entry for tool and CLI output.
func (e Entry) Value() ojson.Value {
	v, _ := ojson.Parse(append([]byte(`{"seq":`+strconv.FormatInt(e.Seq, 10)+`,`), Payload(e)[1:]...))
	return v.Value
}

// Log is the readable tail of a change log.
type Log struct {
	Entries []Entry
	// Issues are lines that do not decode and chain breaks; a broken
	// chain splits the log, and only the part after the last break is
	// trusted for rebasing.
	Issues []string
	// Truncated reports that older entries were not read.
	Truncated bool
}

// MaxRead bounds how much of the log tail is read.
const MaxRead = 4 << 20

// Load reads the tail of a change log (missing file: empty log).
func Load(path string) (*Log, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Log{}, nil
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return &Log{Issues: []string{"change log is not a regular file"}}, nil
	}
	off := int64(0)
	if st.Size() > MaxRead {
		off = st.Size() - MaxRead
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, err
	}
	l := &Log{}
	if off > 0 {
		l.Truncated = true
		buf = buf[bytes.IndexByte(buf, '\n')+1:]
	}
	return parse(l, buf), nil
}

// Parse decodes log bytes (tests, archives).
func Parse(data []byte) *Log { return parse(&Log{}, data) }

func parse(l *Log, data []byte) *Log {
	var prevHash string
	first := true
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			l.Issues = append(l.Issues, "torn last line")
			break
		}
		line := data[:i]
		data = data[i+1:]
		e, ok := decode(line)
		if !ok {
			l.Issues = append(l.Issues, "undecodable line after seq "+strconv.FormatInt(lastSeq(l), 10))
			prevHash, first = "", true
			continue
		}
		if !first && e.Prev != prevHash {
			l.Issues = append(l.Issues, "chain break before seq "+strconv.FormatInt(e.Seq, 10))
			l.Entries = nil
		}
		l.Entries = append(l.Entries, e)
		prevHash, first = e.hash, false
	}
	return l
}

func lastSeq(l *Log) int64 {
	if n := len(l.Entries); n > 0 {
		return l.Entries[n-1].Seq
	}
	return 0
}

func decode(line []byte) (Entry, bool) {
	p, err := ojson.Parse(line)
	if err != nil || p.Value.Kind() != ojson.Object || len(p.Duplicates) > 0 {
		return Entry{}, false
	}
	v := p.Value
	e := Entry{hash: storage.LineHash(line)}
	str := func(k string) (string, bool) {
		x, ok := v.Get(k)
		if !ok || x.Kind() != ojson.String {
			return "", false
		}
		return x.Str(), true
	}
	ver, ok := v.Get("v")
	if !ok || ver.NumberLiteral() != strconv.Itoa(Version) {
		return Entry{}, false
	}
	seq, ok := v.Get("seq")
	if !ok {
		return Entry{}, false
	}
	if e.Seq, err = strconv.ParseInt(seq.NumberLiteral(), 10, 64); err != nil || e.Seq < 1 {
		return Entry{}, false
	}
	if pv, ok := v.Get("prev"); ok && pv.Kind() == ojson.String {
		e.Prev = pv.Str()
	}
	var okAt, okOp, okSrc, okTx bool
	e.At, okAt = str("at")
	e.Op, okOp = str("op")
	e.Source, okSrc = str("source")
	e.Tx, okTx = str("tx")
	if !okAt || !okOp || !okSrc || !okTx {
		return Entry{}, false
	}
	for _, k := range []string{"before", "after"} {
		x, ok := v.Get(k)
		if !ok {
			return Entry{}, false
		}
		if x.Kind() == ojson.Null {
			continue
		}
		ph, ok1 := x.Get("planHash")
		sh, ok2 := x.Get("stateHash")
		if !ok1 || !ok2 || ph.Kind() != ojson.String || sh.Kind() != ojson.String {
			return Entry{}, false
		}
		h := &Hashes{PlanHash: ph.Str(), StateHash: sh.Str()}
		if k == "before" {
			e.Before = h
		} else {
			e.After = h
		}
	}
	ch, ok := v.Get("changes")
	if !ok || ch.Kind() != ojson.Array {
		return Entry{}, false
	}
	for _, c := range ch.Elems() {
		var x Change
		path, ok1 := c.Get("path")
		op, ok2 := c.Get("op")
		if !ok1 || !ok2 || path.Kind() != ojson.String || op.Kind() != ojson.String {
			return Entry{}, false
		}
		x.Path, x.Op = path.Str(), op.Str()
		if f, ok := c.Get("from"); ok {
			x.From = f.Str()
		}
		if t, ok := c.Get("to"); ok {
			x.To = t.Str()
		}
		if n, ok := c.Get("count"); ok {
			i, _ := strconv.Atoi(n.NumberLiteral())
			x.Count = i
		}
		e.Changes = append(e.Changes, x)
	}
	return e, true
}

// Since returns the entries that lead from state hash from to state hash
// to, oldest first. ok=false when the log does not connect them (a gap,
// an out-of-band edit, or from older than the readable tail).
func (l *Log) Since(from, to string) ([]Entry, bool) {
	if from == to {
		return nil, true
	}
	cur := to
	var out []Entry
	for i := len(l.Entries) - 1; i >= 0; i-- {
		e := l.Entries[i]
		if !e.StateChange() {
			// Sidecar-only writes leave the state hash; keep them for
			// the record when they sit inside the range.
			if len(out) > 0 || (e.After != nil && e.After.StateHash == to) {
				out = append(out, e)
			}
			continue
		}
		if e.After == nil || e.After.StateHash != cur {
			return nil, false
		}
		out = append(out, e)
		if e.Before == nil {
			return nil, false
		}
		cur = e.Before.StateHash
		if cur == from {
			for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
				out[a], out[b] = out[b], out[a]
			}
			return out, true
		}
	}
	return nil, false
}

// Conflict is the first change in theirs that overlaps one in ours.
func Conflict(ours []Change, theirs []Entry) (Change, *Entry, bool) {
	for i := range theirs {
		for _, t := range theirs[i].Changes {
			for _, o := range ours {
				if overlaps(o, t) {
					return t, &theirs[i], true
				}
			}
		}
	}
	return Change{}, nil, false
}

// overlaps: the same element, or an element and its parent when either
// side adds, removes or reorders. Two note appends commute.
func overlaps(a, b Change) bool {
	if a.Path == b.Path {
		return !(a.Op == "appended" && b.Op == "appended")
	}
	structural := func(c Change) bool { return c.Op == "added" || c.Op == "removed" || c.Op == "reordered" }
	if strings.HasPrefix(b.Path, a.Path+"/") {
		return structural(a)
	}
	if strings.HasPrefix(a.Path, b.Path+"/") {
		return structural(b)
	}
	return false
}
