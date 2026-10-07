// Package engine implements the read-only workplan operations (list, read,
// inspect, validate, resume, doctor) over byte-exact snapshots. Every
// operation opens artifacts read-only; none writes defaults, upgrades a
// format, repairs, creates locks or changes a file's mtime.
package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/gitview"
	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Engine runs read-only operations for one trusted project root.
type Engine struct {
	RequestedRoot string // as supplied by the trusted caller (absolute)
	Root          string // canonical (symlinks resolved)
	Limits        snapshot.Limits
	// MaxResponseBytes bounds a single operation's serialized output
	// (the protocol frame limit). Zero means the default 64 MiB.
	MaxResponseBytes int

	// Compaction configures the compaction advisor thresholds (nil: the
	// defaults). Set by the trusted caller (CLI or serve flag), never by
	// model input.
	Compaction *advisor.Thresholds

	// EvidenceSource and EvidenceTree are set by the trusted caller: the
	// record source ("agent" when empty) and a tree pinned before a run.
	EvidenceSource string
	EvidenceTree   *gitview.Tree

	// Cache, when set (serve), keeps artifacts and decoded plans between
	// operations. Trusted configuration.
	Cache *snapshot.Cache

	// JournalVersion selects the journal format of new writes: 2 (the
	// default, images by reference) or 1 (inline, readable by the
	// reference plugin). Trusted configuration.
	JournalVersion int

	// Source is the change-log origin of writes ("agent" when empty);
	// NoHistory turns the change log off. Trusted configuration.
	Source    string
	NoHistory bool
	// Rebase is the default of workplan_update's rebase member.
	Rebase bool
}

// DefaultMaxResponseBytes is the approved response frame limit.
const DefaultMaxResponseBytes = 64 << 20

// New resolves the root and returns an engine with default limits.
func New(root string) (*Engine, error) {
	req, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	canon, err := snapshot.CanonicalRoot(req)
	if err != nil {
		return nil, err
	}
	e := &Engine{RequestedRoot: req, Root: canon, Limits: snapshot.DefaultLimits}
	if testCacheAll {
		e.Cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
	}
	return e, nil
}

// testCacheAll gives every engine a cache (tests: SHIORI_TEST_CACHE=1).
var testCacheAll = false

func (e *Engine) maxResponse() int {
	if e.MaxResponseBytes > 0 {
		return e.MaxResponseBytes
	}
	return DefaultMaxResponseBytes
}

func (e *Engine) dir() string {
	return filepath.Join(e.Root, filepath.FromSlash(snapshot.WorkplanDir))
}

func (e *Engine) absRel(rel string) string { return filepath.Join(e.Root, filepath.FromSlash(rel)) }

// load is a read's snapshot: through the cache, trusting unchanged stats.
func (e *Engine) load(id string) (*snapshot.Snapshot, error) {
	return snapshot.LoadWith(&snapshot.Reader{Root: e.Root, Limits: e.Limits, Cache: e.Cache, TrustStat: true}, id)
}

// loadFresh is a writer's snapshot: every file is read and hashed; only
// the content-keyed plan decode may come from the cache.
func (e *Engine) loadFresh(id string) (*snapshot.Snapshot, error) {
	return snapshot.LoadWith(&snapshot.Reader{Root: e.Root, Limits: e.Limits, Cache: e.Cache}, id)
}

// normalizeRequested normalizes a caller-supplied id (inputs are already
// validated to contain [A-Za-z0-9]).
func normalizeRequested(raw string) (string, error) { return model.NormalizeID(raw) }

// depView is the decoded dependency sidecar plus its issues.
type depView struct {
	recorded bool
	deps     *model.Dependencies
	issues   []string // unprefixed "path: message"
}

// dependencies decodes the sidecar; ix may be nil and is then built only
// when a sidecar exists.
func (e *Engine) dependencies(s *snapshot.Snapshot, ix *index.Plan) depView {
	v := depView{recorded: s.Dependencies.Exists}
	if !v.recorded {
		return v
	}
	if ix == nil {
		ix = index.Build(s.Plan)
	}
	parsed, err := ojson.Parse(s.Dependencies.Bytes)
	if err != nil {
		v.issues = []string{"Invalid workplan dependencies JSON at " + s.Dependencies.Path + ": " + err.Error()}
		return v
	}
	d, issues := model.DecodeDependencies(parsed.Value)
	if len(issues) > 0 {
		for _, is := range issues {
			v.issues = append(v.issues, is.String())
		}
		return v
	}
	v.deps = d
	v.issues = index.ValidateDependencies(ix, d)
	return v
}

func refValue(r model.StepRef) ojson.Value {
	return ojson.NewObject(2).
		Set("phaseId", ojson.StringValue(r.PhaseID)).
		Set("stepId", ojson.StringValue(r.StepID)).Value()
}

