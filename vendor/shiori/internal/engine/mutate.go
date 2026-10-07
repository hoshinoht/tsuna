package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// Mutation flow: Prepare* validates input and
// state, reads the complete artifact set and returns a Prepared intent
// without creating any file, lock or directory. Execute asks the
// Authorizer about exactly that intent and only then commits it through
// the storage engine, which rechecks everything under the locks.

// AuthRequest is what an Authorizer decides on.
type AuthRequest struct {
	Tool      string
	Intent    *storage.Intent
	Digest    string
	Resources storage.Resources
}

// Authorizer grants or refuses a prepared intent. It must not change any
// file. The CLI implements it with a TTY prompt or --yes; the native
// protocol with the host's permission decision.
type Authorizer interface {
	Authorize(ctx context.Context, req AuthRequest) error
}

// ErrDenied is a refused authorization.
var ErrDenied = errors.New("Workplan mutation was not authorized; nothing was changed")

// Prepared is a single-use prepared mutation.
type Prepared struct {
	Tool   string
	Intent *storage.Intent // nil: nothing would change; no authorization needed
	digest string
	used   bool
	// result renders the tool output for the committed state.
	result  func(directorySync bool) (Output, error)
	recheck func() error
}

// Output is a tool result: a JSON object, or text plus metadata (patch).
type Output struct {
	Value    ojson.Value
	Text     string
	Metadata ojson.Value
}

// String is the exact tool output text.
func (o Output) String() string {
	if o.Text != "" {
		return o.Text
	}
	return string(ojson.Pretty(o.Value))
}

// ExecOptions are trusted, non-model collaborators of Execute.
type ExecOptions struct {
	Hooks storage.Hooks
}

// Prepare dispatches a mutating tool ("create", "update", "patch",
// "reset", "checkpoint", "compact", "compact_preview") on accepted input.
func (e *Engine) Prepare(tool string, data ojson.Value) (*Prepared, error) {
	switch tool {
	case "create":
		return e.PrepareCreate(data)
	case "checkpoint":
		return e.PrepareCheckpoint(data)
	case "compact", "compact_preview":
		return e.PrepareCompact(data)
	case "patch":
		return e.PreparePatch(data)
	case "reset":
		return e.PrepareReset(data)
	case "update":
		return e.PrepareUpdate(data)
	}
	return nil, fmt.Errorf("unsupported mutation tool: %s", tool)
}

// Execute authorizes and commits a prepared mutation.
func (e *Engine) Execute(ctx context.Context, p *Prepared, auth Authorizer, opts ExecOptions) (Output, error) {
	if p.used {
		return Output{}, errors.New("prepared mutation already used; prepare again")
	}
	p.used = true
	if p.Intent == nil {
		return p.result(true)
	}
	if p.Intent.Digest() != p.digest {
		return Output{}, errors.New("prepared mutation changed after preparation; prepare again")
	}
	if err := ctx.Err(); err != nil {
		return Output{}, &storage.CancelledError{Stage: "authorization"}
	}
	if auth == nil {
		return Output{}, ErrDenied
	}
	if err := auth.Authorize(ctx, AuthRequest{Tool: p.Tool, Intent: p.Intent, Digest: p.digest, Resources: p.Intent.Resources()}); err != nil {
		return Output{}, err
	}
	// A late approval after cancellation cannot reactivate the request.
	if err := ctx.Err(); err != nil {
		return Output{}, &storage.CancelledError{Stage: "locking"}
	}
	h := opts.Hooks
	userRecheck := h.Recheck
	h.Recheck = func() error {
		if p.recheck != nil {
			if err := p.recheck(); err != nil {
				return err
			}
		}
		if userRecheck != nil {
			return userRecheck()
		}
		return nil
	}
	res, err := storage.Commit(ctx, p.Intent, h)
	if err != nil {
		return Output{}, err
	}
	return p.result(res.DirectorySync)
}

func finalize(p *Prepared) *Prepared {
	if p.Intent != nil {
		p.digest = p.Intent.Digest()
	}
	return p
}

// Clock is the engine's time source (tests freeze it).
var Clock = func() time.Time { return time.Now() }

// nowISO is the timestamp new writes record: whole-second UTC
// ("2006-01-02T15:04:05Z"). Readers keep
// accepting milliseconds and every other isoDatetimeUtc form.
func (e *Engine) nowISO() string {
	return Clock().UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
}

