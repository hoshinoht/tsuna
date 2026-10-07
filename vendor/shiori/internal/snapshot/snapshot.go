package snapshot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Limits. They are configurable by the trusted caller
// only, never by model input.
type Limits struct {
	MaxArtifactBytes int64
	MaxSnapshotBytes int64
	MaxSpecFiles     int
}

// DefaultLimits are the approved defaults.
var DefaultLimits = Limits{
	MaxArtifactBytes: 64 << 20,
	MaxSnapshotBytes: 256 << 20,
	MaxSpecFiles:     1024,
}

// ErrUnsupported marks a request that exceeds a configured capability.
var ErrUnsupported = errors.New("unsupported_capability")

// Artifact is one file read exactly once.
type Artifact struct {
	Rel    string // project-relative path ('/' separators, as stored)
	Path   string // absolute path
	Bytes  []byte
	Exists bool
	SHA256 string // digest of Bytes when already computed
}

// entry is the manifest entry of an artifact under a manifest path.
func (a Artifact) entry(path string) Entry {
	if a.Exists && a.SHA256 != "" {
		return Entry{Path: path, SHA256: a.SHA256}
	}
	return EntryFor(path, a.Bytes, a.Exists)
}

// Snapshot is the complete, byte-exact artifact set of one plan.
type Snapshot struct {
	Root string
	ID   string // normalized id used for the file names

	JSON         Artifact
	Plan         *model.Plan // decoded with normalized planFile/specFiles
	Markdown     Artifact
	Specs        []Artifact
	Checkpoint   Artifact
	Dependencies Artifact
	Journal      Artifact

	PlanManifest  []Entry
	StateManifest []Entry
	PlanHash      string
	StateHash     string

	// MissingPlanArtifacts lists linked Markdown/spec paths that are absent.
	MissingPlanArtifacts []string
	// BackslashPaths lists linked paths containing '\'.
	BackslashPaths []string
}

// PlanRel is the primary JSON path for an id.
func PlanRel(id string) string { return WorkplanDir + "/" + id + ".json" }

// SidecarRel is a sidecar path for an id and suffix such as ".checkpoint.json".
func SidecarRel(id, suffix string) string { return WorkplanDir + "/" + id + suffix }

// Reader reads artifacts under a canonical root. It only opens files
// read-only and never creates, renames or touches anything.
type Reader struct {
	Root   string
	Limits Limits
	// Overlay substitutes prospective contents (nil = absent) for paths,
	// so a writer can compute post-commit hashes without touching disk.
	Overlay map[string][]byte
	// Cache, when set, supplies decoded plans by content digest and, with
	// TrustStat (reads only), unchanged files by stat identity.
	Cache     *Cache
	TrustStat bool
	total     int64
}

func (r *Reader) read(rel string) (Artifact, error) {
	a := Artifact{Rel: rel, Path: abs(r.Root, rel)}
	if data, ok := r.Overlay[rel]; ok {
		if data != nil {
			a.Bytes, a.Exists = data, true
		}
		return a, nil
	}
	f, err := os.Open(a.Path)
	if err != nil {
		// ENOENT, or ENOTDIR when a path component is a file, is "missing".
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return a, nil
		}
		return a, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return a, err
	}
	if st.IsDir() {
		return a, fmt.Errorf("Artifact is a directory: %s", a.Path)
	}
	if st.Size() > r.Limits.MaxArtifactBytes {
		return a, fmt.Errorf("%w: artifact %s is %d bytes, above the %d-byte read limit", ErrUnsupported, a.Path, st.Size(), r.Limits.MaxArtifactBytes)
	}
	r.total += st.Size()
	if r.total > r.Limits.MaxSnapshotBytes {
		return a, fmt.Errorf("%w: snapshot exceeds the %d-byte limit", ErrUnsupported, r.Limits.MaxSnapshotBytes)
	}
	if r.Cache != nil && r.TrustStat {
		if data, sha, ok := r.Cache.file(a.Path, st); ok {
			a.Bytes, a.SHA256, a.Exists = data, sha, true
			return a, nil
		}
	}
	// Read into a buffer sized from Stat (one allocation); a file that grew
	// meanwhile is still bounded by the limit.
	data := make([]byte, 0, st.Size()+1)
	for {
		if int64(len(data)) > r.Limits.MaxArtifactBytes {
			return a, fmt.Errorf("%w: artifact %s exceeds the read limit", ErrUnsupported, a.Path)
		}
		if len(data) == cap(data) {
			data = append(data, 0)[:len(data)]
		}
		n, err := f.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if err == io.EOF {
			break
		}
		if err != nil {
			return a, err
		}
	}
	a.Bytes = data
	a.Exists = true
	if r.Cache != nil {
		a.SHA256 = SHA256Hex(data)
		// Only a file that did not change while it was read is cached.
		if after, err := f.Stat(); err == nil && after.Size() == st.Size() && after.ModTime().Equal(st.ModTime()) {
			r.Cache.putFile(a.Path, st, data, a.SHA256)
		}
	}
	return a, nil
}

