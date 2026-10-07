package conformance

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Resume budgeting keeps display text readable and
// pages the rest, so some oracle packets change. The comparator proves the
// packet is the oracle's up to the approved change: the same machine
// fields (ids, hashes, counts, totals, freshness, retrieval); every display
// string is the oracle's string or a compatible truncation of the same
// text; page items are the same items in the same order (a prefix of
// either side); page arithmetic and omission counts are self-consistent.

// minTitle is the readable minimum of a shortened display string outside
// the single-item emergency levels.
const minTitle = 80

var resumeSkip = map[string]bool{
	"safety.overflow": true, "safety.truncatedDangerFieldCount": true, "safety.overflowPointers": true,
	"safety.omittedDangerCounts": true, "safety.unverifiedWarningsOmitted": true,
	"truncatedFields": true, "truncatedFieldCount": true, "truncatedFieldPathsOmitted": true,
	"page.returned": true, "page.omitted": true, "page.nextCursor": true,
}

var resumePrefixLists = map[string]bool{
	"checkpoint.blockers": true, "checkpoint.guardrails": true, "checkpoint.references": true,
	"checkpoint.recentValidation": true, "workplan.scope": true, "workplan.nonGoals": true,
	"workplan.constraints": true, "workplan.relevantFiles": true, "safety.highFindings": true,
	"safety.unverifiedWarnings": true, "currentDependencies.references": true, "page.items": true,
}

// truncCompatible reports whether a and b are the same text, possibly
// shortened with a trailing ellipsis on either side.
func truncCompatible(a, b string) bool {
	if a == b {
		return true
	}
	ta, tb := strings.HasSuffix(a, "…"), strings.HasSuffix(b, "…")
	sa, sb := strings.TrimSuffix(a, "…"), strings.TrimSuffix(b, "…")
	switch {
	case ta && tb:
		return strings.HasPrefix(sa, sb) || strings.HasPrefix(sb, sa)
	case ta:
		return strings.HasPrefix(b, sa)
	case tb:
		return strings.HasPrefix(a, sb)
	}
	return false
}

// compatible compares a packet value with the oracle's under the
// truncation and prefix-list rules.
func compatible(g, w any, path string) error {
	if resumeSkip[path] {
		return nil
	}
	switch wv := w.(type) {
	case map[string]any:
		gv, ok := g.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: type differs", path)
		}
		if len(gv) != len(wv) {
			return fmt.Errorf("%s: member sets differ", path)
		}
		for k, x := range wv {
			y, ok := gv[k]
			if !ok {
				return fmt.Errorf("%s: missing member %s", path, k)
			}
			p := k
			if path != "" {
				p = path + "." + k
			}
			if err := compatible(y, x, p); err != nil {
				return err
			}
		}
		return nil
	case []any:
		gv, ok := g.([]any)
		if !ok {
			return fmt.Errorf("%s: type differs", path)
		}
		n := len(wv)
		if resumePrefixLists[path] {
			if len(gv) < n {
				n = len(gv)
			}
		} else if len(gv) != len(wv) {
			return fmt.Errorf("%s: length %d want %d", path, len(gv), len(wv))
		}
		for i := 0; i < n; i++ {
			if err := compatible(gv[i], wv[i], path); err != nil {
				return fmt.Errorf("[%d] %w", i, err)
			}
		}
		return nil
	case string:
		gs, ok := g.(string)
		if !ok || !truncCompatible(gs, wv) {
			return fmt.Errorf("%s: %q is not a truncation-compatible form of %q", path, g, wv)
		}
		return nil
	}
	if !reflect.DeepEqual(g, w) {
		return fmt.Errorf("%s: %v want %v", path, g, w)
	}
	return nil
}

// resumeBudgetCompat compares a budgeted resume packet with the oracle's
// and checks its self-consistency and readability invariants.
func resumeBudgetCompat(got, want string) error {
	g, err := decodeAny(got)
	if err != nil {
		return err
	}
	w, err := decodeAny(want)
	if err != nil {
		return err
	}
	gm := g.(map[string]any)
	if err := compatible(g, w, ""); err != nil {
		return err
	}
	pg := gm["page"].(map[string]any)
	items := pg["items"].([]any)
	ret, om, off, tot := num(pg["returned"]), num(pg["omitted"]), num(pg["offset"]), num(pg["total"])
	if ret != len(items) || ret+om+off != tot {
		return fmt.Errorf("page arithmetic: returned %d omitted %d offset %d total %d", ret, om, off, tot)
	}
	if _, isStr := pg["nextCursor"].(string); isStr != (om > 0) {
		return fmt.Errorf("nextCursor presence does not match omitted=%d", om)
	}
	if om > 0 && ret == 0 {
		return fmt.Errorf("page does not progress")
	}
	wp := gm["workplan"].(map[string]any)
	cp := gm["checkpoint"].(map[string]any)
	sf := gm["safety"].(map[string]any)
	odc := sf["omittedDangerCounts"].(map[string]any)
	for _, c := range []struct {
		obj        map[string]any
		list, tot  string
		omittedKey string
	}{{wp, "constraints", "constraintsTotal", "constraints"}, {wp, "scope", "scopeTotal", "scope"}, {wp, "nonGoals", "nonGoalsTotal", "nonGoals"},
		{cp, "blockers", "blockersTotal", "blockers"}, {sf, "highFindings", "highFindingsTotal", "highFindings"}} {
		if l, ok := c.obj[c.list].([]any); ok {
			if num(c.obj[c.tot])-len(l) != num(odc[c.omittedKey]) {
				return fmt.Errorf("omittedDangerCounts.%s inconsistent", c.omittedKey)
			}
		}
	}
	// Readability: outside the single-item emergency levels no display
	// string is shortened below the title minimum, and paths are never
	// shortened.
	if ret > 1 {
		var short []string
		var walk func(x any, p string)
		walk = func(x any, p string) {
			switch xv := x.(type) {
			case map[string]any:
				for k, y := range xv {
					walk(y, p+"."+k)
				}
			case []any:
				for _, y := range xv {
					walk(y, p+"[]")
				}
			case string:
				// One unit of slack: a surrogate pair is never split.
				if strings.HasSuffix(xv, "…") && ojson.UTF16Len(xv) < minTitle-1 {
					short = append(short, p)
				}
			}
		}
		walk(g, "")
		if len(short) > 0 {
			return fmt.Errorf("strings shortened below the readable minimum with %d items: %v", ret, short)
		}
		for _, k := range []string{"path", "planFile"} {
			if s, _ := gm[k].(string); strings.HasSuffix(s, "…") {
				return fmt.Errorf("%s was truncated", k)
			}
		}
	}
	return nil
}
