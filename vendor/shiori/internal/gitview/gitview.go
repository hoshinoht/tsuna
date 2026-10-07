package gitview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Tree is the git tree of a project root's working state (uncommitted and
// untracked, non-ignored files included), without the workplan directory.
type Tree struct {
	OID     string
	Scope   map[string]*string // scope path -> entries digest (nil: absent)
	entries []entry
}

// TreeTimeout bounds one tree computation.
var TreeTimeout = 10 * time.Second

// GitBinary is the git executable (tests may override it).
var GitBinary = "git"

// ErrNoGit: no git work tree; records get no tree and state "unknown".
var ErrNoGit = errors.New("not inside a git work tree")

// Snapshot computes root's working tree without writing to the repository:
// git works on a copied index with a private, empty object directory (no
// alternates, so not even object mtimes are freshened). exclude is removed
// from the tree; each scope path gets a digest of its index entries.
func Snapshot(ctx context.Context, root, exclude string, scope []string) (*Tree, error) {
	ctx, cancel := context.WithTimeout(ctx, TreeTimeout)
	defer cancel()
	g := &gitRun{ctx: ctx, dir: root}
	out, err := g.run(nil, "rev-parse", "--is-inside-work-tree", "--show-prefix", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoGit, err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "true" {
		return nil, ErrNoGit
	}
	prefix, index := lines[1], lines[2]

	tmp, err := os.MkdirTemp("", "shiori-evidence-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := os.Mkdir(filepath.Join(tmp, "objects"), 0o700); err != nil {
		return nil, err
	}
	tmpIndex := filepath.Join(tmp, "index")
	if err := copyFile(index, tmpIndex); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	g.env = []string{"GIT_INDEX_FILE=" + tmpIndex, "GIT_OBJECT_DIRECTORY=" + filepath.Join(tmp, "objects")}
	if _, err := g.run(nil, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	if exclude != "" {
		if _, err := g.run(nil, "rm", "-r", "--cached", "-q", "--ignore-unmatch", "--", exclude); err != nil {
			return nil, err
		}
	}
	args := []string{"write-tree", "--missing-ok"}
	if prefix != "" {
		args = append(args, "--prefix="+prefix)
	}
	oid, err := g.run(nil, args...)
	if err != nil {
		// An empty project subtree: hash the empty tree.
		if prefix == "" {
			return nil, err
		}
		if oid, err = g.run(strings.NewReader(""), "hash-object", "-t", "tree", "--stdin"); err != nil {
			return nil, err
		}
	}
	listing, err := g.run(nil, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	t := &Tree{OID: strings.TrimSpace(oid), Scope: map[string]*string{}, entries: parseEntries(listing)}
	for _, p := range scope {
		t.Scope[p] = t.digest(p)
	}
	return t, nil
}

// entry is one index line: meta is "<mode> <oid> <stage>" as listed, id
// is "<mode> <oid>" (what two trees compare on).
type entry struct{ path, meta, id string }

func parseEntries(listing string) []entry {
	var out []entry
	for _, rec := range strings.Split(listing, "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 {
			out = append(out, entry{path, meta, f[0] + " " + f[1]})
		}
	}
	return out
}

func under(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// digest is the sha256 of `git ls-files -s -z -- <prefix>` (nil: no
// entries), computed from the one full listing.
func (t *Tree) digest(prefix string) *string {
	h := sha256.New()
	n := 0
	for _, e := range t.entries {
		if under(e.path, prefix) {
			h.Write([]byte(e.meta + "\t" + e.path + "\x00"))
			n++
		}
	}
	if n == 0 {
		return nil
	}
	d := hex.EncodeToString(h.Sum(nil))
	return &d
}

// Changed lists, in path order, the paths whose entries differ between
// two trees of the same repository (added, removed or modified).
func (t *Tree) Changed(other *Tree) []string {
	a := map[string]string{}
	for _, e := range t.entries {
		a[e.path] = e.id
	}
	var out []string
	for _, e := range other.entries {
		if m, ok := a[e.path]; !ok || m != e.id {
			out = append(out, e.path)
		}
		delete(a, e.path)
	}
	for p := range a {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type gitRun struct {
	ctx context.Context
	dir string
	env []string
}

// run executes git without inherited GIT_* variables, prompts or optional locks.
func (g *gitRun) run(stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(g.ctx, GitBinary, append([]string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = g.dir
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "LC_ALL=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"), g.env...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if g.ctx.Err() != nil {
			return "", fmt.Errorf("git %s timed out", args[0])
		}
		msg := strings.TrimSpace(errb.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Repo is a work tree's read-only git facts.
type Repo struct {
	Dir       string // the directory asked about
	CommonDir string // absolute; shared by every worktree of a repository
	Head      string // commit, "" on an unborn branch
	Branch    string // "" when detached
}

// Open reads dir's repository facts. Nothing is written.
func Open(ctx context.Context, dir string) (*Repo, error) {
	ctx, cancel := context.WithTimeout(ctx, TreeTimeout)
	defer cancel()
	g := &gitRun{ctx: ctx, dir: dir}
	out, err := g.run(nil, "rev-parse", "--is-inside-work-tree", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoGit, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != "true" {
		return nil, ErrNoGit
	}
	r := &Repo{Dir: dir, CommonDir: filepath.Clean(lines[1])}
	if h, err := g.run(nil, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		r.Head = strings.TrimSpace(h)
	}
	if b, err := g.run(nil, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		r.Branch = strings.TrimSpace(b)
	}
	return r, nil
}

// HeadTree is the tree of a commit as seen from root (the same subtree
// and exclusion as Snapshot), for comparing with a working tree.
func HeadTree(ctx context.Context, root, rev, exclude string) (*Tree, error) {
	ctx, cancel := context.WithTimeout(ctx, TreeTimeout)
	defer cancel()
	g := &gitRun{ctx: ctx, dir: root}
	out, err := g.run(nil, "ls-tree", "-r", "-z", rev)
	if err != nil {
		return nil, err
	}
	t := &Tree{OID: rev}
	for _, rec := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || (exclude != "" && under(path, exclude)) {
			continue
		}
		t.entries = append(t.entries, entry{path: path, meta: f[0] + " " + f[2] + " 0", id: f[0] + " " + f[2]})
	}
	return t, nil
}

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path, Head, Branch string
	Bare, Prunable     bool
}

// Worktrees lists the repository's worktrees.
func Worktrees(ctx context.Context, dir string) ([]Worktree, error) {
	ctx, cancel := context.WithTimeout(ctx, TreeTimeout)
	defer cancel()
	g := &gitRun{ctx: ctx, dir: dir}
	out, err := g.run(nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	var cur *Worktree
	for _, f := range strings.Split(out, "\x00") {
		k, v, _ := strings.Cut(f, " ")
		switch {
		case k == "worktree":
			list = append(list, Worktree{Path: filepath.Clean(v)})
			cur = &list[len(list)-1]
		case cur == nil:
		case k == "HEAD":
			cur.Head = v
		case k == "branch":
			cur.Branch = strings.TrimPrefix(v, "refs/heads/")
		case k == "bare":
			cur.Bare = true
		case k == "prunable":
			cur.Prunable = true
		}
	}
	return list, nil
}

// IsAncestor reports whether commit a is an ancestor of (or equal to) b.
func IsAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TreeTimeout)
	defer cancel()
	g := &gitRun{ctx: ctx, dir: dir}
	_, err := g.run(nil, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if strings.HasSuffix(err.Error(), "exit status 1") { // not an ancestor
		return false, nil
	}
	return false, err
}
