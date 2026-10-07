package storage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Target is one artifact the mutation replaces, creates or deletes.
type Target struct {
	Rel          string // project-relative path with '/' separators
	Kind         string // plan | markdown | checkpoint | dependencies | archive
	Before       []byte
	BeforeExists bool
	After        []byte
	AfterExists  bool // false = delete
	Mode         fs.FileMode
	Stage        string // same-directory staging path (empty for deletions)
	Backup       string // journal v2: hard link to the before image (empty when absent)

	sealed          bool // the digests below are computed
	beforeH, afterH string
}

// Seal computes the digests once; a sealed target's images must not change.
func (t *Target) Seal() {
	t.beforeH, t.afterH = digestOrEmpty(t.Before, t.BeforeExists), digestOrEmpty(t.After, t.AfterExists)
	t.sealed = true
}

// SealKnown is Seal with the before digest already known (the caller
// hashed exactly these before bytes).
func (t *Target) SealKnown(before string) {
	t.beforeH, t.afterH = before, digestOrEmpty(t.After, t.AfterExists)
	t.sealed = true
}

// BeforeHash / AfterHash are the lowercase SHA-256 digests, "" when absent.
func (t Target) BeforeHash() string {
	if t.sealed {
		return t.beforeH
	}
	return digestOrEmpty(t.Before, t.BeforeExists)
}

func (t Target) AfterHash() string {
	if t.sealed {
		return t.afterH
	}
	return digestOrEmpty(t.After, t.AfterExists)
}