// ReadFile reads one project-relative artifact (exported for doctor/list).
func (r *Reader) ReadFile(rel string) (Artifact, error) { return r.read(rel) }

// NotFoundError is the reference "Workplan file not found" error.
type NotFoundError struct{ Path string }

func (e *NotFoundError) Error() string { return "Workplan file not found: " + e.Path }

// InvalidJSONError wraps a parse failure with the stable reference prefix.
type InvalidJSONError struct {
	Path string
	Err  error
}

func (e *InvalidJSONError) Error() string {
	return "Invalid workplan JSON at " + e.Path + ": " + e.Err.Error()
}

// LoadPlanDocument reads and decodes only the primary JSON.
func (r *Reader) LoadPlanDocument(id string) (Artifact, *model.Plan, error) {
	a, err := r.read(PlanRel(id))
	if err != nil {
		return a, nil, err
	}
	if !a.Exists {
		return a, nil, &NotFoundError{Path: a.Path}
	}
	if r.Cache != nil && a.SHA256 != "" {
		if p := r.Cache.plan(a.SHA256); p != nil {
			return a, p, nil
		}
	}
	parsed, err := ojson.ParseImmutable(a.Bytes)
	if err != nil {
		return a, nil, &InvalidJSONError{Path: a.Path, Err: err}
	}
	p, err := model.DecodePlan(parsed)
	if err != nil {
		return a, nil, err
	}
	if r.Cache != nil && a.SHA256 != "" {
		r.Cache.putPlan(a.SHA256, len(a.Bytes), p)
	}
	return a, p, nil
}

// Load reads the complete artifact set for a normalized id.
func Load(root, id string, limits Limits) (*Snapshot, error) {
	return LoadOverlay(root, id, limits, nil)
}

// LoadOverlay is Load with prospective contents substituted (see
// Reader.Overlay). A nil slice value marks the path absent.
func LoadOverlay(root, id string, limits Limits, overlay map[string][]byte) (*Snapshot, error) {
	return LoadWith(&Reader{Root: root, Limits: limits, Overlay: overlay}, id)
}