func (v depView) value() ojson.Value {
	deps := []ojson.Value{}
	terms := []ojson.Value{}
	if v.deps != nil {
		for _, en := range v.deps.Entries {
			on := make([]ojson.Value, len(en.DependsOn))
			for i, r := range en.DependsOn {
				on[i] = refValue(r)
			}
			deps = append(deps, ojson.NewObject(3).
				Set("phaseId", ojson.StringValue(en.PhaseID)).
				Set("stepId", ojson.StringValue(en.StepID)).
				Set("dependsOn", ojson.ArrayValue(on)).Value())
		}
		for _, ts := range v.deps.TerminalSummaries {
			terms = append(terms, ojson.NewObject(4).
				Set("phaseId", ojson.StringValue(ts.PhaseID)).
				Set("stepId", ojson.StringValue(ts.StepID)).
				Set("title", ojson.StringValue(ts.Title)).
				Set("status", ojson.StringValue(ts.Status)).Value())
		}
	}
	issues := v.issues
	if issues == nil {
		issues = []string{}
	}
	return ojson.NewObject(4).
		Set("recorded", ojson.BoolValue(v.recorded)).
		Set("dependencies", ojson.ArrayValue(deps)).
		Set("terminalSummaries", ojson.ArrayValue(terms)).
		Set("issues", ojson.StringsValue(issues)).Value()
}

// sliceValue is value() restricted to entries whose source step is
// selected or that depend on a selected step, and the terminal summaries
// those entries reference (filtered read). Issues stay complete.
func (v depView) sliceValue(sel map[model.StepRef]bool) ojson.Value {
	if v.deps == nil {
		return v.value()
	}
	d := *v.deps
	d.Entries = nil
	refs := map[model.StepRef]bool{}
	for _, en := range v.deps.Entries {
		keep := sel[model.StepRef{PhaseID: en.PhaseID, StepID: en.StepID}]
		for _, r := range en.DependsOn {
			keep = keep || sel[r]
		}
		if keep {
			d.Entries = append(d.Entries, en)
			for _, r := range en.DependsOn {
				refs[r] = true
			}
		}
	}
	d.TerminalSummaries = nil
	for _, ts := range v.deps.TerminalSummaries {
		if refs[model.StepRef{PhaseID: ts.PhaseID, StepID: ts.StepID}] || sel[model.StepRef{PhaseID: ts.PhaseID, StepID: ts.StepID}] {
			d.TerminalSummaries = append(d.TerminalSummaries, ts)
		}
	}
	v.deps = &d
	return v.value()
}

// Checkpoint freshness classes.
const (
	FreshnessMissing = "missing"
	FreshnessFresh   = "fresh"
	FreshnessStale   = "stale"
	FreshnessLegacy  = "legacy-unverified"
	FreshnessInvalid = "invalid"
	msgStale         = "Checkpoint does not match the current JSON/Markdown/spec manifest"
	msgLegacy        = "Checkpoint v1 only hashes JSON; multiartifact freshness is unverified"
)

// cpView classifies the checkpoint sidecar. Reads never upgrade it.
type cpView struct {
	exists    bool
	freshness string
	cp        *model.Checkpoint
	// issue is the doctor text after "checkpoint: "; diagnostic is the
	// resume diagnostic for an invalid checkpoint.
	issue      string
	diagnostic string
	// staleDetail names what differs from a stale v2 checkpoint's
	// manifest; doctor appends it.
	staleDetail string
	// staleChanged are the manifest paths behind staleDetail, in the same
	// order (for the compact resume diagnostic).
	staleChanged []string
}

func classifyCheckpoint(s *snapshot.Snapshot) cpView {
	v := cpView{exists: s.Checkpoint.Exists, freshness: FreshnessMissing}
	if !v.exists {
		return v
	}
	parsed, err := ojson.Parse(s.Checkpoint.Bytes)
	if err != nil {
		v.freshness = FreshnessInvalid
		v.issue = err.Error()
		v.diagnostic = "Invalid workplan checkpoint JSON at " + s.Checkpoint.Path + ": " + err.Error()
		return v
	}
	cp, ok := model.DecodeCheckpoint(parsed.Value)
	if !ok {
		v.freshness = FreshnessInvalid
		v.issue = model.ErrCheckpointSchema
		v.diagnostic = "Invalid workplan checkpoint document at " + s.Checkpoint.Path + ": " + model.ErrCheckpointSchema
		return v
	}
	v.cp = cp
	switch {
	case cp.SchemaVersion == 1:
		v.freshness = FreshnessLegacy
		v.issue = msgLegacy
	case cp.PlanHash == s.PlanHash:
		v.freshness = FreshnessFresh
	default:
		v.freshness = FreshnessStale
		v.issue = msgStale
		v.staleDetail, v.staleChanged = staleDetail(cp, s)
	}
	return v
}

// maxDiagnosticPaths and maxDiagnosticUnits bound the compact resume
// diagnostic of a stale checkpoint.
const (
	maxDiagnosticPaths = 3
	maxDiagnosticUnits = 240
)

