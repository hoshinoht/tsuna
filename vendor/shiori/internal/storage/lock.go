// Package storage implements the transactional write path:
// prepared intents, workspace/plan locks with owner metadata, exclusive
// same-directory staging, a durable journal published before the first
// replacement, atomic publication, directory sync, and explicit recovery.
// Nothing in this package runs before authorization: preparation lives in
// the engine and creates no files.
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Lock timing.
const (
	DefaultLockWait  = 5 * time.Second
	DefaultLockGrace = 5 * time.Minute
	lockPoll         = 25 * time.Millisecond
)

// Owner is lock-owner-v1 metadata.
type Owner struct {
	Hostname  string
	PID       int
	Nonce     string
	StartedAt time.Time
}

// NewOwner describes this process for a new lock.
func NewOwner(now time.Time) Owner {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return Owner{Hostname: host, PID: os.Getpid(), Nonce: randomHex(16), StartedAt: now.UTC()}
}

func (o Owner) encode() []byte {
	v := ojson.NewObject(4).
		Set("hostname", ojson.StringValue(o.Hostname)).
		Set("pid", ojson.IntValue(int64(o.PID))).
		Set("nonce", ojson.StringValue(o.Nonce)).
		Set("startedAt", ojson.StringValue(o.StartedAt.Format("2006-01-02T15:04:05.000Z"))).Value()
	return append(ojson.Pretty(v), '\n')
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// NewUUID returns a random RFC 4122 v4 UUID (transaction ids).
func NewUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// LockRef names one lock and every auxiliary path its protocol may touch.
// All are covered by the prepared intent.
type LockRef struct {
	Kind           string // "workspace" or "plan"
	Rel            string // the lock file
	Stage          string // owner staging file, linked into place
	Reclaim        string // reclaim mutex
	ReclaimStage   string // reclaim mutex owner staging
	ReclaimDead    string // quarantine for a dead reclaimer's mutex
	ReclaimRelease string // reclaim mutex release target
	Dead           string // quarantine for a proven-dead owner's lock
	Release        string // release rename target
}

// NewLockRef derives the lock protocol paths for a lock file.
func NewLockRef(kind, rel, tx string) LockRef {
	return LockRef{
		Kind:           kind,
		Rel:            rel,
		Stage:          rel + "." + tx + ".stage",
		Reclaim:        rel + ".reclaim",
		ReclaimStage:   rel + ".reclaim." + tx + ".stage",
		ReclaimDead:    rel + ".reclaim.dead-" + tx,
		ReclaimRelease: rel + ".reclaim.release-" + tx,
		Dead:           rel + ".dead-" + tx + "-" + kind + "-reclaim",
		Release:        rel + ".release-" + tx + "-" + kind,
	}
}

// Paths lists every path of the lock protocol.
func (l LockRef) Paths() []string {
	return []string{l.Rel, l.Stage, l.Reclaim, l.ReclaimStage, l.ReclaimDead, l.ReclaimRelease, l.Dead, l.Release}
}

// LockUnavailableError reports a lock that could not be acquired.
type LockUnavailableError struct {
	Path   string
	Detail string
}

func (e *LockUnavailableError) Error() string {
	return "Workplan lock is held: " + e.Path + " (" + e.Detail + "); retry after the owner finishes or run workplan_doctor"
}

// LockReplacedError reports that a lock we held was replaced by another
// owner; the replacement is left in place.
type LockReplacedError struct{ Path string }

func (e *LockReplacedError) Error() string {
	return "Workplan lock was replaced by another owner and was not removed: " + e.Path
}

// LockConfig controls waiting and reclaim.
type LockConfig struct {
	Wait  time.Duration
	Grace time.Duration
	Now   func() time.Time
	// ProcessAlive reports whether a same-host PID is live. The default
	// uses kill(pid, 0); EPERM counts as alive.
	ProcessAlive func(pid int) bool
}

func (c LockConfig) withDefaults() LockConfig {
	if c.Wait == 0 {
		c.Wait = DefaultLockWait
	}
	if c.Grace == 0 {
		c.Grace = DefaultLockGrace
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.ProcessAlive == nil {
		c.ProcessAlive = processAlive
	}
	return c
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// heldLock is an acquired lock.
type heldLock struct {
	ref   LockRef
	owner Owner
}

// publishExclusive writes data to stage (O_EXCL, synced) and links it to
// dst; link fails if dst exists, so dst appears complete or not at all.
func publishExclusive(root, stage, dst string, data []byte) error {
	sp, dp := abs(root, stage), abs(root, dst)
	if err := writeExclusive(sp, data, 0o600); err != nil {
		return err
	}
	err := os.Link(sp, dp)
	os.Remove(sp)
	return err
}

func writeExclusive(p string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(p)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(p)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(p)
		return err
	}
	if mode != 0o600 {
		if err := os.Chmod(p, mode); err != nil {
			os.Remove(p)
			return err
		}
	}
	return nil
}

// ownerState classifies a lock file's current owner.
type ownerState struct {
	owner      *Owner
	reclaim    bool
	diagnostic string
}

func readOwner(p string) (*Owner, time.Time, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	parsed, perr := ojson.Parse(data)
	if perr != nil {
		return nil, st.ModTime(), nil
	}
	lo, ok := model.DecodeLockOwner(parsed.Value)
	if !ok {
		return nil, st.ModTime(), nil
	}
	started, err := time.Parse(time.RFC3339Nano, lo.StartedAt)
	if err != nil {
		if started, err = time.Parse("2006-01-02T15:04Z", lo.StartedAt); err != nil {
			return nil, st.ModTime(), nil
		}
	}
	return &Owner{Hostname: lo.Hostname, PID: int(lo.PID), Nonce: lo.Nonce, StartedAt: started}, st.ModTime(), nil
}

// classify never permits reclaim by age alone: the owner must be
// well-formed, on this host, proven dead by the OS, and older than the
// grace period by both its recorded start and the file's mtime.
func classify(cfg LockConfig, p string) (ownerState, error) {
	o, mtime, err := readOwner(p)
	if err != nil {
		return ownerState{}, err
	}
	if o == nil {
		return ownerState{diagnostic: "ambiguous owner metadata; never reclaimed automatically"}, nil
	}
	host, _ := os.Hostname()
	s := ownerState{owner: o}
	switch {
	case o.Hostname != host:
		s.diagnostic = fmt.Sprintf("foreign owner %s pid %d; never reclaimed automatically", o.Hostname, o.PID)
	case cfg.ProcessAlive(o.PID):
		s.diagnostic = fmt.Sprintf("live owner pid %d", o.PID)
	default:
		newest := o.StartedAt
		if mtime.After(newest) {
			newest = mtime
		}
		if cfg.Now().Sub(newest) < cfg.Grace {
			s.diagnostic = fmt.Sprintf("dead owner pid %d within the %s abandonment grace", o.PID, cfg.Grace)
		} else {
			s.reclaim = true
		}
	}
	return s, nil
}

// acquire obtains one lock, waiting up to cfg.Wait.
func acquire(ctx context.Context, root string, ref LockRef, owner Owner, cfg LockConfig) (*heldLock, error) {
	deadline := cfg.Now().Add(cfg.Wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := publishExclusive(root, ref.Stage, ref.Rel, owner.encode())
		if err == nil {
			return &heldLock{ref: ref, owner: owner}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		st, cerr := classify(cfg, abs(root, ref.Rel))
		if cerr != nil && !errors.Is(cerr, fs.ErrNotExist) {
			return nil, cerr
		}
		if cerr == nil && st.reclaim {
			if rerr := reclaim(root, ref, *st.owner, owner, cfg); rerr != nil {
				st.diagnostic = rerr.Error()
			} else {
				continue
			}
		}
		if cerr != nil {
			continue // released meanwhile
		}
		if !cfg.Now().Before(deadline) {
			return nil, &LockUnavailableError{Path: abs(root, ref.Rel), Detail: st.diagnostic}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// reclaim removes a proven-dead owner's lock under the reclaim mutex,
// verifying by nonce that exactly that owner's file is removed.
func reclaim(root string, ref LockRef, dead Owner, me Owner, cfg LockConfig) error {
	mutexStage := ref.ReclaimStage
	if err := publishExclusive(root, mutexStage, ref.Reclaim, me.encode()); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		// A reclaimer crashed? Apply the same liveness rules to it.
		st, cerr := classify(cfg, abs(root, ref.Reclaim))
		if cerr != nil || !st.reclaim {
			return errors.New("reclaim in progress by another process")
		}
		if err := removeVerified(root, ref.Reclaim, ref.ReclaimDead, st.owner.Nonce); err != nil {
			return err
		}
		if err := publishExclusive(root, mutexStage, ref.Reclaim, me.encode()); err != nil {
			return err
		}
	}
	defer removeVerified(root, ref.Reclaim, ref.ReclaimRelease, me.Nonce)
	cur, _, err := readOwner(abs(root, ref.Rel))
	if err != nil || cur == nil || cur.Nonce != dead.Nonce {
		return errors.New("lock owner changed during reclaim")
	}
	return removeVerified(root, ref.Rel, ref.Dead, dead.Nonce)
}

// removeVerified atomically moves path aside, and deletes it only when it
// carries nonce; otherwise it restores it (link never overwrites) and
// reports a replacement.
func removeVerified(root, rel, aside, nonce string) error {
	p, a := abs(root, rel), abs(root, aside)
	if err := os.Rename(p, a); err != nil {
		return err
	}
	o, _, err := readOwner(a)
	if err == nil && o != nil && o.Nonce == nonce {
		return os.Remove(a)
	}
	if lerr := os.Link(a, p); lerr == nil {
		os.Remove(a)
	}
	return &LockReplacedError{Path: p}
}

// release drops a held lock without ever unlinking a replacement owner's
// lock.
func (h *heldLock) release(root string) error {
	return removeVerified(root, h.ref.Rel, h.ref.Release, h.owner.Nonce)
}

func abs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