// render is the generated Markdown of a plan.
func (e *Engine) render(p *model.Plan) ([]byte, error) {
	return model.RenderMarkdown(p)
}

// isGenerated reports whether md is the generated rendering of p. Both
// the current and the legacy finding style count.
func (e *Engine) isGenerated(p *model.Plan, md []byte) (bool, error) {
	return model.IsGeneratedMarkdown(p, md)
}

// errPendingJournal is the reference refusal for a plan with a journal.
func (e *Engine) errPendingJournal(id string) error {
	return fmt.Errorf("Workplan transaction pending requires explicit recovery at %s", e.absRel(journalRel(id)))
}

// StaleHashError is the reference stale-state refusal.
type StaleHashError struct {
	Current    string
	NotRebased string // why a requested rebase did not apply
}

// staleHashGuidance is appended to the reference stale-state refusal; it
// adds no hash of its own.
const staleHashGuidance = " Use workplan_resume or workplan_inspect for that re-read before retrying, so the retry is based on the current plan."

func (e *StaleHashError) Error() string {
	msg := "Stale expectedHash; current stateHash is " + e.Current + ". Reread the plan and recompute the mutation." + staleHashGuidance
	if e.NotRebased != "" {
		msg += " Not rebased: " + e.NotRebased + "."
	}
	return msg
}

// DuplicateMembersError refuses to mutate a plan whose stored JSON repeats
// member names: the plan stays readable, writes fail closed.
type DuplicateMembersError struct {
	ID    string
	Paths []string
}

func (e *DuplicateMembersError) Error() string {
	return "Workplan " + e.ID + " has duplicate JSON member names: " + strings.Join(e.Paths, ", ") + ". Remove the duplicates before mutating the plan."
}

// loadForMutation reads the complete artifact set of an existing plan and
// applies the refusals common to every existing-state writer.
func (e *Engine) loadForMutation(raw string, expected *string) (*snapshot.Snapshot, error) {
	id, err := normalizeRequested(raw)
	if err != nil {
		return nil, err
	}
	s, err := e.loadFresh(id)
	if err != nil {
		return nil, e.repairHint(id, err)
	}
	if s.Journal.Exists {
		return nil, e.errPendingJournal(id)
	}
	if len(s.Plan.Duplicates) > 0 {
		paths := make([]string, len(s.Plan.Duplicates))
		for i, d := range s.Plan.Duplicates {
			if d.Path == "" {
				paths[i] = d.Key
			} else {
				paths[i] = d.Path + "." + d.Key
			}
		}
		return nil, &DuplicateMembersError{ID: id, Paths: paths}
	}
	if expected != nil && *expected != s.StateHash {
		return nil, &StaleHashError{Current: s.StateHash}
	}
	return s, nil
}

// readsOf records the exact state manifest as preconditions.
func readsOf(entries []snapshot.Entry) []storage.ReadEntry {
	out := make([]storage.ReadEntry, 0, len(entries))
	for _, en := range entries {
		out = append(out, storage.ReadEntry{Rel: en.Path, SHA256: en.SHA256, Missing: en.Missing})
	}
	return out
}

// fileMode returns the existing permission bits of a file. A new plan
// JSON or linked Markdown takes the mode of the existing primary plans in
// the workplan root, with owner read/write added, or 0644 minus the
// process umask when there is none. Every
// other new file (sidecars, archives) stays 0600.
func (e *Engine) fileMode(rel, kind string) fs.FileMode {
	if st, err := os.Stat(e.absRel(rel)); err == nil {
		return st.Mode().Perm()
	}
	if kind != "plan" && kind != "markdown" {
		return 0o600
	}
	return e.newArtifactMode()
}

// newArtifactMode is the mode for a new plan JSON or linked Markdown.
func (e *Engine) newArtifactMode() fs.FileMode {
	if l, err := e.scanDir(); err == nil {
		for _, name := range l.primary {
			st, err := os.Lstat(filepath.Join(e.dir(), name+".json"))
			if err == nil && st.Mode().IsRegular() {
				return st.Mode().Perm() | 0o600
			}
		}
	}
	return fs.FileMode(0o644 &^ processUmask)
}

// targetSpec is one prospective artifact change.
type targetSpec struct {
	rel        string
	kind       string
	before     []byte
	beforeOK   bool
	after      []byte
	afterOK    bool
	forceWrite bool // keep even when bytes are unchanged
}

