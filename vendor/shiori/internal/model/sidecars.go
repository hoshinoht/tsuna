package model

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"

	"github.com/hoshinoht/shiori/internal/ojson"
)

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Position is a checkpoint's recorded current step.
type Position struct {
	PhaseID, PhaseTitle, PhaseStatus string
	StepID, StepTitle, StepStatus    string
}

// ManifestEntry is one checkpoint manifest entry.
type ManifestEntry struct {
	Path    string
	SHA256  string
	Missing bool
}

// Checkpoint is a decoded checkpoint sidecar (v1 or v2). Unknown members
// are ignored (sidecars are replaced wholesale on write).
type Checkpoint struct {
	SchemaVersion    int
	ID               string
	SourceHash       string // v1
	PlanHash         string // v2
	Manifest         []ManifestEntry
	SourceUpdatedAt  string
	CreatedAt        string
	UpdatedAt        string
	Status           string
	Summary          string
	Current          *Position
	NextAction       string
	Blockers         []string
	RecentValidation []string
	Guardrails       []string
	References       []string
}

// ErrCheckpointSchema is the reference's union-failure text for a
// checkpoint that is JSON but matches neither version.
const ErrCheckpointSchema = ": Invalid input"

// DecodeCheckpoint decodes checkpoint.schema.json (v1 | v2). It returns
// false when the value matches neither version.
func DecodeCheckpoint(v ojson.Value) (*Checkpoint, bool) {
	var c checker
	ms, ok := c.object(fp{}, v)
	if !ok {
		return nil, false
	}
	f := fieldMap(ms)
	cp := &Checkpoint{}
	sv, _ := f.get("schemaVersion")
	switch fv, _ := sv.Float(); {
	case sv.Kind() == ojson.Number && fv == 1:
		cp.SchemaVersion = 1
		x, _ := f.get("sourceHash")
		cp.SourceHash, _ = c.str(fp{}, x)
		if !hashPattern.MatchString(cp.SourceHash) {
			return nil, false
		}
	case sv.Kind() == ojson.Number && fv == 2:
		cp.SchemaVersion = 2
		x, _ := f.get("planHash")
		cp.PlanHash, _ = c.str(fp{}, x)
		if !hashPattern.MatchString(cp.PlanHash) {
			return nil, false
		}
		x, _ = f.get("manifest")
		elems, ok := c.array(fp{}, x)
		if !ok {
			return nil, false
		}
		for _, e := range elems {
			em, ok := c.object(fp{}, e)
			if !ok {
				return nil, false
			}
			ef := fieldMap(em)
			var me ManifestEntry
			pv, _ := ef.get("path")
			me.Path, _ = c.str(fp{}, pv)
			if me.Path == "" {
				return nil, false
			}
			hv, hasHash := ef.get("sha256")
			mv, hasMissing := ef.get("missing")
			if hasHash == hasMissing {
				return nil, false
			}
			if hasHash {
				if hv.Kind() != ojson.String || !hashPattern.MatchString(hv.Str()) {
					return nil, false
				}
				me.SHA256 = hv.Str()
			} else {
				if mv.Kind() != ojson.Bool || !mv.Bool() {
					return nil, false
				}
				me.Missing = true
			}
			cp.Manifest = append(cp.Manifest, me)
		}
		x, _ = f.get("evidenceStatus")
		if x.Kind() != ojson.String || x.Str() != "unverified" {
			return nil, false
		}
	default:
		return nil, false
	}
	x, _ := f.get("id")
	cp.ID, _ = c.nonblank(fp{}, x)
	x, _ = f.get("sourceUpdatedAt")
	cp.SourceUpdatedAt, _ = c.str(fp{}, x)
	x, _ = f.get("createdAt")
	cp.CreatedAt, _ = c.str(fp{}, x)
	x, _ = f.get("updatedAt")
	cp.UpdatedAt, _ = c.str(fp{}, x)
	x, _ = f.get("status")
	cp.Status, _ = c.enum(fp{}, x, Statuses)
	x, _ = f.get("summary")
	cp.Summary, _ = c.str(fp{}, x)
	x, _ = f.get("current")
	if x.Kind() != ojson.Null {
		pm, ok := c.object(fp{}, x)
		if ok {
			pf := fieldMap(pm)
			pos := &Position{}
			y, _ := pf.get("phaseId")
			pos.PhaseID, _ = c.nonblank(fp{}, y)
			y, _ = pf.get("phaseTitle")
			pos.PhaseTitle, _ = c.str(fp{}, y)
			y, _ = pf.get("phaseStatus")
			pos.PhaseStatus, _ = c.enum(fp{}, y, Statuses)
			y, _ = pf.get("stepId")
			pos.StepID, _ = c.nonblank(fp{}, y)
			y, _ = pf.get("stepTitle")
			pos.StepTitle, _ = c.str(fp{}, y)
			y, _ = pf.get("stepStatus")
			pos.StepStatus, _ = c.enum(fp{}, y, Statuses)
			cp.Current = pos
		}
	}
	x, _ = f.get("nextAction")
	cp.NextAction, _ = c.str(fp{}, x)
	x, _ = f.get("blockers")
	cp.Blockers, _ = c.strList(fp{}, x)
	x, _ = f.get("recentValidation")
	cp.RecentValidation, _ = c.strList(fp{}, x)
	x, _ = f.get("guardrails")
	cp.Guardrails, _ = c.strList(fp{}, x)
	x, _ = f.get("references")
	cp.References, _ = c.strList(fp{}, x)
	if len(c.issues) > 0 {
		return nil, false
	}
	return cp, true
}

