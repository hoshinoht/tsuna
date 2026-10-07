package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lockRoot(t *testing.T) (string, LockRef) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".opencode/workplan"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, NewLockRef("plan", ".opencode/workplan/.p.lock", NewUUID())
}

func writeOwner(t *testing.T, root string, ref LockRef, o Owner, age time.Duration) {
	t.Helper()
	p := abs(root, ref.Rel)
	if err := os.WriteFile(p, o.encode(), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func fastCfg(alive bool) LockConfig {
	return LockConfig{Wait: 150 * time.Millisecond, Grace: time.Minute, ProcessAlive: func(int) bool { return alive }}.withDefaults()
}

func ownerAt(host string, pid int, age time.Duration) Owner {
	return Owner{Hostname: host, PID: pid, Nonce: randomHex(8), StartedAt: time.Now().Add(-age).UTC()}
}

func hostname(t *testing.T) string {
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Live, foreign, ambiguous and reused owners are never reclaimed,
// however old; a proven-dead same-host owner is reclaimed only after the
// grace period.
func TestLockNeverReclaimedByAgeAlone(t *testing.T) {
	old := 10 * time.Hour
	cases := []struct {
		name    string
		owner   *Owner
		raw     string
		alive   bool
		age     time.Duration
		reclaim bool
	}{
		{name: "live same-host owner", owner: ptr(ownerAt(hostname(t), 4242, old)), alive: true, age: old},
		{name: "reused pid (alive process, stale metadata)", owner: ptr(ownerAt(hostname(t), 1, old)), alive: true, age: old},
		{name: "foreign host", owner: ptr(ownerAt("other-host.invalid", 4242, old)), alive: false, age: old},
		{name: "ambiguous metadata", raw: "not json", age: old},
		{name: "empty lock file", raw: "", age: old},
		{name: "dead owner within grace", owner: ptr(ownerAt(hostname(t), 4242, time.Second)), alive: false, age: time.Second},
		{name: "dead owner, old start but fresh mtime", owner: ptr(ownerAt(hostname(t), 4242, old)), alive: false, age: time.Second},
		{name: "dead owner beyond grace", owner: ptr(ownerAt(hostname(t), 4242, old)), alive: false, age: old, reclaim: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, ref := lockRoot(t)
			if c.owner != nil {
				writeOwner(t, root, ref, *c.owner, c.age)
			} else {
				p := abs(root, ref.Rel)
				os.WriteFile(p, []byte(c.raw), 0o600)
				mt := time.Now().Add(-c.age)
				os.Chtimes(p, mt, mt)
			}
			before, _ := os.ReadFile(abs(root, ref.Rel))
			me := NewOwner(time.Now())
			h, err := acquire(context.Background(), root, ref, me, fastCfg(c.alive))
			if c.reclaim {
				if err != nil {
					t.Fatalf("expected reclaim, got %v", err)
				}
				if err := h.release(root); err != nil {
					t.Fatal(err)
				}
				return
			}
			var lu *LockUnavailableError
			if !errors.As(err, &lu) {
				t.Fatalf("expected LockUnavailableError, got %v", err)
			}
			after, _ := os.ReadFile(abs(root, ref.Rel))
			if string(after) != string(before) {
				t.Fatal("existing lock was modified")
			}
			assertNoAux(t, root)
		})
	}
}

func ptr(o Owner) *Owner { return &o }

func assertNoAux(t *testing.T, root string) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(root, ".opencode/workplan"))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".reclaim") || strings.Contains(e.Name(), ".release-") || strings.HasSuffix(e.Name(), ".stage") {
			t.Fatalf("lock protocol left %s", e.Name())
		}
	}
}

// Cleanup must never unlink a replacement owner's lock.
func TestReleaseNeverRemovesReplacement(t *testing.T) {
	root, ref := lockRoot(t)
	h, err := acquire(context.Background(), root, ref, NewOwner(time.Now()), fastCfg(true))
	if err != nil {
		t.Fatal(err)
	}
	other := ownerAt(hostname(t), 999999, 0)
	writeOwner(t, root, ref, other, 0) // replaced behind our back
	err = h.release(root)
	var lr *LockReplacedError
	if !errors.As(err, &lr) {
		t.Fatalf("expected LockReplacedError, got %v", err)
	}
	o, _, err := readOwner(abs(root, ref.Rel))
	if err != nil || o == nil || o.Nonce != other.Nonce {
		t.Fatalf("replacement lock was not preserved: %v %v", o, err)
	}
	assertNoAux(t, root)
}

// A dead owner's lock replaced by a live owner during reclaim is kept.
func TestReclaimVerifiesNonce(t *testing.T) {
	root, ref := lockRoot(t)
	dead := ownerAt(hostname(t), 4242, 10*time.Hour)
	writeOwner(t, root, ref, dead, 10*time.Hour)
	live := ownerAt(hostname(t), os.Getpid(), 0)
	writeOwner(t, root, ref, live, 0) // replaced after the judgement
	err := reclaim(root, ref, dead, NewOwner(time.Now()), fastCfg(false))
	if err == nil {
		t.Fatal("reclaim removed a lock it did not judge")
	}
	o, _, _ := readOwner(abs(root, ref.Rel))
	if o == nil || o.Nonce != live.Nonce {
		t.Fatal("live replacement was removed")
	}
	assertNoAux(t, root)
}

// Waiting honours cancellation.
func TestAcquireCancelled(t *testing.T) {
	root, ref := lockRoot(t)
	writeOwner(t, root, ref, ownerAt(hostname(t), os.Getpid(), 0), 0)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	cfg := LockConfig{Wait: 5 * time.Second}.withDefaults()
	start := time.Now()
	_, err := acquire(ctx, root, ref, NewOwner(time.Now()), cfg)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

// Lock files are written atomically: a new lock appears complete.
func TestLockOwnerMetadata(t *testing.T) {
	root, ref := lockRoot(t)
	me := NewOwner(time.Now())
	h, err := acquire(context.Background(), root, ref, me, fastCfg(true))
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := readOwner(abs(root, ref.Rel))
	if err != nil || o == nil || o.Nonce != me.Nonce || o.PID != os.Getpid() || o.Hostname != hostname(t) {
		t.Fatalf("owner metadata %+v %v", o, err)
	}
	if err := h.release(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs(root, ref.Rel)); !os.IsNotExist(err) {
		t.Fatal("lock not released")
	}
	assertNoAux(t, root)
}