// buildIntent assembles the intent; targets whose bytes do not change are
// dropped (no needless replacement).
func (e *Engine) buildIntent(op, id, tx string, specs []targetSpec, reads []storage.ReadEntry) *storage.Intent {
	in := &storage.Intent{
		Operation:      op,
		WorkplanID:     id,
		Root:           e.Root,
		TransactionID:  tx,
		CreatedAt:      e.nowISO(),
		Reads:          reads,
		JournalRel:     journalRel(id),
		JournalStage:   storage.JournalStagePath(snapshot.WorkplanDir, id, tx),
		JournalVersion: e.journalVersion(),
	}
	// Before images are the bytes the reads hashed.
	known := map[string]string{}
	for _, r := range reads {
		if !r.Missing {
			known[r.Rel] = r.SHA256
		}
	}
	var rels []string
	for _, t := range specs {
		if !t.forceWrite && t.beforeOK == t.afterOK && string(t.before) == string(t.after) {
			continue
		}
		tg := storage.Target{Rel: t.rel, Kind: t.kind, Before: t.before, BeforeExists: t.beforeOK, After: t.after, AfterExists: t.afterOK, Mode: e.fileMode(t.rel, t.kind)}
		if t.afterOK {
			tg.Stage = storage.StagePath(t.rel, tx, len(in.Targets))
		}
		if t.beforeOK && in.JournalVersion == 2 {
			tg.Backup = storage.BackupPath(t.rel, tx, len(in.Targets))
		}
		if sum, ok := known[t.rel]; ok && t.beforeOK {
			tg.SealKnown(sum)
			if verifyPostHashes && tg.BeforeHash() != hashOf(t.before) {
				panic("buildIntent: known before digest differs for " + t.rel)
			}
		} else {
			tg.Seal()
		}
		in.Targets = append(in.Targets, tg)
		rels = append(rels, t.rel)
	}
	if len(in.Targets) == 0 {
		return nil
	}
	in.Dirs = storage.ParentDirs(append(rels, snapshot.WorkplanDir+"/x")...)
	in.Locks = []storage.LockRef{
		storage.NewLockRef("workspace", snapshot.WorkplanDir+"/.workspace-mutation.lock", tx),
		storage.NewLockRef("plan", snapshot.WorkplanDir+"/."+id+".lock", tx),
	}
	return in
}

// overlay maps each target to its after-image (nil = absent) plus the
// journal as absent: the committed state.
func overlay(in *storage.Intent, id string) map[string][]byte {
	m := map[string][]byte{journalRel(id): nil}
	if in == nil {
		return m
	}
	for _, t := range in.Targets {
		if t.AfterExists {
			m[t.Rel] = t.After
		} else {
			m[t.Rel] = nil
		}
	}
	return m
}

// postHashes computes the plan and state hashes the committed intent
// produces without reading or parsing anything: every manifest path that
// is not a target was verified unchanged under the lock, so its digest is
// the prepared one, and targets carry their after digests. post is the
// plan as written. ok=false (a path the prepared manifest does not
// cover, such as a newly linked spec) means: use postSnapshot.
func (e *Engine) postHashes(pre *snapshot.Snapshot, in *storage.Intent, post *model.Plan) (planHash, stateHash string, ok bool) {
	known := map[string]snapshot.Entry{}
	for _, en := range pre.StateManifest {
		known[en.Path] = en
	}
	known[journalRel(pre.ID)] = snapshot.Entry{Path: journalRel(pre.ID), Missing: true}
	if in != nil {
		for _, t := range in.Targets {
			p := snapshot.ManifestPath(t.Rel)
			if t.AfterExists {
				known[p] = snapshot.Entry{Path: p, SHA256: t.AfterHash()}
			} else {
				known[p] = snapshot.Entry{Path: p, Missing: true}
			}
		}
	}
	pf, err := snapshot.NormalizePlanFile(e.Root, post.PlanFile)
	if err != nil {
		return "", "", false
	}
	paths := []string{snapshot.PlanRel(pre.ID), pf}
	for _, raw := range post.SpecFiles {
		sp, err := snapshot.NormalizeSpecFile(e.Root, raw)
		if err != nil {
			return "", "", false
		}
		paths = append(paths, sp)
	}
	seen := map[string]bool{}
	var planEntries []snapshot.Entry
	for _, p := range paths {
		mp := snapshot.ManifestPath(p)
		if seen[mp] {
			continue
		}
		seen[mp] = true
		en, hit := known[mp]
		if !hit {
			return "", "", false
		}
		planEntries = append(planEntries, en)
	}
	stateEntries := append([]snapshot.Entry{}, planEntries...)
	for _, suffix := range []string{".checkpoint.json", ".dependencies.json", ".transaction.json"} {
		en, hit := known[snapshot.SidecarRel(pre.ID, suffix)]
		if !hit {
			return "", "", false
		}
		stateEntries = append(stateEntries, en)
	}
	planHash = snapshot.ManifestHash(snapshot.PlanHashVersion, planEntries)
	stateHash = snapshot.ManifestHash(snapshot.StateHashVersion, stateEntries)
	if verifyPostHashes {
		if ps, err := e.postSnapshot(in, pre.ID); err != nil || ps.PlanHash != planHash || ps.StateHash != stateHash {
			panic(fmt.Sprintf("postHashes differ from the reloaded snapshot for %s: %v", pre.ID, err))
		}
	}
	return planHash, stateHash, true
}