// StepRef is a composite (phaseId, stepId) reference as stored.
type StepRef struct {
	PhaseID string
	StepID  string
}

// DependencyEntry is one dependencies-v1 entry.
type DependencyEntry struct {
	StepRef
	DependsOn []StepRef
}

// TerminalSummary is an archived prerequisite summary.
type TerminalSummary struct {
	StepRef
	Title  string
	Status string
}

// Dependencies is a decoded dependency sidecar.
type Dependencies struct {
	ID                string
	UpdatedAt         string
	Entries           []DependencyEntry
	TerminalSummaries []TerminalSummary
}

// DecodeDependencies decodes dependencies-v1. On failure it returns the
// schema issues ("path: message", root "$").
func DecodeDependencies(v ojson.Value) (*Dependencies, []Issue) {
	var c checker
	ms, ok := c.object(fp{}, v)
	if !ok {
		return nil, c.issues
	}
	f := fieldMap(ms)
	d := &Dependencies{}
	x, _ := f.get("schemaVersion")
	c.literalNumber(at(nil, "schemaVersion"), x, 1)
	x, _ = f.get("id")
	d.ID, _ = c.nonblank(at(nil, "id"), x)
	x, _ = f.get("updatedAt")
	d.UpdatedAt, _ = c.str(at(nil, "updatedAt"), x)
	x, _ = f.get("dependencies")
	if elems, ok := c.array(at(nil, "dependencies"), x); ok {
		for i, e := range elems {
			p := idx(child(nil, "dependencies"), i)
			em, ok := c.object(fp{base: p}, e)
			if !ok {
				continue
			}
			ef := fieldMap(em)
			var de DependencyEntry
			de.StepRef = decodeRef(&c, p, ef)
			y, _ := ef.get("dependsOn")
			if deps, ok := c.array(at(p, "dependsOn"), y); ok {
				for j, dv := range deps {
					dp := idx(child(p, "dependsOn"), j)
					dm, ok := c.object(fp{base: dp}, dv)
					if !ok {
						continue
					}
					de.DependsOn = append(de.DependsOn, decodeRef(&c, dp, fieldMap(dm)))
				}
			}
			d.Entries = append(d.Entries, de)
		}
	}
	if x, present := f.get("terminalSummaries"); present {
		if elems, ok := c.array(at(nil, "terminalSummaries"), x); ok {
			for i, e := range elems {
				p := idx(child(nil, "terminalSummaries"), i)
				em, ok := c.object(fp{base: p}, e)
				if !ok {
					continue
				}
				ef := fieldMap(em)
				var ts TerminalSummary
				ts.StepRef = decodeRef(&c, p, ef)
				y, _ := ef.get("title")
				ts.Title, _ = c.str(at(p, "title"), y)
				y, _ = ef.get("status")
				ts.Status, _ = c.enum(at(p, "status"), y, []string{"completed", "cancelled"})
				d.TerminalSummaries = append(d.TerminalSummaries, ts)
			}
		}
	}
	if len(c.issues) > 0 {
		return nil, c.issues
	}
	return d, nil
}

func decodeRef(c *checker, path []string, f fields) StepRef {
	var r StepRef
	x, _ := f.get("phaseId")
	r.PhaseID, _ = c.nonblank(at(path, "phaseId"), x)
	x, _ = f.get("stepId")
	r.StepID, _ = c.nonblank(at(path, "stepId"), x)
	return r
}

// JournalTarget is one transaction journal target.
type JournalTarget struct {
	Path       string
	BeforeHash *string
	AfterHash  *string
	Before     []byte // nil when absent (v2: until loaded)
	After      []byte
	Mode       int
	// v2: where the images are kept until the transaction completes.
	Backup, Stage string
}

// Journal is a decoded transaction journal (v1 or v2). A v2 journal's
// images are loaded separately (they live in files next to the targets).
type Journal struct {
	Version       int
	TransactionID string
	WorkplanID    string
	Operation     string
	CreatedAt     string
	Targets       []JournalTarget
}

