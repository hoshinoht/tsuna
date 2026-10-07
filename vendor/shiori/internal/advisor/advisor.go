// Package advisor holds the compaction advisor and note rollover selection.
//
// The advisor is advice only. resume and doctor report
// compactionRecommended when the history that workplan_compact could
// archive (completed phases, rolled-over notes, resolved findings) would
// save at least MinSavingsBytes and at least one threshold is crossed.
// Nothing is archived without the unchanged preview -> exact token ->
// ARCHIVE_SELECTED_HISTORY flow.
//
// Note rollover is a selection mode of that flow: every note older than
// the latest keepLatest is selected unless it is pinned. Notes carry no
// timestamps, so "older than the newest checkpoint" is enforced by the
// apply rule that already requires a fresh checkpoint: a fresh checkpoint
// binds the exact current plan bytes, so every stored note predates it.
package advisor

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Thresholds configure the advisor. Zero fields take the defaults; Off
// disables the advice.
type Thresholds struct {
	Off             bool
	MinSavingsBytes int // the estimated JSON+Markdown saving must reach this
	Notes           int // notes eligible for rollover
	TerminalPercent int // archivable completed-phase bytes, % of the plan JSON
	PlanBytes       int // plan JSON size
	KeepNotes       int // rollover keepLatest the advice uses
}

// DefaultThresholds are grounded in a long-plan measurement (257 KB JSON,
// 214 notes; notes ≈48% of the JSON, terminal steps ≈48% of the step
// bytes). A plan crosses a threshold well before that size, and small
// plans (under 32 KiB of possible saving) stay quiet.
var DefaultThresholds = Thresholds{
	MinSavingsBytes: 32 << 10,
	Notes:           50,
	TerminalPercent: 25,
	PlanBytes:       192 << 10,
	KeepNotes:       DefaultRolloverKeep,
}

// Rollover bounds.
const (
	DefaultRolloverKeep = 20
	MaxRolloverKeep     = 10000
)

// Resolve returns the effective thresholds: the defaults overridden by the
// positive fields of c (nil: the defaults).
func (c *Thresholds) Resolve() Thresholds {
	t := DefaultThresholds
	if c != nil {
		t.Off = c.Off
		if c.MinSavingsBytes > 0 {
			t.MinSavingsBytes = c.MinSavingsBytes
		}
		if c.Notes > 0 {
			t.Notes = c.Notes
		}
		if c.TerminalPercent > 0 {
			t.TerminalPercent = c.TerminalPercent
		}
		if c.PlanBytes > 0 {
			t.PlanBytes = c.PlanBytes
		}
		if c.KeepNotes > 0 {
			t.KeepNotes = c.KeepNotes
		}
	}
	return t
}

// ParseThresholds parses the CLI/serve --compaction-advice value: "off",
// or comma-separated key=value pairs with the keys min-savings-kib, notes,
// terminal-percent, plan-kib and keep-notes (positive integers).
func ParseThresholds(spec string) (*Thresholds, error) {
	t := &Thresholds{}
	if strings.TrimSpace(spec) == "off" {
		t.Off = true
		return t, nil
	}
	for _, part := range strings.Split(spec, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		n, err := strconv.Atoi(v)
		if !ok || err != nil || n <= 0 || n > 1<<30 {
			return nil, fmt.Errorf("--compaction-advice: expected off or key=positive-integer pairs, got %q", part)
		}
		switch k {
		case "min-savings-kib":
			t.MinSavingsBytes = n << 10
		case "notes":
			t.Notes = n
		case "terminal-percent":
			if n > 100 {
				return nil, fmt.Errorf("--compaction-advice: terminal-percent must be 1-100")
			}
			t.TerminalPercent = n
		case "plan-kib":
			t.PlanBytes = n << 10
		case "keep-notes":
			if n > MaxRolloverKeep {
				return nil, fmt.Errorf("--compaction-advice: keep-notes must be 1-%d", MaxRolloverKeep)
			}
			t.KeepNotes = n
		default:
			return nil, fmt.Errorf("--compaction-advice: unknown key %q (min-savings-kib, notes, terminal-percent, plan-kib, keep-notes)", k)
		}
	}
	return t, nil
}

// ---- note rollover ----

// Keep reasons, in precedence order.
const (
	keepPinned         = "pinned"
	keepDecision       = "decision"
	keepOpenReference  = "openReference"
	keepArchivePointer = "archivePointer"
)

var keepReasons = []string{keepPinned, keepDecision, keepOpenReference, keepArchivePointer}

// PinnedNoteMarker explicitly pins a note (case-insensitive, anywhere).
const PinnedNoteMarker = "[pinned]"