// LoadWith is Load through a configured reader (cache, overlay).
func LoadWith(r *Reader, id string) (*Snapshot, error) {
	root := r.Root
	limits := r.Limits
	s := &Snapshot{Root: root, ID: id}
	var err error
	s.JSON, s.Plan, err = r.LoadPlanDocument(id)
	if err != nil {
		return nil, err
	}
	// Normalize a shallow copy: a decoded plan may be shared via the cache.
	cp := *s.Plan
	s.Plan = &cp
	p := s.Plan
	if p.PlanFile, err = NormalizePlanFile(root, p.PlanFile); err != nil {
		return nil, err
	}
	if len(p.SpecFiles) > limits.MaxSpecFiles {
		return nil, fmt.Errorf("%w: %d linked spec files exceed the limit of %d", ErrUnsupported, len(p.SpecFiles), limits.MaxSpecFiles)
	}
	specs := make([]string, len(p.SpecFiles))
	for i, raw := range p.SpecFiles {
		if specs[i], err = NormalizeSpecFile(root, raw); err != nil {
			return nil, err
		}
	}
	p.SpecFiles = specs

	if !withinRoot(root, abs(root, p.PlanFile)) {
		return nil, fmt.Errorf("Plan file must stay inside workspace root: %s", p.PlanFile)
	}
	if s.Markdown, err = r.read(p.PlanFile); err != nil {
		return nil, err
	}
	seen := map[string]bool{ManifestPath(s.JSON.Rel): true, ManifestPath(p.PlanFile): true}
	planEntries := []Entry{
		s.JSON.entry(ManifestPath(s.JSON.Rel)),
		s.Markdown.entry(ManifestPath(p.PlanFile)),
	}
	if !s.Markdown.Exists {
		s.MissingPlanArtifacts = append(s.MissingPlanArtifacts, p.PlanFile)
	}
	if containsBackslash(p.PlanFile) {
		s.BackslashPaths = append(s.BackslashPaths, p.PlanFile)
	}
	for _, rel := range specs {
		if !withinRoot(root, abs(root, rel)) {
			return nil, fmt.Errorf("Spec file must stay inside workspace root: %s", rel)
		}
		a, err := r.read(rel)
		if err != nil {
			return nil, err
		}
		s.Specs = append(s.Specs, a)
		if containsBackslash(rel) {
			s.BackslashPaths = append(s.BackslashPaths, rel)
		}
		mp := ManifestPath(rel)
		if seen[mp] {
			continue
		}
		seen[mp] = true
		planEntries = append(planEntries, a.entry(mp))
		if !a.Exists {
			s.MissingPlanArtifacts = append(s.MissingPlanArtifacts, rel)
		}
	}
	for _, sc := range []struct {
		dst    *Artifact
		suffix string
	}{{&s.Checkpoint, ".checkpoint.json"}, {&s.Dependencies, ".dependencies.json"}, {&s.Journal, ".transaction.json"}} {
		if *sc.dst, err = r.read(SidecarRel(id, sc.suffix)); err != nil {
			return nil, err
		}
	}
	s.PlanManifest = SortEntries(planEntries)
	stateEntries := append([]Entry{}, planEntries...)
	for _, a := range []Artifact{s.Checkpoint, s.Dependencies, s.Journal} {
		stateEntries = append(stateEntries, a.entry(a.Rel))
	}
	s.StateManifest = SortEntries(stateEntries)
	s.PlanHash = ManifestHash(PlanHashVersion, planEntries)
	s.StateHash = ManifestHash(StateHashVersion, stateEntries)
	return s, nil
}

// InterruptedStateHash computes the read-only state hash of a plan whose
// primary JSON is absent but whose journal is pending: the
// primary is recorded as missing and only the journal's own targets and
// the id's sidecars contribute.
func InterruptedStateHash(root, id string, limits Limits, journalTargets []string) (string, error) {
	_, sh, err := InterruptedHashes(root, id, limits, journalTargets, nil)
	return sh, err
}

// InterruptedHashes returns the interrupted plan and state hashes of a
// journal-only plan (optionally over an overlay): the plan manifest is the
// primary JSON plus the journal's Markdown targets; the state manifest adds
// every in-root journal target and the id's sidecars.
func InterruptedHashes(root, id string, limits Limits, journalTargets []string, overlay map[string][]byte) (string, string, error) {
	r := &Reader{Root: root, Limits: limits, Overlay: overlay}
	var planEntries []Entry
	primary, err := r.read(PlanRel(id))
	if err != nil {
		return "", "", err
	}
	planEntries = append(planEntries, EntryFor(ManifestPath(primary.Rel), primary.Bytes, primary.Exists))
	for _, t := range journalTargets {
		if !strings.HasSuffix(t, ".md") {
			continue
		}
		if _, ok := relInRoot(root, t); !ok {
			continue
		}
		a, err := r.read(t)
		if err != nil {
			return "", "", err
		}
		planEntries = append(planEntries, EntryFor(ManifestPath(t), a.Bytes, a.Exists))
	}
	sh, err := interruptedState(r, root, id, journalTargets)
	if err != nil {
		return "", "", err
	}
	return ManifestHash(PlanHashVersion, planEntries), sh, nil
}

