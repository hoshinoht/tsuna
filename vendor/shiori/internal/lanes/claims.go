package lanes

import (
	"sort"
	"strings"
)

// Trie holds path claims by segment. Claiming a path claims everything
// under it, so two claims overlap when one is a segment prefix of the
// other.
type Trie struct{ root node }

type node struct {
	owner    string // lane id when a claim ends here
	children map[string]*node
}

// Conflict is an overlap between a new claim and an existing one.
type Conflict struct {
	Path, By   string // the new claim and its lane
	With, Lane string // the existing claim and its lane
}

// Insert claims path for lane, or reports the existing claim it overlaps
// (an ancestor, the same path, or a descendant). Overlaps within one lane
// are allowed and stored once.
func (t *Trie) Insert(path, lane string) *Conflict {
	n := &t.root
	segs := strings.Split(path, "/")
	prefix := ""
	for _, s := range segs {
		if n.owner != "" && n.owner != lane {
			return &Conflict{Path: path, By: lane, Lane: n.owner, With: prefix}
		}
		if n.children == nil {
			n.children = map[string]*node{}
		}
		c := n.children[s]
		if c == nil {
			c = &node{}
			n.children[s] = c
		}
		prefix = strings.TrimPrefix(prefix+"/"+s, "/")
		n = c
	}
	if n.owner != "" && n.owner != lane {
		return &Conflict{Path: path, By: lane, Lane: n.owner, With: path}
	}
	if other, at := n.descendant(lane, path); other != "" {
		return &Conflict{Path: path, By: lane, Lane: other, With: at}
	}
	n.owner = lane
	return nil
}

// descendant finds a claim of another lane under n.
func (n *node) descendant(lane, at string) (string, string) {
	keys := make([]string, 0, len(n.children))
	for k := range n.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := n.children[k]
		p := at + "/" + k
		if c.owner != "" && c.owner != lane {
			return c.owner, p
		}
		if o, q := c.descendant(lane, p); o != "" {
			return o, q
		}
	}
	return "", ""
}

// Covers reports whether path is under one of claims.
func Covers(claims []string, path string) bool {
	for _, c := range claims {
		if path == c || strings.HasPrefix(path, c+"/") {
			return true
		}
	}
	return false
}

// ClaimTrie builds the trie of every active lane's claims, skipping skip.
func (l *Ledger) ClaimTrie(skip string) (*Trie, *Conflict) {
	t := &Trie{}
	for i := range l.Lanes {
		ln := &l.Lanes[i]
		if !Active(ln.State) || ln.ID == skip {
			continue
		}
		for _, c := range ln.Claims {
			if cf := t.Insert(c, ln.ID); cf != nil {
				return t, cf
			}
		}
	}
	return t, nil
}

// MergeOrder orders the active lanes so that a lane comes after every
// lane owning a prerequisite of its steps (prereqs maps a lane to the
// lanes it depends on). Ties keep creation order. Lanes on a cycle are
// returned separately.
func (l *Ledger) MergeOrder(prereqs map[string][]string) (order, cycle []string) {
	var ids []string
	active := map[string]bool{}
	for _, ln := range l.Lanes {
		if Active(ln.State) {
			ids = append(ids, ln.ID)
			active[ln.ID] = true
		}
	}
	done := map[string]bool{}
	for len(order)+len(cycle) < len(ids) {
		progressed := false
		for _, id := range ids {
			if done[id] {
				continue
			}
			ready := true
			for _, p := range prereqs[id] {
				if active[p] && !done[p] {
					ready = false
				}
			}
			if ready {
				done[id] = true
				order = append(order, id)
				progressed = true
			}
		}
		if !progressed {
			for _, id := range ids {
				if !done[id] {
					cycle = append(cycle, id)
					done[id] = true
				}
			}
		}
	}
	return order, cycle
}