// isDecision recognizes a decision record: the JSON twin of a Decision
// register line under the owner's convention ("record every user decision
// as a JSON note and a Decision register line"), i.e. the whole words
// decision/decisions/decided in any case, or the uppercase word USER.
func isDecision(n string) bool {
	return hasWord(n, "USER") || hasWordFold(n, "decision") || hasWordFold(n, "decisions") || hasWordFold(n, "decided")
}

func wordChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// hasWord finds w as a whole ASCII word (regexp \b semantics).
func hasWord(n, w string) bool {
	for from := 0; ; {
		i := strings.Index(n[from:], w)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(w)
		if (i == 0 || !wordChar(n[i-1])) && (end == len(n) || !wordChar(n[end])) {
			return true
		}
		from = i + 1
	}
}

// hasWordFold is hasWord ignoring ASCII case (w is lowercase).
func hasWordFold(n, w string) bool {
	for i := 0; i+len(w) <= len(n); i++ {
		if n[i]|0x20 != w[0] {
			continue
		}
		if (i == 0 || !wordChar(n[i-1])) && (i+len(w) == len(n) || !wordChar(n[i+len(w)])) && strings.EqualFold(n[i:i+len(w)], w) {
			return true
		}
	}
	return false
}

const archivePointerPrefix = "Compaction archive: "

// KeepArchivePointers is how many archive pointer notes rollover keeps:
// the latest ones in the plan's note order, counting pointers inside the
// latest keepLatest. Older pointer notes are ordinary notes for rollover
// (archived with the complete original text, never lost).
const KeepArchivePointers = 3

// Rollover is a note rollover selection.
type Rollover struct {
	keep     int
	pins     []int
	total    int
	latest   int            // notes inside the latest keep
	selected []int          // older, unpinned notes, ascending
	kept     map[string]int // older notes kept, by reason
	bytes    int            // UTF-8 bytes of the selected note texts
}

// SelectRollover applies the rollover rule to a plan.
func SelectRollover(p *model.Plan, keep int, pins []int) Rollover {
	r := Rollover{keep: keep, pins: pins, total: len(p.Notes), kept: map[string]int{}}
	cut := len(p.Notes) - keep
	if cut < 0 {
		cut = 0
	}
	r.latest = len(p.Notes) - cut
	pinned := map[int]bool{}
	for _, i := range pins {
		pinned[i] = true
	}
	refs := openRefs(p)
	keptPointer := map[int]bool{}
	for i, left := len(p.Notes)-1, KeepArchivePointers; i >= 0 && left > 0; i-- {
		if strings.HasPrefix(p.Notes[i], archivePointerPrefix) {
			keptPointer[i] = true
			left--
		}
	}
	for i := 0; i < cut; i++ {
		n := p.Notes[i]
		reason := ""
		switch {
		case pinned[i] || strings.Contains(strings.ToLower(n), PinnedNoteMarker):
			reason = keepPinned
		case isDecision(n):
			reason = keepDecision
		case refs.match(n):
			reason = keepOpenReference
		case keptPointer[i]:
			reason = keepArchivePointer
		}
		if reason != "" {
			r.kept[reason]++
			continue
		}
		r.selected = append(r.selected, i)
		r.bytes += len(n)
	}
	return r
}

// Selected lists the selected note indexes, ascending.
func (r Rollover) Selected() []int { return r.selected }

// openRefSet holds the `<phaseId>/<stepId>` of every open step and the
// titles of open findings (at least 12 code units, to avoid matching
// common words). Matching is linear in the note length: step references
// are whole id tokens around a '/', and titles are looked up by their
// first titlePrefix bytes.
type openRefSet struct {
	steps    map[string]bool
	titles   map[string][]string // by the first titlePrefix bytes
	hasTitle bool
}

const titlePrefix = 12

func openRefs(p *model.Plan) openRefSet {
	r := openRefSet{steps: map[string]bool{}, titles: map[string][]string{}}
	for _, ph := range p.Phases {
		for _, st := range ph.Steps {
			if !terminal(st.Status) && ph.ID != "" && st.ID != "" {
				r.steps[ph.ID+"/"+st.ID] = true
			}
		}
	}
	for _, f := range p.Findings {
		t := strings.TrimSpace(f.Title)
		if f.Open() && ojson.UTF16Len(t) >= 12 && len(t) >= titlePrefix {
			r.titles[t[:titlePrefix]] = append(r.titles[t[:titlePrefix]], t)
			r.hasTitle = true
		}
	}
	return r
}

func idChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

