package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// Explicit recovery: the journal is validated, and every
// current target preflighted against its before/after image, BEFORE
// authorization is requested; both are repeated under the locks. A
// journal-only plan (primary JSON never published) is recoverable;
// its expectedHash is the doctor's interrupted-state hash.

// JournalInvalidError is a journal that fails validation; nothing is
// written and the journal is preserved.
type JournalInvalidError struct {
	Path   string
	Detail string
}

func (e *JournalInvalidError) Error() string {
	return "Invalid workplan transaction journal at " + e.Path + ": " + e.Detail + "; journal evidence preserved"
}

// ExternalEditError is a third state found by the recovery preflight.
type ExternalEditError struct {
	TransactionID, JournalPath, Rel, Found string
}

func (e *ExternalEditError) Error() string {
	return "Workplan transaction " + e.TransactionID + " requires explicit recovery at " + e.JournalPath + ": External edit at " + e.Rel + "; expected before or after hash, found " + e.Found + "; journal evidence preserved"
}

var operationKinds = map[string]map[string]bool{
	"create":              {"plan": true, "markdown": true},
	"create:overwrite":    {"plan": true, "markdown": true, "archive": true}, // repair archive
	"update":              {"plan": true, "markdown": true, "dependencies": true, "evidence": true, "lanes": true, "links": true},
	"patch":               {"markdown": true},
	"reset:draft":         {"plan": true, "markdown": true, "checkpoint": true},
	"reset:wipe":          {"plan": true, "markdown": true, "checkpoint": true, "dependencies": true, "archive": true},
	"reset:markdown-only": {"markdown": true},
	"checkpoint":          {"checkpoint": true},
	"compact:apply":       {"plan": true, "markdown": true, "checkpoint": true, "dependencies": true, "archive": true},
}

// readJournal loads and decodes the pending journal of id.
func (e *Engine) readJournal(id string) ([]byte, *model.Journal, bool, error) {
	rel := journalRel(id)
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	a, err := r.ReadFile(rel)
	if err != nil || !a.Exists {
		return nil, nil, false, err
	}
	parsed, perr := ojson.Parse(a.Bytes)
	if perr != nil {
		return a.Bytes, nil, true, &JournalInvalidError{Path: a.Path, Detail: "not valid JSON: " + perr.Error()}
	}
	j, ok := model.DecodeJournal(parsed.Value)
	if !ok {
		return a.Bytes, nil, true, &JournalInvalidError{Path: a.Path, Detail: "does not match transaction-journal-v1 or v2 or its content hashes"}
	}
	if j.Version == 2 {
		if err := e.loadJournalImages(id, j); err != nil {
			return a.Bytes, nil, true, err
		}
	}
	return a.Bytes, j, true, nil
}

// loadJournalImages reads a v2 journal's images: each side from the target
// itself when it holds that image, else from the staged file or backup
// link the journal names (only the names Shiori derives are accepted). An
// image found nowhere stays nil and is reported when recovery needs it.
func (e *Engine) loadJournalImages(id string, j *model.Journal) error {
	bad := func(format string, a ...any) error {
		return &JournalInvalidError{Path: e.absRel(journalRel(id)), Detail: fmt.Sprintf(format, a...)}
	}
	if !safeTxID.MatchString(j.TransactionID) {
		return bad("transaction id %q is not one Shiori writes", j.TransactionID)
	}
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	read := func(rel string, h *string) []byte {
		if snapshot.CheckWritable(e.Root, rel) != nil {
			return nil
		}
		a, err := r.ReadFile(rel)
		if err != nil || !a.Exists || hashOf(a.Bytes) != *h {
			return nil
		}
		return a.Bytes
	}
	for i := range j.Targets {
		t := &j.Targets[i]
		if t.Stage != "" && t.Stage != storage.StagePath(t.Path, j.TransactionID, i) {
			return bad("target %s names staged file %s", t.Path, t.Stage)
		}
		if t.Backup != "" && t.Backup != storage.BackupPath(t.Path, j.TransactionID, i) {
			return bad("target %s names backup %s", t.Path, t.Backup)
		}
		if t.BeforeHash != nil {
			if t.Before = read(t.Path, t.BeforeHash); t.Before == nil {
				t.Before = read(t.Backup, t.BeforeHash)
			}
		}
		if t.AfterHash != nil {
			if t.After = read(t.Path, t.AfterHash); t.After == nil {
				t.After = read(t.Stage, t.AfterHash)
			}
		}
	}
	return nil
}