// staleDiagnostic is the compact resume form of staleDetail:
// "changed: <path>[, <path>...] [+N more]" (at most three paths), or
// "changed: planHash only" when every manifest entry still matches. The
// text is capped at maxDiagnosticUnits UTF-16 code units.
func staleDiagnostic(paths []string) string {
	if len(paths) == 0 {
		return "changed: planHash only"
	}
	shown := paths
	if len(shown) > maxDiagnosticPaths {
		shown = shown[:maxDiagnosticPaths]
	}
	out := "changed: " + strings.Join(shown, ", ")
	if n := len(paths) - len(shown); n > 0 {
		out += " +" + strconv.Itoa(n) + " more"
	}
	out, _ = ojson.TruncateUTF16(out, maxDiagnosticUnits)
	return out
}

// maxStaleEntries bounds the artifacts named by staleDetail.
const maxStaleEntries = 5

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// staleDetail compares a stale v2 checkpoint's manifest with the current
// plan manifest and names each artifact whose bytes changed, appeared,
// disappeared or entered/left the manifest.
func staleDetail(cp *model.Checkpoint, s *snapshot.Snapshot) (string, []string) {
	state := func(sha string, missing bool) string {
		if missing {
			return "missing"
		}
		return "sha256 " + short(sha)
	}
	then := map[string]model.ManifestEntry{}
	for _, en := range cp.Manifest {
		then[en.Path] = en
	}
	var parts, paths []string
	now := map[string]bool{}
	for _, en := range s.PlanManifest {
		now[en.Path] = true
		old, ok := then[en.Path]
		switch {
		case !ok:
			parts = append(parts, en.Path+" was added to the plan's links since the checkpoint (now "+state(en.SHA256, en.Missing)+")")
			paths = append(paths, en.Path)
		case old.Missing != en.Missing || old.SHA256 != en.SHA256:
			parts = append(parts, en.Path+" changed (checkpoint "+state(old.SHA256, old.Missing)+", now "+state(en.SHA256, en.Missing)+")")
			paths = append(paths, en.Path)
		}
	}
	for _, en := range cp.Manifest {
		if !now[en.Path] {
			parts = append(parts, en.Path+" is no longer linked (checkpoint "+state(en.SHA256, en.Missing)+")")
			paths = append(paths, en.Path)
		}
	}
	if len(parts) == 0 {
		return "Every manifest entry matches, but the recorded planHash differs (checkpoint " + short(cp.PlanHash) + ", now " + short(s.PlanHash) + ")", nil
	}
	more := ""
	if len(parts) > maxStaleEntries {
		more = fmt.Sprintf("; and %d more", len(parts)-maxStaleEntries)
		parts = parts[:maxStaleEntries]
	}
	return "Changed since the checkpoint: " + strings.Join(parts, "; ") + more + ". Write a new checkpoint after reviewing the change.", paths
}

// journalRel is the pending journal path for an id.
func journalRel(id string) string { return snapshot.SidecarRel(id, ".transaction.json") }

// recoveryPacket is the reference's short-circuit output for a plan with a
// pending journal (read/inspect/resume).
func recoveryPacket(s *snapshot.Snapshot, tool string) ojson.Value {
	b := ojson.NewObject(5).
		Set("recoveryRequired", ojson.BoolValue(true)).
		Set("journalPath", ojson.StringValue(journalRel(s.ID))).
		Set("planHash", ojson.StringValue(s.PlanHash)).
		Set("stateHash", ojson.StringValue(s.StateHash))
	switch tool {
	case "read":
		b.Set("workplan", ojson.NullValue())
	case "resume":
		b.Set("planFresh", ojson.BoolValue(false))
	}
	return b.Value()
}

// errUnsupported builds the bounded-output refusal.
func (e *Engine) checkResponse(v ojson.Value, tool, id string) (ojson.Value, error) {
	if len(ojson.Pretty(v)) > e.maxResponse() {
		return ojson.Value{}, fmt.Errorf("%w: %s output for %s exceeds the %d-byte response limit; use includeMarkdown=false, workplan_inspect or workplan_resume", snapshot.ErrUnsupported, tool, id, e.maxResponse())
	}
	return v, nil
}

// IsUnsupported reports an unsupported_capability error.
func IsUnsupported(err error) bool { return errors.Is(err, snapshot.ErrUnsupported) }

// compactionAdvice is the compaction advice for a loaded plan, or nil when
// compaction is not recommended. withMarkdown (doctor) adds the linked
// Markdown estimate, which needs a full render.
func (e *Engine) compactionAdvice(s *snapshot.Snapshot, freshness string, withMarkdown bool) *advisor.Advice {
	o := advisor.Options{Thresholds: e.Compaction.Resolve(), Freshness: freshness, Now: e.nowISO()}
	if withMarkdown {
		o.Generated = func() bool {
			gen, err := e.generatedMarkdown(s)
			return err == nil && gen
		}
	}
	return advisor.Advise(s, o)
}