// match reports whether a note names an open step as a whole
// `<phase>/<step>` token or quotes an open finding's title.
func (r openRefSet) match(n string) bool {
	if len(r.steps) > 0 {
		for i := 0; i < len(n); i++ {
			if n[i] != '/' {
				continue
			}
			a := i
			for a > 0 && idChar(n[a-1]) {
				a--
			}
			b := i + 1
			for b < len(n) && idChar(n[b]) {
				b++
			}
			if a < i && b > i+1 && r.steps[n[a:b]] {
				return true
			}
		}
	}
	if r.hasTitle {
		for i := 0; i+titlePrefix <= len(n); i++ {
			for _, t := range r.titles[n[i:i+titlePrefix]] {
				if strings.HasPrefix(n[i:], t) {
					return true
				}
			}
		}
	}
	return false
}

func (r Rollover) keptValue() ojson.Value {
	b := ojson.NewObject(5).Set("latest", ojson.IntValue(int64(r.latest)))
	for _, k := range keepReasons {
		b.Set(k, ojson.IntValue(int64(r.kept[k])))
	}
	return b.Value()
}

// InputValue is the canonical rollover input the preview token binds.
func (r Rollover) InputValue() ojson.Value {
	return ojson.NewObject(2).
		Set("keepLatest", ojson.IntValue(int64(r.keep))).
		Set("pinNoteIndexes", intsValue(orEmptyInts(r.pins))).Value()
}

// PreviewValue is the rollover breakdown of a compaction preview.
func (r Rollover) PreviewValue() ojson.Value {
	older := r.total - r.latest
	return ojson.NewObject(7).
		Set("keepLatest", ojson.IntValue(int64(r.keep))).
		Set("pinNoteIndexes", intsValue(orEmptyInts(r.pins))).
		Set("noteCount", ojson.IntValue(int64(r.total))).
		Set("olderThanLatest", ojson.IntValue(int64(older))).
		Set("selectedCount", ojson.IntValue(int64(len(r.selected)))).
		Set("kept", r.keptValue()).
		Set("olderThanCheckpoint", ojson.StringValue("enforced at apply: apply requires a fresh checkpoint, which binds the current plan bytes, so every selected note predates the newest checkpoint")).Value()
}

// ---- advisor ----

// ByteDelta is a before/after byte count.
type ByteDelta struct{ Before, After int }

// Value renders the delta with its saving and percentage, then extra.
func (d ByteDelta) Value(extra ...ojson.Member) ojson.Value {
	b := ojson.NewObject(5).
		Set("before", ojson.IntValue(int64(d.Before))).
		Set("after", ojson.IntValue(int64(d.After))).
		Set("saved", ojson.IntValue(int64(d.Before-d.After))).
		Set("percent", ojson.IntValue(int64(percent(d.Before-d.After, d.Before))))
	for _, m := range extra {
		b.Set(m.Key, m.Value)
	}
	return b.Value()
}

func percent(part, whole int) int {
	if whole <= 0 || part <= 0 {
		return 0
	}
	return part * 100 / whole
}

// Advice is a compaction recommendation.
type Advice struct {
	th             Thresholds
	reasons        []string
	json, md       ByteDelta
	gen            bool
	phaseIDs       []string
	archivable     int // steps in archivable phases
	archivableSize int // plan JSON bytes those phases account for
	terminal       int // completed or cancelled steps anywhere
	roll           Rollover
	notesSize      int // plan JSON bytes the rolled-over notes account for
	findings       []int
	findingsSize   int // plan JSON bytes the resolved findings account for
	freshness      string
}

// Options is what the advisor needs besides the snapshot.
type Options struct {
	Thresholds Thresholds
	Freshness  string // checkpoint freshness class
	Now        string // the timestamp an apply would record
	// Generated reports whether the linked Markdown is the generated
	// rendering. Nil leaves the Markdown estimate out (resume): it needs
	// a full render.
	Generated func() bool
}

// archivablePhase is the compaction rule: completed with every step
// explicitly completed.
func archivablePhase(ph model.Phase) bool {
	ok := ph.Status == "completed" && len(ph.Steps) > 0
	for _, st := range ph.Steps {
		ok = ok && st.Status == "completed"
	}
	return ok
}

func terminal(status string) bool { return status == "completed" || status == "cancelled" }

// elemLen is the length of v pretty-printed as an element of a top-level
// plan array (level 2: every nested line gets four more spaces).
func elemLen(v ojson.Value) int {
	b := ojson.Pretty(v)
	return len(b) + 4*bytes.Count(b, []byte{'\n'})
}