func planFileOf(root string, data []byte) (string, string, error) {
	parsed, err := ojson.Parse(data)
	if err != nil {
		return "", "", err
	}
	p, err := model.DecodePlan(parsed)
	if err != nil {
		return "", "", err
	}
	pf, err := snapshot.NormalizePlanFile(root, p.PlanFile)
	return p.ID, pf, err
}

// validateJournal proves the journal only touches this plan's own
// artifacts and that Markdown link changes are complete and exclusive.
func (e *Engine) validateJournal(id string, j *model.Journal) error {
	bad := func(format string, a ...any) error {
		return &JournalInvalidError{Path: e.absRel(journalRel(id)), Detail: fmt.Sprintf(format, a...)}
	}
	if j.WorkplanID != id {
		return bad("workplanId %s does not match %s", j.WorkplanID, id)
	}
	allowed, ok := operationKinds[j.Operation]
	if !ok {
		return bad("unsupported operation %q", j.Operation)
	}
	seen := map[string]bool{}
	kinds := make([]string, len(j.Targets))
	var primary *model.JournalTarget
	for i := range j.Targets {
		t := &j.Targets[i]
		if strings.Contains(t.Path, `\`) || snapshot.CheckWritable(e.Root, t.Path) != nil {
			return bad("target %s is not a canonical in-root path", t.Path)
		}
		key := strings.ToLower(t.Path) // fail closed on case-folding aliases
		if seen[key] {
			return bad("duplicate target %s", t.Path)
		}
		seen[key] = true
		dir := snapshot.WorkplanDir + "/"
		switch {
		case t.Path == snapshot.PlanRel(id):
			kinds[i] = "plan"
			primary = t
		case t.Path == snapshot.SidecarRel(id, ".checkpoint.json"):
			kinds[i] = "checkpoint"
		case t.Path == snapshot.SidecarRel(id, ".dependencies.json"):
			kinds[i] = "dependencies"
		case t.Path == evidenceRel(id):
			kinds[i] = "evidence"
			if t.AfterHash == nil {
				return bad("evidence target %s would be deleted", t.Path)
			}
		case t.Path == lanesRel(id):
			kinds[i] = "lanes"
			if t.AfterHash == nil {
				return bad("lanes target %s would be deleted", t.Path)
			}
		case t.Path == linksRel(id):
			kinds[i] = "links"
			if t.AfterHash == nil {
				return bad("links target %s would be deleted", t.Path)
			}
		case strings.HasPrefix(t.Path, dir+"archive/"+id+"/state-") && strings.HasSuffix(t.Path, ".json") && path.Dir(t.Path) == dir+"archive/"+id:
			kinds[i] = "archive"
			if t.Before != nil || t.BeforeHash != nil || t.AfterHash == nil {
				return bad("archive target %s must be a new, complete file", t.Path)
			}
			parsed, err := ojson.Parse(t.After)
			wid, _ := parsed.Value.Get("workplanId")
			av, _ := parsed.Value.Get("archiveVersion")
			if err != nil || wid.Str() != id || av.NumberLiteral() != "1" {
				return bad("archive target %s is not this plan's complete archive", t.Path)
			}
		case strings.HasPrefix(t.Path, dir) && strings.HasSuffix(t.Path, ".md"):
			kinds[i] = "markdown"
		default:
			return bad("target %s is not an artifact of workplan %s", t.Path, id)
		}
		if !allowed[kinds[i]] {
			return bad("operation %s cannot change %s target %s", j.Operation, kinds[i], t.Path)
		}
	}
	// Linked Markdown relationships.
	var beforePF, afterPF string
	if primary != nil {
		for _, img := range []struct {
			data []byte
			dst  *string
		}{{primary.Before, &beforePF}, {primary.After, &afterPF}} {
			if img.data == nil {
				continue
			}
			pid, pf, err := planFileOf(e.Root, img.data)
			if err != nil && j.Operation == "create:overwrite" && img.dst == &beforePF {
				// Overwrite repairs an unreadable plan, so its
				// before image need not decode (no link is derived from it).
				continue
			}
			if err != nil {
				return bad("primary plan image is invalid: %v", err)
			}
			if pid != id {
				return bad("primary plan image has id %s", pid)
			}
			*img.dst = pf
		}
		if primary.AfterHash == nil && !(j.Operation == "create" || j.Operation == "create:overwrite") {
			return bad("operation %s cannot delete the primary plan", j.Operation)
		}
		if strings.HasPrefix(j.Operation, "create") && j.Operation == "create" && primary.BeforeHash != nil {
			return bad("create journal overwrites an existing primary plan")
		}
	}
	if beforePF == "" || afterPF == "" {
		r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
		if a, _, err := r.LoadPlanDocument(id); err == nil {
			if _, pf, err := planFileOf(e.Root, a.Bytes); err == nil {
				if beforePF == "" && (primary == nil || primary.BeforeHash != nil) {
					beforePF = pf
				}
				if afterPF == "" && primary == nil {
					afterPF = pf
				}
			}
		}
		if afterPF == "" {
			afterPF = beforePF
		}
	}
	for i, t := range j.Targets {
		if kinds[i] != "markdown" {
			continue
		}
		if t.Path != beforePF && t.Path != afterPF {
			return bad("Markdown target %s is not the plan's linked planFile", t.Path)
		}
		if t.AfterHash == nil {
			return bad("Markdown target %s would be deleted", t.Path)
		}
	}
	if beforePF != "" && afterPF != "" && beforePF != afterPF {
		found := false
		for _, t := range j.Targets {
			if t.Path == beforePF {
				return bad("moved planFile keeps its old Markdown %s as a target", beforePF)
			}
			if t.Path == afterPF {
				if t.BeforeHash != nil || t.AfterHash == nil {
					return bad("moved planFile target %s must be new (absent before) and written", afterPF)
				}
				found = true
			}
		}
		if !found {
			return bad("moved planFile %s has no Markdown target", afterPF)
		}
	}
	return nil
}

// safeTxID limits staging cleanup to the transaction id shapes Shiori
// writes (UUIDs, hex), so a journal cannot name an arbitrary path.
var safeTxID = regexp.MustCompile(`^[0-9a-f-]{1,64}$`)

func hashOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// preflight checks every target is at its before or after image.
func (e *Engine) preflight(id string, j *model.Journal) error {
	for _, t := range j.Targets {
		data, err := os.ReadFile(e.absRel(t.Path))
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		matches := func(h *string) bool {
			if h == nil {
				return !exists
			}
			return exists && hashOf(data) == *h
		}
		if !matches(t.BeforeHash) && !matches(t.AfterHash) {
			found := "missing"
			if exists {
				found = hashOf(data)
			}
			return &ExternalEditError{TransactionID: j.TransactionID, JournalPath: e.absRel(journalRel(id)), Rel: t.Path, Found: found}
		}
	}
	return nil
}

// recoveryHashes computes planHash/stateHash, over an optional overlay,
// for a plan that may have no primary JSON.
func (e *Engine) recoveryHashes(id string, j *model.Journal, ov map[string][]byte) (string, string, error) {
	s, err := snapshot.LoadOverlay(e.Root, id, e.Limits, ov)
	if err == nil {
		return s.PlanHash, s.StateHash, nil
	}
	if _, nf := err.(*snapshot.NotFoundError); !nf {
		// An unreadable primary (for example the before image
		// of an overwrite that repairs it) has the raw-byte hashes doctor
		// reports for it.
		if IsUnsupported(err) {
			return "", "", err
		}
		u, uerr := snapshot.LoadUnreadableOverlay(e.Root, id, e.Limits, ov)
		if uerr != nil {
			return "", "", err
		}
		return u.PlanHash, u.StateHash, nil
	}
	var targets []string
	for _, t := range j.Targets {
		targets = append(targets, t.Path)
	}
	return snapshot.InterruptedHashes(e.Root, id, e.Limits, targets, ov)
}

// PrepareRecovery prepares update {recovery: resume|rollback}.
func (e *Engine) PrepareRecovery(rawID, mode string, expected *string) (*Prepared, error) {
	id, err := normalizeRequested(rawID)
	if err != nil {
		return nil, err
	}
	jbytes, j, present, err := e.readJournal(id)
	if err != nil {
		return nil, err
	}
	if !present {
		r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
		a, err := r.ReadFile(snapshot.PlanRel(id))
		if err != nil {
			return nil, err
		}
		if !a.Exists {
			return nil, &snapshot.NotFoundError{Path: a.Path}
		}
		return nil, fmt.Errorf("No pending transaction to %s for %s", mode, id)
	}
	if err := e.validateJournal(id, j); err != nil {
		return nil, err
	}
	_, current, err := e.recoveryHashes(id, j, nil)
	if err != nil {
		return nil, err
	}
	if expected != nil && *expected != current {
		return nil, &StaleHashError{Current: current}
	}
	if err := e.preflight(id, j); err != nil {
		return nil, err
	}
	nonce := storage.NewUUID()
	in := &storage.Intent{
		Operation:     "recovery:" + mode,
		WorkplanID:    id,
		Root:          e.Root,
		TransactionID: j.TransactionID,
		CreatedAt:     e.nowISO(),
		JournalRel:    journalRel(id),
		Recovery:      mode,
		JournalBytes:  jbytes,
		Locks: []storage.LockRef{
			storage.NewLockRef("workspace", snapshot.WorkplanDir+"/.workspace-mutation.lock", nonce),
			storage.NewLockRef("plan", snapshot.WorkplanDir+"/."+id+".lock", nonce),
		},
	}
	var rels []string
	for _, t := range j.Targets {
		img, imgHash := t.After, t.AfterHash
		if mode == "rollback" {
			img, imgHash = t.Before, t.BeforeHash
		}
		cur, err := os.ReadFile(e.absRel(t.Path))
		exists := err == nil
		if imgHash == nil && !exists || imgHash != nil && exists && hashOf(cur) == *imgHash {
			continue // already at the recovery image
		}
		if imgHash != nil && img == nil {
			side := map[string]string{"resume": "staged after", "rollback": "before"}[mode]
			return nil, fmt.Errorf("Workplan transaction %s cannot %s: the %s image of %s is missing or changed; journal evidence preserved", j.TransactionID, mode, side, t.Path)
		}
		tg := storage.Target{Rel: t.Path, Kind: "recovery", Before: cur, BeforeExists: exists, After: img, AfterExists: imgHash != nil, Mode: os.FileMode(t.Mode)}
		tg.Seal()
		if tg.AfterExists {
			tg.Stage = storage.StagePath(t.Path, nonce, len(in.Targets))
		}
		in.Targets = append(in.Targets, tg)
		rels = append(rels, t.Path)
	}
	// An interrupted writer (for example a killed process)
	// can leave its same-directory staging files behind. Their names are
	// derived from the journal's transaction id and target index, so no
	// other writer owns them; recovery removes the ones present.
	var stages []string
	if safeTxID.MatchString(j.TransactionID) {
		stages = append(stages, storage.JournalStagePath(snapshot.WorkplanDir, id, j.TransactionID))
	}
	for i, t := range j.Targets {
		if t.AfterHash != nil && safeTxID.MatchString(j.TransactionID) {
			stages = append(stages, storage.StagePath(t.Path, j.TransactionID, i))
		}
		if j.Version == 2 && t.BeforeHash != nil {
			stages = append(stages, storage.BackupPath(t.Path, j.TransactionID, i))
		}
	}
	for _, stage := range stages {
		if snapshot.CheckWritable(e.Root, stage) != nil {
			continue
		}
		data, err := os.ReadFile(e.absRel(stage))
		if err != nil {
			continue
		}
		in.Targets = append(in.Targets, storage.Target{Rel: stage, Kind: "staging", Before: data, BeforeExists: true})
		rels = append(rels, stage)
	}
	in.Dirs = storage.ParentDirs(append(rels, snapshot.WorkplanDir+"/x")...)
	prep := &Prepared{Tool: "workplan_update", Intent: in}
	prep.recheck = func() error {
		if err := e.validateJournal(id, j); err != nil {
			return err
		}
		return e.preflight(id, j)
	}
	prep.result = func(sync bool) (Output, error) {
		ov := map[string][]byte{journalRel(id): nil}
		for _, t := range in.Targets {
			if t.AfterExists {
				ov[t.Rel] = t.After
			} else {
				ov[t.Rel] = nil
			}
		}
		ph, sh, err := e.recoveryHashes(id, j, ov)
		if err != nil {
			return Output{}, err
		}
		return Output{Value: ojson.NewObject(7).
			Set("recovered", ojson.BoolValue(true)).
			Set("recovery", ojson.StringValue(mode)).
			Set("id", ojson.StringValue(id)).
			Set("transactionId", ojson.StringValue(j.TransactionID)).
			Set("planHash", ojson.StringValue(ph)).
			Set("stateHash", ojson.StringValue(sh)).
			Set("directorySync", dirSyncValue(sync)).Value()}, nil
	}
	pre, _ := e.loadFresh(id)
	return e.logged(prep, pre, nil), nil
}
