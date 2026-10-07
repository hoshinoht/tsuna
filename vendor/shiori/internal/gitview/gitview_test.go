package gitview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSnapshotOfSubdirectoryRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	root := filepath.Join(repo, "app")
	for _, d := range []string{"app/src", "app/.opencode/workplan", "other"} {
		os.MkdirAll(filepath.Join(repo, d), 0o755)
	}
	write := func(rel, body string) { os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644) }
	write("app/src/a.go", "a")
	write("other/x", "x")
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "init")
	snap := func() *Tree {
		tr, err := Snapshot(context.Background(), root, ".opencode/workplan", []string{"src", "src/a.go", "missing"})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	t0 := snap()
	if t0.Scope["missing"] != nil || t0.Scope["src"] == nil || t0.Scope["src/a.go"] == nil {
		t.Fatalf("scope %v", t0.Scope)
	}
	// The digest is that of git's own listing for the path.
	for _, p := range []string{"src", "src/a.go"} {
		cmd := exec.Command("git", "ls-files", "-s", "-z", "--", p)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(out)
		if *t0.Scope[p] != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s digest differs from git ls-files", p)
		}
	}
	write("other/x", "changed")
	write("app/.opencode/workplan/p.json", "{}")
	if t1 := snap(); t1.OID != t0.OID {
		t.Fatal("changes outside the root or in the workplan directory changed the tree")
	}
	write("app/src/b.go", "b")
	t2 := snap()
	if t2.OID == t0.OID || *t2.Scope["src"] == *t0.Scope["src"] || *t2.Scope["src/a.go"] != *t0.Scope["src/a.go"] {
		t.Fatal("an untracked file in the root did not change the tree and src only")
	}
	if _, err := Snapshot(context.Background(), t.TempDir(), "", nil); !errors.Is(err, ErrNoGit) {
		t.Fatalf("outside git: %v", err)
	}
}

// TestCommitContentMatchesSnapshot: the in-memory tree OID of a commit
// equals the snapshot of a clean working state with that content, for a
// subdirectory root with a committed workplan directory, a symlink and an
// executable.
func TestCommitContentMatchesSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	root := filepath.Join(repo, "app")
	for _, d := range []string{"app/src/deep/er", "app/.opencode/workplan", "other"} {
		os.MkdirAll(filepath.Join(repo, d), 0o755)
	}
	write := func(rel, body string) { os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644) }
	write("app/src/a.go", "a")
	write("app/src-b", "b")
	write("app/src/deep/er/c", "c")
	write("app/.opencode/workplan/p.json", "{}")
	write("other/x", "x")
	os.Symlink("a.go", filepath.Join(root, "src/link"))
	os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "one")
	first, err := Snapshot(context.Background(), root, ".opencode/workplan", []string{"src"})
	if err != nil {
		t.Fatal(err)
	}
	write("app/src/a.go", "a2")
	git("commit", "-qam", "two")
	second, _ := Snapshot(context.Background(), root, ".opencode/workplan", []string{"src"})
	cs, err := Commits(context.Background(), root, ".opencode/workplan", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].ContentOID != second.OID || cs[1].ContentOID != first.OID {
		t.Fatalf("commit content %v vs snapshots %s %s", cs, second.OID, first.OID)
	}
	if d := cs[1].Tree.Digest("src"); d == nil || *d != *first.Scope["src"] {
		t.Fatal("scope digest of a commit differs from the snapshot's")
	}
}