// arraySaved is how many bytes the pretty encoding of a top-level plan
// array of n elements shrinks when the elements of the given lengths are
// removed: each element is "\n" + four spaces + its text, separated by
// commas, and an emptied array becomes "[]".
func arraySaved(n int, removed []int) int {
	sum := 0
	for _, l := range removed {
		sum += 5 + l
	}
	if len(removed) == 0 {
		return 0
	}
	if len(removed) == n {
		return sum + n + 2
	}
	return sum + len(removed)
}

// Advise estimates what compaction could archive and returns the advice
// when it is recommended, else nil. The plan JSON saving is computed from
// the pretty encoding of the removed elements, so it is the exact size
// that apply writes (archive pointer note and new updatedAt included).
func Advise(s *snapshot.Snapshot, o Options) *Advice {
	if s == nil || s.Plan == nil || s.Journal.Exists {
		return nil
	}
	th := o.Thresholds
	if th.Off || len(s.JSON.Bytes) < th.MinSavingsBytes {
		return nil
	}
	p := s.Plan
	a := &Advice{th: th, freshness: o.Freshness}
	var phaseLens, noteLens, findingLens []int
	for _, ph := range p.Phases {
		for _, st := range ph.Steps {
			if terminal(st.Status) {
				a.terminal++
			}
		}
		if archivablePhase(ph) {
			a.phaseIDs = append(a.phaseIDs, ph.ID)
			a.archivable += len(ph.Steps)
			phaseLens = append(phaseLens, elemLen(ph.ToValue()))
		}
	}
	a.roll = SelectRollover(p, th.KeepNotes, nil)
	for _, i := range a.roll.selected {
		noteLens = append(noteLens, len(ojson.Quote(p.Notes[i])))
	}
	for i, f := range p.Findings {
		if !f.Open() {
			a.findings = append(a.findings, i)
			findingLens = append(findingLens, elemLen(f.ToValue()))
		}
	}
	if len(a.phaseIDs)+len(a.roll.selected)+len(a.findings) == 0 {
		return nil
	}
	a.archivableSize = arraySaved(len(p.Phases), phaseLens)
	a.notesSize = arraySaved(len(p.Notes), noteLens)
	a.findingsSize = arraySaved(len(p.Findings), findingLens)
	// Apply appends the archive pointer note (same length as the real
	// path) and stamps a new updatedAt.
	pointer := archivePointerPrefix + snapshot.WorkplanDir + "/archive/" + p.ID + "/state-000000000000-000000000000.json"
	added := 5 + len(ojson.Quote(pointer)) + 1
	if len(noteLens) == len(p.Notes) {
		added = len(ojson.Quote(pointer)) + 8
	}
	added += len(o.Now) - len(p.UpdatedAt)
	before := len(s.JSON.Bytes)
	a.json = ByteDelta{before, before - a.archivableSize - a.notesSize - a.findingsSize + added}
	if a.json.Before-a.json.After < th.MinSavingsBytes {
		return nil
	}
	if n := len(a.roll.selected); n >= th.Notes {
		a.reasons = append(a.reasons, fmt.Sprintf("notes: %d notes are eligible for rollover (threshold %d)", n, th.Notes))
	}
	if pc := percent(a.archivableSize, before); pc >= th.TerminalPercent {
		a.reasons = append(a.reasons, fmt.Sprintf("terminalSteps: archivable completed phases are %d%% of the plan JSON (threshold %d%%)", pc, th.TerminalPercent))
	}
	if before >= th.PlanBytes {
		a.reasons = append(a.reasons, fmt.Sprintf("size: the plan JSON is %d bytes (threshold %d)", before, th.PlanBytes))
	}
	if len(a.reasons) == 0 {
		return nil
	}
	a.md = ByteDelta{len(s.Markdown.Bytes), len(s.Markdown.Bytes)}
	if o.Generated != nil && o.Generated() {
		a.gen = true
		if md, err := model.RenderMarkdown(adviceNext(p, a, pointer)); err == nil {
			a.md.After = len(md)
		}
	}
	return a
}

// adviceNext is the plan after the advised compaction (for the Markdown
// rendering).
func adviceNext(p *model.Plan, a *Advice, pointer string) *model.Plan {
	q := p.Clone()
	drop := map[string]bool{}
	for _, id := range a.phaseIDs {
		drop[id] = true
	}
	q.Phases = q.Phases[:0:0]
	for _, ph := range p.Phases {
		if !drop[ph.ID] {
			q.Phases = append(q.Phases, ph)
		}
	}
	gone := map[int]bool{}
	for _, i := range a.roll.selected {
		gone[i] = true
	}
	q.Notes = nil
	for i, n := range p.Notes {
		if !gone[i] {
			q.Notes = append(q.Notes, n)
		}
	}
	q.Notes = appendNote(q.Notes, pointer)
	q.Findings = []model.Finding{}
	for _, f := range p.Findings {
		if f.Open() {
			q.Findings = append(q.Findings, f)
		}
	}
	return q
}