func interruptedState(r *Reader, root, id string, journalTargets []string) (string, error) {
	entries := []Entry{}
	seen := map[string]bool{}
	add := func(rel string) error {
		mp := ManifestPath(rel)
		if seen[mp] {
			return nil
		}
		seen[mp] = true
		a, err := r.read(rel)
		if err != nil {
			return err
		}
		entries = append(entries, EntryFor(mp, a.Bytes, a.Exists))
		return nil
	}
	if err := add(PlanRel(id)); err != nil {
		return "", err
	}
	for _, t := range journalTargets {
		if _, ok := relInRoot(root, t); !ok {
			continue
		}
		if err := add(t); err != nil {
			return "", err
		}
	}
	for _, suffix := range []string{".checkpoint.json", ".dependencies.json", ".transaction.json"} {
		if err := add(SidecarRel(id, suffix)); err != nil {
			return "", err
		}
	}
	return ManifestHash(StateHashVersion, entries), nil
}

func containsBackslash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			return true
		}
	}
	return false
}

// DirExists reports whether the workplan directory exists.
func DirExists(root string) bool {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(WorkplanDir)))
	return err == nil && st.IsDir()
}

// Unreadable is the raw-byte view of a plan whose primary JSON exists but
// cannot be loaded (unparseable, wrong shape or an invalid link). Its
// hashes cover the exact primary bytes and the id's sidecars, so a
// read-only report can hand out an expectedHash for an explicit repair.
// The linked Markdown and specs are unknown
// and not part of the manifest.
type Unreadable struct {
	JSON          Artifact
	Checkpoint    Artifact
	Dependencies  Artifact
	Journal       Artifact
	StateManifest []Entry
	PlanHash      string
	StateHash     string
}

// LoadUnreadable reads the raw artifacts of an unreadable plan. It fails
// when the primary JSON is absent.
func LoadUnreadable(root, id string, limits Limits) (*Unreadable, error) {
	return LoadUnreadableOverlay(root, id, limits, nil)
}

// LoadUnreadableOverlay is LoadUnreadable over prospective contents (see
// Reader.Overlay).
func LoadUnreadableOverlay(root, id string, limits Limits, overlay map[string][]byte) (*Unreadable, error) {
	r := &Reader{Root: root, Limits: limits, Overlay: overlay}
	u := &Unreadable{}
	var err error
	if u.JSON, err = r.read(PlanRel(id)); err != nil {
		return nil, err
	}
	if !u.JSON.Exists {
		return nil, &NotFoundError{Path: u.JSON.Path}
	}
	for _, sc := range []struct {
		dst    *Artifact
		suffix string
	}{{&u.Checkpoint, ".checkpoint.json"}, {&u.Dependencies, ".dependencies.json"}, {&u.Journal, ".transaction.json"}} {
		if *sc.dst, err = r.read(SidecarRel(id, sc.suffix)); err != nil {
			return nil, err
		}
	}
	planEntries := []Entry{EntryFor(ManifestPath(u.JSON.Rel), u.JSON.Bytes, true)}
	state := append([]Entry{}, planEntries...)
	for _, a := range []Artifact{u.Checkpoint, u.Dependencies, u.Journal} {
		state = append(state, EntryFor(a.Rel, a.Bytes, a.Exists))
	}
	u.StateManifest = SortEntries(state)
	u.PlanHash = ManifestHash(PlanHashVersion, planEntries)
	u.StateHash = ManifestHash(StateHashVersion, state)
	return u, nil
}