// postSummary is the committed plan's summary and hashes: from the plan
// as written and the known digests, else from a reload.
func (e *Engine) postSummary(pre *snapshot.Snapshot, in *storage.Intent, post *model.Plan) (ojson.Value, string, string, error) {
	if ph, sh, ok := e.postHashes(pre, in, post); ok {
		return post.Summary(), ph, sh, nil
	}
	ps, err := e.postSnapshot(in, pre.ID)
	if err != nil {
		return ojson.Value{}, "", "", err
	}
	return ps.Plan.Summary(), ps.PlanHash, ps.StateHash, nil
}

// rememberRendered records that the Markdown an update just wrote is the
// rendering of the plan it wrote, so the next write skips re-rendering the
// stored plan to classify it.
func (e *Engine) rememberRendered(in *storage.Intent, jsonRel, mdRel string, p *model.Plan) {
	if e.Cache == nil || in == nil {
		return
	}
	var planSHA, mdSHA string
	for _, t := range in.Targets {
		switch t.Rel {
		case jsonRel:
			planSHA = t.AfterHash()
		case mdRel:
			mdSHA = t.AfterHash()
		}
	}
	if planSHA != "" && mdSHA != "" {
		e.Cache.SetGenerated(snapshot.GeneratedKey(planSHA, mdSHA, p.PlanFile, p.SpecFiles), true)
	}
}

// seedPlan puts the plan a write just serialized into the cache under the
// written bytes' digest, as decoding would yield it (decoding never sets
// SpecFilesAdded or keeps duplicate members), so the next read does not
// parse what was just written. Engine tests decode and compare.
func (e *Engine) seedPlan(in *storage.Intent, jsonRel string, p *model.Plan) {
	if e.Cache == nil || in == nil {
		return
	}
	for _, t := range in.Targets {
		if t.Rel != jsonRel || !t.AfterExists {
			continue
		}
		c := asDecoded(p)
		if verifyPostHashes {
			parsed, err := ojson.ParseImmutable(t.After)
			if err != nil {
				panic(err)
			}
			d, err := model.DecodePlan(parsed)
			if err != nil {
				panic(err)
			}
			if len(d.Duplicates) == 0 {
				d.Duplicates = nil
			}
			if !reflect.DeepEqual(c, d) {
				panic(fmt.Sprintf("seeded plan differs from decoding the written bytes for %s: %s", p.ID, planDiff(c, d)))
			}
		}
		e.Cache.SeedPlan(t.AfterHash(), len(t.After), c)
	}
}

// verifyPostHashes makes postHashes cross-check against a full reload
// (tests only).
var verifyPostHashes = false

// postSnapshot is the snapshot the committed intent produces.
func (e *Engine) postSnapshot(in *storage.Intent, id string) (*snapshot.Snapshot, error) {
	return snapshot.LoadOverlay(e.Root, id, e.Limits, overlay(in, id))
}

func dirSyncValue(ok bool) ojson.Value {
	if ok {
		return ojson.StringValue("supported")
	}
	return ojson.StringValue("unsupported")
}