// appendNote appends the trimmed note unless it is blank or present.
func appendNote(notes []string, n string) []string {
	out := append([]string(nil), notes...)
	t := model.TrimJS(n)
	if t == "" {
		return out
	}
	for _, x := range notes {
		if x == t {
			return out
		}
	}
	return append(out, t)
}

func (a *Advice) total() ByteDelta {
	return ByteDelta{a.json.Before + a.md.Before, a.json.After + a.md.After}
}

// CompactValue is resume's compact form. Its saving is the plan JSON's
// (resume does not render the Markdown); doctor has the detail.
func (a *Advice) CompactValue() ojson.Value {
	return ojson.NewObject(5).
		Set("savedJsonBytes", ojson.IntValue(int64(a.json.Before-a.json.After))).
		Set("savedJsonPercent", ojson.IntValue(int64(percent(a.json.Before-a.json.After, a.json.Before)))).
		Set("notes", ojson.IntValue(int64(len(a.roll.selected)))).
		Set("terminalSteps", ojson.IntValue(int64(a.archivable))).
		Set("resolvedFindings", ojson.IntValue(int64(len(a.findings)))).Value()
}

// DetailValue is doctor's full advice.
func (a *Advice) DetailValue(id string) ojson.Value {
	treatment := "preserved"
	if a.gen {
		treatment = "generated-refresh"
	}
	sel := ojson.NewObject(3).
		Set("completedPhaseIds", ojson.StringsValue(orEmpty(a.phaseIDs)))
	if len(a.roll.selected) > 0 {
		sel.Set("noteRollover", ojson.NewObject(1).Set("keepLatest", ojson.IntValue(int64(a.roll.keep))).Value())
	}
	sel.Set("resolvedFindingIndexes", intsValue(a.findings))
	th := a.th
	return ojson.NewObject(10).
		Set("reasons", ojson.StringsValue(a.reasons)).
		Set("estimate", ojson.NewObject(3).
			Set("json", a.json.Value()).
			Set("markdown", a.md.Value(ojson.Member{Key: "treatment", Value: ojson.StringValue(treatment)})).
			Set("total", a.total().Value()).Value()).
		Set("terminalSteps", ojson.NewObject(3).
			Set("total", ojson.IntValue(int64(a.terminal))).
			Set("archivable", ojson.IntValue(int64(a.archivable))).
			Set("archivableBytes", ojson.IntValue(int64(a.archivableSize))).Value()).
		Set("notes", ojson.NewObject(5).
			Set("total", ojson.IntValue(int64(a.roll.total))).
			Set("keepLatest", ojson.IntValue(int64(a.roll.keep))).
			Set("eligible", ojson.IntValue(int64(len(a.roll.selected)))).
			Set("eligibleBytes", ojson.IntValue(int64(a.notesSize))).
			Set("kept", a.roll.keptValue()).Value()).
		Set("resolvedFindings", ojson.NewObject(2).
			Set("count", ojson.IntValue(int64(len(a.findings)))).
			Set("bytes", ojson.IntValue(int64(a.findingsSize))).Value()).
		Set("selection", sel.Value()).
		Set("checkpointFreshness", ojson.StringValue(a.freshness)).
		Set("thresholds", ojson.NewObject(5).
			Set("minSavingsBytes", ojson.IntValue(int64(th.MinSavingsBytes))).
			Set("notes", ojson.IntValue(int64(th.Notes))).
			Set("terminalPercent", ojson.IntValue(int64(th.TerminalPercent))).
			Set("planBytes", ojson.IntValue(int64(th.PlanBytes))).
			Set("keepNotes", ojson.IntValue(int64(th.KeepNotes))).Value()).
		Set("instruction", ojson.StringValue("Advice only; nothing was archived. Preview with workplan_compact_preview id="+id+
			", an archiveReason and this selection, then apply with workplan_compact mode=apply, the exact previewToken, confirmation=ARCHIVE_SELECTED_HISTORY, the current stateHash and a fresh checkpoint. The archive keeps the complete originals.")).Value()
}

func intsValue(xs []int) ojson.Value {
	out := make([]ojson.Value, len(xs))
	for i, x := range xs {
		out[i] = ojson.IntValue(int64(x))
	}
	return ojson.ArrayValue(out)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyInts(xs []int) []int {
	if xs == nil {
		return []int{}
	}
	return xs
}