func digestOrEmpty(b []byte, ok bool) string {
	if !ok {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ReadEntry is one precondition: a path and the exact state preparation
// observed (sha256, or missing).
type ReadEntry struct {
	Rel     string
	SHA256  string
	Missing bool
}

// Intent is a prepared mutation: every resource it may read, write,
// delete, lock, stage or archive, with before/after digests. Preparing an
// intent creates nothing; Commit executes exactly this intent.
type Intent struct {
	Operation     string // journal operation label (reference values)
	WorkplanID    string
	Root          string // canonical project root the intent is bound to
	TransactionID string
	CreatedAt     string
	Targets       []Target
	Reads         []ReadEntry
	JournalRel    string
	JournalStage  string
	Locks         []LockRef // acquisition order: workspace, then plan
	Dirs          []string  // directories created if missing (parents first)
	// Recovery is "resume" or "rollback" for explicit recovery of the
	// pending journal at JournalRel; no new journal is written and the
	// existing one is removed last.
	Recovery string
	// JournalBytes is the exact pending journal a recovery is bound to.
	JournalBytes []byte
	// JournalVersion is 1 (inline base64 images) or 2 (images by
	// reference to the staged files and before-image hard links).
	JournalVersion int
	// History is the change-log entry appended after the commit.
	History *Append
}

// Resources is the exact-resource view of an intent (absolute paths), in
// the form an authorization prompt or protocol frame presents it.
type Resources struct {
	Read    []string
	Write   []string
	Delete  []string
	Archive []string
	Journal string
	Lock    []string
	Staging []string
	Dirs    []string
}

// Resources lists every path the intent can touch.
func (in *Intent) Resources() Resources {
	var r Resources
	a := func(rel string) string { return abs(in.Root, rel) }
	for _, e := range in.Reads {
		r.Read = append(r.Read, a(e.Rel))
	}
	for _, t := range in.Targets {
		switch {
		case !t.AfterExists:
			r.Delete = append(r.Delete, a(t.Rel))
		case t.Kind == "archive":
			r.Archive = append(r.Archive, a(t.Rel))
		default:
			r.Write = append(r.Write, a(t.Rel))
		}
		if t.Stage != "" {
			r.Staging = append(r.Staging, a(t.Stage))
		}
		if t.Backup != "" {
			r.Staging = append(r.Staging, a(t.Backup))
		}
	}
	if in.History != nil {
		r.Write = append(r.Write, a(in.History.Rel))
		if in.History.ArchiveRel != "" {
			r.Archive = append(r.Archive, a(in.History.ArchiveRel))
		}
	}
	r.Journal = a(in.JournalRel)
	if in.JournalStage != "" {
		r.Staging = append(r.Staging, a(in.JournalStage))
	}
	for _, l := range in.Locks {
		for _, p := range l.Paths() {
			r.Lock = append(r.Lock, a(p))
		}
	}
	for _, d := range in.Dirs {
		r.Dirs = append(r.Dirs, a(d))
	}
	return r
}

// Value renders the intent (content digests, never content) for display
// and for the intent digest.
func (in *Intent) Value() ojson.Value {
	res := in.Resources()
	targets := make([]ojson.Value, len(in.Targets))
	for i, t := range in.Targets {
		targets[i] = ojson.NewObject(5).
			Set("path", ojson.StringValue(t.Rel)).
			Set("kind", ojson.StringValue(t.Kind)).
			Set("beforeHash", nullable(t.BeforeHash())).
			Set("afterHash", nullable(t.AfterHash())).
			Set("mode", ojson.IntValue(int64(t.Mode.Perm()))).Value()
	}
	reads := make([]ojson.Value, len(in.Reads))
	for i, e := range in.Reads {
		b := ojson.NewObject(2).Set("path", ojson.StringValue(e.Rel))
		if e.Missing {
			b.Set("missing", ojson.BoolValue(true))
		} else {
			b.Set("sha256", ojson.StringValue(e.SHA256))
		}
		reads[i] = b.Value()
	}
	b := ojson.NewObject(12).
		Set("operation", ojson.StringValue(in.Operation)).
		Set("workplanId", ojson.StringValue(in.WorkplanID)).
		Set("root", ojson.StringValue(in.Root)).
		Set("transactionId", ojson.StringValue(in.TransactionID))
	if in.JournalVersion == 2 {
		b.Set("journalVersion", ojson.IntValue(2))
	}
	if in.History != nil {
		b.Set("history", ojson.NewObject(2).
			Set("path", ojson.StringValue(in.History.Rel)).
			Set("payloadSha256", ojson.StringValue(digestOrEmpty(in.History.Payload, true))).Value())
	}
	if in.Recovery != "" {
		b.Set("recovery", ojson.StringValue(in.Recovery)).
			Set("journalSha256", ojson.StringValue(digestOrEmpty(in.JournalBytes, true)))
	}
	return b.
		Set("targets", ojson.ArrayValue(targets)).
		Set("reads", ojson.ArrayValue(reads)).
		Set("writePaths", ojson.StringsValue(orEmpty(res.Write))).
		Set("deletePaths", ojson.StringsValue(orEmpty(res.Delete))).
		Set("archivePaths", ojson.StringsValue(orEmpty(res.Archive))).
		Set("journalPath", ojson.StringValue(res.Journal)).
		Set("lockPaths", ojson.StringsValue(orEmpty(res.Lock))).
		Set("stagingPaths", ojson.StringsValue(orEmpty(res.Staging))).
		Set("directories", ojson.StringsValue(orEmpty(res.Dirs))).Value()
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nullable(s string) ojson.Value {
	if s == "" {
		return ojson.NullValue()
	}
	return ojson.StringValue(s)
}

// Digest binds an approval to this exact intent (resources, digests,
// operation, root). Content bytes are bound through their digests.
func (in *Intent) Digest() string {
	h := sha256.New()
	h.Write([]byte("shiori-intent-v1\n"))
	h.Write(ojson.Compact(in.Value()))
	h.Write([]byte{'\n'})
	return hex.EncodeToString(h.Sum(nil))
}

// StagePath is the same-directory staging name for a target:
// ".<base>.<tx>.<index>.stage".
func StagePath(rel, tx string, index int) string {
	dir, base := path.Split(rel)
	return dir + "." + base + "." + tx + "." + itoa(index) + ".stage"
}

// BackupPath is the journal v2 before-image link for a target:
// ".<base>.<tx>.<index>.before".
func BackupPath(rel, tx string, index int) string {
	dir, base := path.Split(rel)
	return dir + "." + base + "." + tx + "." + itoa(index) + ".before"
}

// JournalStagePath is ".<id>.transaction.<tx>.stage" in the workplan dir.
func JournalStagePath(dir, id, tx string) string {
	return dir + "/." + id + ".transaction." + tx + ".stage"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ParentDirs lists the directories (parents first) that must exist for
// the given project-relative files, excluding the root.
func ParentDirs(rels ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rels {
		d := path.Dir(r)
		var chain []string
		for d != "." && d != "/" && d != "" {
			chain = append(chain, d)
			d = path.Dir(d)
		}
		for i := len(chain) - 1; i >= 0; i-- {
			if !seen[chain[i]] {
				seen[chain[i]] = true
				out = append(out, chain[i])
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.Count(out[i], "/") < strings.Count(out[j], "/") })
	return out
}

// EncodeJournal renders the intent's journal: v1 exactly as the reference
// writes it (pretty JSON, padded base64 content), or v2 with the staged
// after images and before-image links named instead of inlined.
func EncodeJournal(in *Intent) []byte {
	if in.JournalVersion == 2 {
		return encodeJournalV2(in)
	}
	targets := make([]ojson.Value, len(in.Targets))
	for i, t := range in.Targets {
		targets[i] = ojson.NewObject(6).
			Set("path", ojson.StringValue(t.Rel)).
			Set("beforeHash", nullable(t.BeforeHash())).
			Set("afterHash", nullable(t.AfterHash())).
			Set("beforeContent", content(t.Before, t.BeforeExists)).
			Set("afterContent", content(t.After, t.AfterExists)).
			Set("mode", ojson.IntValue(int64(t.Mode.Perm()))).Value()
	}
	v := ojson.NewObject(6).
		Set("schemaVersion", ojson.IntValue(1)).
		Set("transactionId", ojson.StringValue(in.TransactionID)).
		Set("workplanId", ojson.StringValue(in.WorkplanID)).
		Set("operation", ojson.StringValue(in.Operation)).
		Set("createdAt", ojson.StringValue(in.CreatedAt)).
		Set("targets", ojson.ArrayValue(targets)).Value()
	return append(ojson.Pretty(v), '\n')
}

func content(b []byte, ok bool) ojson.Value {
	if !ok {
		return ojson.NullValue()
	}
	return ojson.StringValue(base64.StdEncoding.EncodeToString(b))
}

func encodeJournalV2(in *Intent) []byte {
	targets := make([]ojson.Value, len(in.Targets))
	for i, t := range in.Targets {
		targets[i] = ojson.NewObject(6).
			Set("path", ojson.StringValue(t.Rel)).
			Set("beforeHash", nullable(t.BeforeHash())).
			Set("afterHash", nullable(t.AfterHash())).
			Set("beforeBackup", nullable(t.Backup)).
			Set("afterStage", nullable(t.Stage)).
			Set("mode", ojson.IntValue(int64(t.Mode.Perm()))).Value()
	}
	v := ojson.NewObject(6).
		Set("schemaVersion", ojson.IntValue(2)).
		Set("transactionId", ojson.StringValue(in.TransactionID)).
		Set("workplanId", ojson.StringValue(in.WorkplanID)).
		Set("operation", ojson.StringValue(in.Operation)).
		Set("createdAt", ojson.StringValue(in.CreatedAt)).
		Set("targets", ojson.ArrayValue(targets)).Value()
	return append(ojson.Pretty(v), '\n')
}
