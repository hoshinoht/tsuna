package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"
)

// Fault injection points (tests only). A Hooks.Fault returning an error
// at a point simulates a failure there.
const (
	FaultDirs         = "dirs"
	FaultLocked       = "locked"
	FaultStage        = "stage"           // + ":<index>"
	FaultBackup       = "backup"          // journal v2: before images linked, not synced
	FaultJournalStage = "journal-stage"   // journal staged, not published
	FaultJournalLink  = "journal-publish" // journal published, before dir sync
	FaultJournalSync  = "journal-sync"
	FaultPublish      = "publish" // + ":<index>", before that publication
	FaultDirSync      = "dir-sync"
	FaultCleanup      = "cleanup" // before the journal is removed
	FaultRelease      = "release"
)

// Hooks are the commit's collaborators.
type Hooks struct {
	// Recheck runs under both locks and must reject stale or conflicting
	// state (claims, ownership). Storage itself rechecks Reads and every
	// target's before-image.
	Recheck func() error
	// Fault is the test-only fault injector.
	Fault func(point string) error
	Lock  LockConfig
	Now   func() time.Time
	// AfterLock is a test-only barrier invoked while holding both locks.
	AfterLock func()
}

func (h Hooks) fault(p string) error {
	if h.Fault == nil {
		return nil
	}
	return h.Fault(p)
}

// Result is a committed mutation's durability report.
type Result struct {
	// DirectorySync is true when every directory fsync succeeded; false
	// means the platform refused directory sync (reported, not hidden).
	DirectorySync bool
	// HistoryErr is a failed change-log append (the commit stands).
	HistoryErr error
}

// StaleError is a precondition that changed between preparation and the
// locked recheck.
type StaleError struct{ Message string }

func (e *StaleError) Error() string { return e.Message }

// CancelledError is cancellation before any durable change.
type CancelledError struct{ Stage string }

func (e *CancelledError) Error() string {
	return "Workplan mutation cancelled before " + e.Stage + "; no artifact was changed"
}

// RecoveryRequiredError is a failure after the journal was published. The
// artifacts may be the old, the new, or a mixed generation; the journal is
// preserved and explicit recovery (update {recovery}) is required.
type RecoveryRequiredError struct {
	TransactionID string
	JournalPath   string // absolute
	Cause         error
	Uncertain     bool // cancellation/transport: outcome uncertain
}

func (e *RecoveryRequiredError) Error() string {
	return "Workplan transaction " + e.TransactionID + " requires explicit recovery at " + e.JournalPath + ": " + e.Cause.Error()
}

func (e *RecoveryRequiredError) Unwrap() error { return e.Cause }

// fileDigestStat is fileDigest plus the stat of the file it read.
func fileDigestStat(p string) (string, bool, fs.FileInfo, error) {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return "", false, nil, nil
		}
		return "", false, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false, nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, nil, err
	}
	return hex.EncodeToString(h.Sum(nil)), true, info, nil
}

func fileDigest(p string) (string, bool, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return "", false, nil
		}
		return "", false, err
	}
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:]), true, nil
}