// claimCheck enforces workspace linkage for new destinations: pending
// journals of other plans claim their targets (unparseable journals fail
// closed), and another plan's linked Markdown owns its path even while
// absent. It runs during preparation and again under the workspace lock.
func (e *Engine) claimCheck(id string, newRels []string, markdownRels []string) error {
	if len(newRels) == 0 {
		return nil
	}
	l, err := e.scanDir()
	if err != nil {
		return err
	}
	// Paths compare case-folded: on case-insensitive filesystems (APFS
	// default) differently cased links alias one file, so ambiguous
	// claims fail closed everywhere.
	want := map[string]bool{}
	for _, r := range newRels {
		want[strings.ToLower(r)] = true
	}
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	for _, sc := range l.sidecars {
		if sc.kind != kindTransaction {
			continue
		}
		owner := strings.TrimSuffix(sc.name, ".transaction.json")
		if owner == id {
			continue
		}
		rel := snapshot.WorkplanDir + "/" + sc.name
		a, err := r.ReadFile(rel)
		if err != nil || !a.Exists {
			return fmt.Errorf("Ambiguous pending workplan destination claim at %s", e.absRel(rel))
		}
		parsed, perr := ojson.Parse(a.Bytes)
		if perr != nil {
			return fmt.Errorf("Ambiguous pending workplan destination claim at %s", e.absRel(rel))
		}
		j, ok := model.DecodeJournal(parsed.Value)
		if !ok {
			return fmt.Errorf("Ambiguous pending workplan destination claim at %s", e.absRel(rel))
		}
		for _, t := range j.Targets {
			if want[strings.ToLower(t.Path)] {
				return fmt.Errorf("Workplan destination is claimed by pending transaction %s for %s", j.TransactionID, j.WorkplanID)
			}
		}
	}
	if len(markdownRels) == 0 {
		return nil
	}
	md := map[string]bool{}
	for _, m := range markdownRels {
		md[strings.ToLower(m)] = true
	}
	for _, name := range l.primary {
		other, err := model.NormalizeID(name)
		if err != nil || other == id || other != name {
			continue
		}
		_, p, err := r.LoadPlanDocument(other)
		if err != nil {
			continue
		}
		pf, err := snapshot.NormalizePlanFile(e.Root, p.PlanFile)
		if err != nil {
			continue
		}
		if md[strings.ToLower(pf)] {
			return fmt.Errorf("Plan file destination is already owned by %s: %s", other, e.absRel(pf))
		}
	}
	return nil
}

// newTargets lists the rels of targets absent before (new destinations).
func newTargets(in *storage.Intent) (all, markdown []string) {
	if in == nil {
		return nil, nil
	}
	for _, t := range in.Targets {
		if !t.BeforeExists && t.AfterExists {
			all = append(all, t.Rel)
			if t.Kind == "markdown" {
				markdown = append(markdown, t.Rel)
			}
		}
	}
	return all, markdown
}

// journalVersion is the journal format new writes use (default 2).
func (e *Engine) journalVersion() int {
	if e.JournalVersion == 1 {
		return 1
	}
	return 2
}

// planDiff names the first top-level field where two plans differ.
func planDiff(a, b *model.Plan) string {
	va, vb := reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem()
	for i := 0; i < va.NumField(); i++ {
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			return fmt.Sprintf("%s: %#v vs %#v", va.Type().Field(i).Name, va.Field(i).Interface(), vb.Field(i).Interface())
		}
	}
	return "no top-level field"
}

// asDecoded is a copy of p shaped as decoding its stored bytes yields it.
func asDecoded(p *model.Plan) *model.Plan {
	c := p.Clone()
	c.HasSpecFiles = c.HasSpecFiles || c.SpecFilesAdded
	c.SpecFilesAdded = false
	c.Duplicates = nil
	for _, l := range []*[]string{&c.Scope, &c.NonGoals, &c.Constraints, &c.RelevantFiles, &c.Notes} {
		if *l == nil {
			*l = []string{}
		}
	}
	// The decoder makes string lists non-nil and leaves empty phase,
	// step and finding lists nil.
	if c.SpecFiles == nil {
		c.SpecFiles = []string{}
	}
	if len(c.Findings) == 0 {
		c.Findings = nil
	}
	if len(c.Phases) == 0 {
		c.Phases = nil
	}
	for i := range c.Phases {
		if len(c.Phases[i].Steps) == 0 {
			c.Phases[i].Steps = nil
		}
	}
	return c
}
