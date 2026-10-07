package gitview

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sort"
	"strings"
)

// Commit is one commit and its content as seen from a project root.
type Commit struct {
	Hash string
	Tree *Tree
	// ContentOID is the tree OID of that content (the root's subtree,
	// without the excluded directory): what Snapshot computes for a
	// working state with exactly this content.
	ContentOID string
}

// Commits lists up to n commits reachable from HEAD, newest first.
func Commits(ctx context.Context, root, exclude string, n int) ([]Commit, error) {
	c, cancel := context.WithTimeout(ctx, TreeTimeout)
	g := &gitRun{ctx: c, dir: root}
	out, err := g.run(nil, "log", "-n", itoa(n), "--format=%H")
	cancel()
	if err != nil {
		return nil, err
	}
	var list []Commit
	for _, h := range strings.Fields(out) {
		t, err := HeadTree(ctx, root, h, exclude)
		if err != nil {
			return nil, err
		}
		list = append(list, Commit{Hash: h, Tree: t, ContentOID: t.ContentOID()})
	}
	return list, nil
}

func itoa(n int) string {
	if n <= 0 {
		return "1"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// Digest is the scope digest of prefix in t (nil: no entries).
func (t *Tree) Digest(prefix string) *string { return t.digest(prefix) }

// ContentOID computes the git tree OID of t's entries in memory, the way
// write-tree would, without writing anything.
func (t *Tree) ContentOID() string {
	sha256Repo := false
	for _, e := range t.entries {
		if f := strings.Fields(e.id); len(f) == 2 {
			sha256Repo = len(f[1]) == 64
			break
		}
	}
	newHash := sha1.New
	if sha256Repo {
		newHash = sha256.New
	}
	root := &dirNode{children: map[string]*dirNode{}}
	for _, e := range t.entries {
		f := strings.Fields(e.id)
		if len(f) != 2 {
			continue
		}
		parts := strings.Split(e.path, "/")
		d := root
		for _, p := range parts[:len(parts)-1] {
			c, ok := d.children[p]
			if !ok {
				c = &dirNode{children: map[string]*dirNode{}}
				d.children[p] = c
			}
			d = c
		}
		d.files = append(d.files, fileEntry{name: parts[len(parts)-1], mode: f[0], oid: f[1]})
	}
	return hex.EncodeToString(root.oid(newHash))
}

type fileEntry struct{ name, mode, oid string }

type dirNode struct {
	children map[string]*dirNode
	files    []fileEntry
}

func (d *dirNode) oid(newHash func() hash.Hash) []byte {
	type item struct {
		key, name, mode string
		raw             []byte
	}
	var items []item
	for name, c := range d.children {
		items = append(items, item{key: name + "/", name: name, mode: "40000", raw: c.oid(newHash)})
	}
	for _, f := range d.files {
		raw, _ := hex.DecodeString(f.oid)
		items = append(items, item{key: f.name, name: f.name, mode: strings.TrimLeft(f.mode, "0"), raw: raw})
	}
	// Git orders entries by name, a directory as if it ended in '/'.
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	var body []byte
	for _, it := range items {
		body = append(body, it.mode+" "+it.name+"\x00"...)
		body = append(body, it.raw...)
	}
	h := newHash()
	h.Write([]byte("tree " + itoa0(len(body)) + "\x00"))
	h.Write(body)
	return h.Sum(nil)
}

func itoa0(n int) string {
	if n == 0 {
		return "0"
	}
	return itoa(n)
}