// Commit executes exactly the prepared intent: directories, workspace
// lock then plan lock, locked recheck, exclusive same-directory staging
// with fsync, durable journal, atomic publication, directory sync, journal
// removal and lock release. Recovery intents reuse the same engine and
// remove the existing journal last instead of writing a new one.
func Commit(ctx context.Context, in *Intent, h Hooks) (res Result, err error) {
	if h.Now == nil {
		h.Now = time.Now
	}
	h.Lock = h.Lock.withDefaults()
	root := in.Root
	if err := ctx.Err(); err != nil {
		return res, &CancelledError{Stage: "locking"}
	}

	// Directories (parents first). They are never removed again: removing
	// an empty directory could race another writer creating its lock there.
	for _, d := range in.Dirs {
		p := abs(root, d)
		if st, err := os.Lstat(p); err == nil {
			if !st.IsDir() {
				return res, fmt.Errorf("Workplan path is not a directory: %s", p)
			}
			continue
		}
		if err := os.Mkdir(p, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return res, err
		}
	}
	if err := h.fault(FaultDirs); err != nil {
		return res, err
	}

	// Locks in deterministic order: workspace linkage, then plan.
	owner := NewOwner(h.Now())
	var held []*heldLock
	releaseAll := func() error {
		var first error
		for i := len(held) - 1; i >= 0; i-- {
			if err := held[i].release(root); err != nil && first == nil {
				first = err
			}
		}
		held = nil
		return first
	}
	for _, ref := range in.Locks {
		hl, err := acquire(ctx, root, ref, owner, h.Lock)
		if err != nil {
			releaseAll()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return res, &CancelledError{Stage: "locking"}
			}
			return res, err
		}
		held = append(held, hl)
	}
	defer func() {
		rerr := releaseAll()
		if rerr != nil && err == nil {
			// The mutation is complete; a lock replacement is reported
			// but does not undo it.
			err = rerr
		}
	}()
	if err := h.fault(FaultLocked); err != nil {
		return res, err
	}
	if h.AfterLock != nil {
		h.AfterLock()
	}

	// Locked recheck of every precondition; each file is hashed once.
	type digest struct {
		sum  string
		ok   bool
		info fs.FileInfo
	}
	seen := map[string]digest{}
	digestOf := func(rel string) (string, bool, error) {
		if d, hit := seen[rel]; hit {
			return d.sum, d.ok, nil
		}
		sum, ok, info, err := fileDigestStat(abs(root, rel))
		if err == nil {
			seen[rel] = digest{sum, ok, info}
		}
		return sum, ok, err
	}
	for _, e := range in.Reads {
		sum, ok, err := digestOf(e.Rel)
		if err != nil {
			return res, err
		}
		if ok && e.Missing {
			return res, &StaleError{Message: "Refusing to overwrite existing workplan artifact: " + abs(root, e.Rel)}
		}
		if ok == e.Missing || (ok && sum != e.SHA256) {
			return res, &StaleError{Message: "Workplan state changed after preparation: " + abs(root, e.Rel) + ". Reread the plan and recompute the mutation."}
		}
	}
	for _, t := range in.Targets {
		if in.Recovery != "" {
			break // recovery preflight is the caller's Recheck
		}
		sum, ok, err := digestOf(t.Rel)
		if err != nil {
			return res, err
		}
		if ok != t.BeforeExists || (ok && sum != t.BeforeHash()) {
			if !t.BeforeExists {
				return res, &StaleError{Message: "Refusing to overwrite existing workplan artifact: " + abs(root, t.Rel)}
			}
			return res, &StaleError{Message: "Workplan state changed after preparation: " + abs(root, t.Rel) + ". Reread the plan and recompute the mutation."}
		}
	}
	if in.Recovery == "" {
		if _, ok, err := fileDigest(abs(root, in.JournalRel)); err != nil {
			return res, err
		} else if ok {
			return res, &StaleError{Message: "Workplan transaction pending requires explicit recovery at " + abs(root, in.JournalRel)}
		}
	} else {
		data, err := os.ReadFile(abs(root, in.JournalRel))
		if err != nil || string(data) != string(in.JournalBytes) {
			return res, &StaleError{Message: "Pending journal changed after preparation: " + abs(root, in.JournalRel)}
		}
	}
	if h.Recheck != nil {
		if err := h.Recheck(); err != nil {
			return res, err
		}
	}
	if err := ctx.Err(); err != nil {
		return res, &CancelledError{Stage: "staging"}
	}

	// Exclusive same-directory staging.
	var staged []string
	cleanupStage := func() {
		for _, s := range staged {
			os.Remove(s)
		}
	}
	for i, t := range in.Targets {
		if err := h.fault(fmt.Sprintf("%s:%d", FaultStage, i)); err != nil {
			cleanupStage()
			return res, err
		}
		if !t.AfterExists {
			continue
		}
		sp := abs(root, t.Stage)
		if err := writeExclusive(sp, t.After, t.Mode.Perm()); err != nil {
			cleanupStage()
			return res, err
		}
		staged = append(staged, sp)
	}

	jp := abs(root, in.JournalRel)
	v2 := in.JournalVersion == 2 && in.Recovery == ""
	var backups []string
	removeBackups := func() {
		for _, b := range backups {
			os.Remove(b)
		}
	}
	fail := func(cause error, uncertain bool) (Result, error) {
		// A v2 journal names the staged files and backups as its images.
		if !v2 {
			cleanupStage()
		}
		return res, &RecoveryRequiredError{TransactionID: in.TransactionID, JournalPath: jp, Cause: cause, Uncertain: uncertain}
	}
	syncOK := true
	syncDir := func(dir string) error {
		if err := fsyncDir(dir); err != nil {
			if isSyncUnsupported(err) {
				syncOK = false
				return nil
			}
			return err
		}
		return nil
	}

	// Journal v2: hard-link each before image to its backup name, check
	// it is still the prepared before image, and make the staged files
	// and links durable before the journal names them.
	if v2 {
		abort := func(err error) (Result, error) {
			removeBackups()
			cleanupStage()
			return res, err
		}
		dirs := map[string]bool{}
		for _, t := range in.Targets {
			if t.Stage != "" {
				dirs[filepath.Dir(abs(root, t.Stage))] = true
			}
			if t.Backup == "" {
				continue
			}
			bp := abs(root, t.Backup)
			if err := os.Link(abs(root, t.Rel), bp); err != nil {
				return abort(err)
			}
			backups = append(backups, bp)
			dirs[filepath.Dir(bp)] = true
			// The link must be the file the locked recheck hashed.
			if st, err := os.Stat(bp); err != nil || seen[t.Rel].info == nil || !os.SameFile(st, seen[t.Rel].info) ||
				st.Size() != seen[t.Rel].info.Size() || !st.ModTime().Equal(seen[t.Rel].info.ModTime()) {
				return abort(&StaleError{Message: "Workplan state changed after preparation: " + abs(root, t.Rel) + ". Reread the plan and recompute the mutation."})
			}
		}
		if err := h.fault(FaultBackup); err != nil {
			return abort(err)
		}
		for d := range dirs {
			if err := syncDir(d); err != nil {
				return abort(err)
			}
		}
	}

	// Durable journal before the first replacement.
	if in.Recovery == "" {
		js := abs(root, in.JournalStage)
		if err := writeExclusive(js, EncodeJournal(in), 0o600); err != nil {
			removeBackups()
			cleanupStage()
			return res, err
		}
		if err := h.fault(FaultJournalStage); err != nil {
			os.Remove(js)
			removeBackups()
			cleanupStage()
			return res, err
		}
		if err := os.Link(js, jp); err != nil {
			os.Remove(js)
			removeBackups()
			cleanupStage()
			if errors.Is(err, fs.ErrExist) {
				return res, &StaleError{Message: "Workplan transaction pending requires explicit recovery at " + jp}
			}
			return res, err
		}
		os.Remove(js)
		if err := h.fault(FaultJournalLink); err != nil {
			return fail(err, false)
		}
		if err := syncDir(filepath.Dir(jp)); err != nil {
			return fail(err, false)
		}
		if err := h.fault(FaultJournalSync); err != nil {
			return fail(err, false)
		}
	}

	// Atomic publication, one artifact at a time.
	dirs := map[string]bool{}
	for i, t := range in.Targets {
		if err := ctx.Err(); err != nil {
			return fail(fmt.Errorf("cancelled after %d of %d publications; outcome uncertain", i, len(in.Targets)), true)
		}
		if err := h.fault(fmt.Sprintf("%s:%d", FaultPublish, i)); err != nil {
			return fail(err, false)
		}
		tp := abs(root, t.Rel)
		dirs[filepath.Dir(tp)] = true
		if t.AfterExists {
			if err := os.Rename(abs(root, t.Stage), tp); err != nil {
				return fail(err, false)
			}
			staged = removeString(staged, abs(root, t.Stage))
		} else if err := os.Remove(tp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fail(err, false)
		}
	}
	if err := h.fault(FaultDirSync); err != nil {
		return fail(err, false)
	}
	for d := range dirs {
		if err := syncDir(d); err != nil {
			return fail(err, false)
		}
	}

	// Journal cleanup completes the transaction.
	if err := h.fault(FaultCleanup); err != nil {
		return fail(err, false)
	}
	if err := os.Remove(jp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(err, false)
	}
	if err := syncDir(filepath.Dir(jp)); err != nil {
		return fail(err, false)
	}
	// The transaction is complete; leftover links are only clutter.
	removeBackups()
	res.DirectorySync = syncOK
	if in.History != nil {
		if res.HistoryErr = h.fault(FaultHistory); res.HistoryErr == nil {
			res.HistoryErr = appendChained(root, in.History)
		}
	}
	return res, nil
}

func removeString(list []string, s string) []string {
	for i, v := range list {
		if v == s {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// isSyncUnsupported reports platform refusal of directory fsync.
func isSyncUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.EBADF)
}

// DirOf returns the project-relative directory of a relative path.
func DirOf(rel string) string { return path.Dir(rel) }