// DecodeJournal decodes and self-checks a journal (hashes equal the
// SHA-256 of the decoded content). It returns false on any failure.
func DecodeJournal(v ojson.Value) (*Journal, bool) {
	var c checker
	ms, ok := c.object(fp{}, v)
	if !ok {
		return nil, false
	}
	f := fieldMap(ms)
	j := &Journal{Version: 1}
	x, _ := f.get("schemaVersion")
	if sv, _ := x.Float(); x.Kind() == ojson.Number && sv == 2 {
		j.Version = 2
	} else if !c.literalNumber(fp{}, x, 1) {
		return nil, false
	}
	x, _ = f.get("transactionId")
	j.TransactionID, _ = c.nonblank(fp{}, x)
	x, _ = f.get("workplanId")
	j.WorkplanID, _ = c.nonblank(fp{}, x)
	x, _ = f.get("operation")
	j.Operation, _ = c.nonblank(fp{}, x)
	x, _ = f.get("createdAt")
	j.CreatedAt, _ = c.str(fp{}, x)
	x, _ = f.get("targets")
	elems, ok := c.array(fp{}, x)
	if !ok || len(c.issues) > 0 {
		return nil, false
	}
	for _, e := range elems {
		em, ok := c.object(fp{}, e)
		if !ok {
			return nil, false
		}
		ef := fieldMap(em)
		var t JournalTarget
		pv, _ := ef.get("path")
		if pv.Kind() != ojson.String || pv.Str() == "" {
			return nil, false
		}
		t.Path = pv.Str()
		var good bool
		if j.Version == 2 {
			if t.BeforeHash, t.Backup, good = hashRef(ef, "beforeHash", "beforeBackup"); !good {
				return nil, false
			}
			if t.AfterHash, t.Stage, good = hashRef(ef, "afterHash", "afterStage"); !good {
				return nil, false
			}
		} else {
			if t.BeforeHash, t.Before, good = hashContent(ef, "beforeHash", "beforeContent"); !good {
				return nil, false
			}
			if t.AfterHash, t.After, good = hashContent(ef, "afterHash", "afterContent"); !good {
				return nil, false
			}
		}
		mv, _ := ef.get("mode")
		mf, ok := mv.Float()
		if !ok || mf != float64(int(mf)) || mf < 0 || mf > 511 {
			return nil, false
		}
		t.Mode = int(mf)
		j.Targets = append(j.Targets, t)
	}
	return j, true
}

func hashContent(f fields, hashKey, contentKey string) (*string, []byte, bool) {
	hv, ok1 := f.get(hashKey)
	cv, ok2 := f.get(contentKey)
	if !ok1 || !ok2 {
		return nil, nil, false
	}
	if hv.Kind() == ojson.Null && cv.Kind() == ojson.Null {
		return nil, nil, true
	}
	if hv.Kind() != ojson.String || !hashPattern.MatchString(hv.Str()) || cv.Kind() != ojson.String {
		return nil, nil, false
	}
	data, err := base64.StdEncoding.DecodeString(cv.Str())
	if err != nil {
		return nil, nil, false
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hv.Str() {
		return nil, nil, false
	}
	h := hv.Str()
	return &h, data, true
}

// hashRef decodes a v2 image: a hash and the file that holds it, both
// null when the image is absent.
func hashRef(f fields, hashKey, refKey string) (*string, string, bool) {
	hv, ok1 := f.get(hashKey)
	rv, ok2 := f.get(refKey)
	if !ok1 || !ok2 {
		return nil, "", false
	}
	if hv.Kind() == ojson.Null && rv.Kind() == ojson.Null {
		return nil, "", true
	}
	if hv.Kind() != ojson.String || !hashPattern.MatchString(hv.Str()) || rv.Kind() != ojson.String || rv.Str() == "" {
		return nil, "", false
	}
	h := hv.Str()
	return &h, rv.Str(), true
}

// LockOwner is lock-owner-v1 metadata.
type LockOwner struct {
	Hostname  string
	PID       int64
	Nonce     string
	StartedAt string
}

// DecodeLockOwner decodes lock-owner-v1.
func DecodeLockOwner(v ojson.Value) (*LockOwner, bool) {
	var c checker
	ms, ok := c.object(fp{}, v)
	if !ok {
		return nil, false
	}
	f := fieldMap(ms)
	lo := &LockOwner{}
	x, _ := f.get("hostname")
	lo.Hostname, _ = c.str(fp{}, x)
	x, _ = f.get("pid")
	pf, ok := x.Float()
	if !ok || pf < 1 || pf != float64(int64(pf)) {
		return nil, false
	}
	lo.PID = int64(pf)
	x, _ = f.get("nonce")
	lo.Nonce, _ = c.str(fp{}, x)
	x, _ = f.get("startedAt")
	lo.StartedAt, _ = c.str(fp{}, x)
	if len(c.issues) > 0 || lo.Hostname == "" || lo.Nonce == "" || !ValidDatetime(lo.StartedAt) {
		return nil, false
	}
	return lo, true
}
