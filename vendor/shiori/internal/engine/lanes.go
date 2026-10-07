package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoshinoht/shiori/internal/gitview"
	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/lanes"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Worktree lanes (spec 06 X3, spec 04): a parent-owned record outside the
// state manifest. Shiori checks claims and checkouts; it never runs git
// worktree, merges or removes anything.

func lanesRel(id string) string { return snapshot.SidecarRel(id, lanes.Suffix) }

type lnView struct {
	exists bool
	rel    string
	ledger *lanes.Ledger
	issues []string
	art    snapshot.Artifact
}

func (e *Engine) loadLanes(id string) (lnView, error) {
	v := lnView{rel: lanesRel(id)}
	r := &snapshot.Reader{Root: e.Root, Limits: e.Limits}
	a, err := r.ReadFile(v.rel)
	if err != nil {
		return v, err
	}
	v.art, v.exists = a, a.Exists
	if !a.Exists {
		return v, nil
	}
	parsed, perr := ojson.Parse(a.Bytes)
	if perr != nil {
		v.issues = []string{"Invalid lanes JSON at " + a.Path + ": " + perr.Error()}
		return v, nil
	}
	l, issues := lanes.Decode(parsed.Value)
	switch {
	case len(issues) > 0:
		v.issues = issues
	case l.ID != id:
		v.issues = []string{"id: Lanes sidecar belongs to " + l.ID + ", not " + id}
	default:
		v.ledger = l
	}
	return v, nil
}

// normalizeClaim validates a project-relative path a lane claims or an
// evidence record covers: inside the root, not the root itself, not the
// workplan directory.
func (e *Engine) normalizeClaim(field, raw string) (string, error) {
	t := strings.TrimSuffix(model.TrimJS(raw), "/")
	rel, err := snapshot.NormalizeSpecFile(e.Root, t)
	if err != nil || t == "" || rel == "" || rel == "." {
		return "", fmt.Errorf("%s: path must be inside the workspace root and not the root itself: %s", field, raw)
	}
	if rel == snapshot.WorkplanDir || strings.HasPrefix(rel, snapshot.WorkplanDir+"/") {
		return "", fmt.Errorf("%s: path cannot be in the workplan directory (its files are not code under test): %s", field, raw)
	}
	return rel, nil
}

// laneBaseline fingerprints the parent working state.
func (e *Engine) laneBaseline() (lanes.Baseline, []string) {
	var b lanes.Baseline
	ctx := context.Background()
	t, err := gitview.Snapshot(ctx, e.Root, snapshot.WorkplanDir, nil)
	if err != nil {
		return b, nil
	}
	b.TreeOID = &t.OID
	repo, err := gitview.Open(ctx, e.Root)
	if err != nil || repo.Head == "" {
		return b, nil
	}
	b.Head = &repo.Head
	ht, err := gitview.HeadTree(ctx, e.Root, repo.Head, snapshot.WorkplanDir)
	if err != nil {
		return b, nil
	}
	dirty := ht.Changed(t)
	b.Dirty = len(dirty)
	return b, dirty
}

// checkoutFor verifies a checkout reported for a lane: an existing
// worktree of this repository, outside the project root, on the stated
// branch (recorded when omitted).
func (e *Engine) checkoutFor(cv ojson.Value) (*lanes.Checkout, error) {
	raw, _ := getStr(cv, "path")
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.Root, p)
	}
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	} else {
		return nil, fmt.Errorf("Lane checkout does not exist: %s", raw)
	}
	if rel, err := filepath.Rel(e.Root, p); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
		return nil, fmt.Errorf("Lane checkout must be outside the project root (a nested worktree would enter the parent's tree): %s", p)
	}
	ctx := context.Background()
	parent, err := gitview.Open(ctx, e.Root)
	if err != nil {
		return nil, fmt.Errorf("Lane checkouts need the project root in a git work tree: %v", err)
	}
	repo, err := gitview.Open(ctx, p)
	if err != nil || repo.CommonDir != parent.CommonDir {
		return nil, fmt.Errorf("Lane checkout is not a worktree of this repository: %s", p)
	}
	co := &lanes.Checkout{Path: p}
	if br, ok := getStr(cv, "branch"); ok {
		if repo.Branch != br {
			return nil, fmt.Errorf("Lane checkout %s is on %q, not %q", p, repo.Branch, br)
		}
		co.Branch = &br
	} else if repo.Branch != "" {
		b := repo.Branch
		co.Branch = &b
	}
	return co, nil
}

// updateLanes applies an update's lanes operations in order. It returns
// the effective ledger, the new sidecar bytes (nil: unchanged), the result
// member and warnings.
func (e *Engine) updateLanes(id string, data ojson.Value, p *model.Plan) (lnView, *lanes.Ledger, []byte, *ojson.Value, []string, error) {
	v, err := e.loadLanes(id)
	if err != nil || !has(data, "lanes") {
		return v, v.ledger, nil, nil, nil, err
	}
	if v.exists && v.ledger == nil {
		return v, nil, nil, nil, nil, fmt.Errorf("Cannot change lanes: the lanes sidecar at %s is invalid (%s). Fix or move it aside first; it is never overwritten", v.art.Path, strings.Join(v.issues, "; "))
	}
	l := &lanes.Ledger{ID: id}
	if v.ledger != nil {
		cp := *v.ledger
		cp.Lanes = append([]lanes.Lane(nil), v.ledger.Lanes...)
		l = &cp
	}
	ix := index.Build(p)
	now := e.nowISO()
	source := e.evidenceSource()
	var warnings []string
	var touched []string
	for i, op := range getObjs(data, "lanes") {
		at := fmt.Sprintf("lanes.%d", i)
		kind, _ := getStr(op, "op")
		laneID, _ := getStr(op, "laneId")
		fail := func(format string, a ...any) error {
			return fmt.Errorf("Invalid lanes input: "+at+": "+format, a...)
		}
		ln := l.Find(laneID)
		switch kind {
		case "propose":
			if !lanes.IDPattern.MatchString(laneID) {
				return v, nil, nil, nil, nil, fail("laneId must be lowercase [a-z0-9-], at most 64: %s", laneID)
			}
			if ln != nil {
				return v, nil, nil, nil, nil, fail("lane %s already exists (%s); lane ids are not reused", laneID, ln.State)
			}
			nl := lanes.Lane{ID: laneID, State: lanes.Claimed, CreatedAt: now, UpdatedAt: now,
				History: []lanes.Event{{State: lanes.Claimed, At: now, Source: source}}}
			seen := map[model.StepRef]bool{}
			for _, sv := range getObjs(op, "steps") {
				var r model.StepRef
				r.PhaseID, _ = getStr(sv, "phaseId")
				r.StepID, _ = getStr(sv, "stepId")
				if _, ok := ix.Step(index.StepKey{PhaseID: r.PhaseID, StepID: r.StepID}); !ok {
					return v, nil, nil, nil, nil, fail("step %s/%s is not in the plan", r.PhaseID, r.StepID)
				}
				if owner := l.OwnerOf(r); owner != nil {
					return v, nil, nil, nil, nil, fail("step %s/%s already belongs to active lane %s", r.PhaseID, r.StepID, owner.ID)
				}
				if !seen[r] {
					seen[r] = true
					nl.Steps = append(nl.Steps, r)
				}
			}
			claims, _ := getList(op, "claims")
			for j, c := range claims {
				rel, err := e.normalizeClaim(fmt.Sprintf("%s.claims.%d", at, j), c)
				if err != nil {
					return v, nil, nil, nil, nil, errors.New("Invalid lanes input: " + err.Error())
				}
				nl.Claims = appendDedupe(nl.Claims, []string{rel})
			}
			base, dirty := e.laneBaseline()
			nl.Baseline = base
			if base.Dirty > 0 {
				warnings = append(warnings, fmt.Sprintf("Lane %s starts from a working state with %d uncommitted path(s) (for example %s); a worktree created from HEAD does not contain them — transfer them explicitly or commit first", laneID, base.Dirty, dirty[0]))
			}
			l.Lanes = append(l.Lanes, nl)
		case "transition":
			if ln == nil {
				return v, nil, nil, nil, nil, fail("lane %s does not exist", laneID)
			}
			to, _ := getStr(op, "state")
			if !lanes.CanTransition(ln.State, to) {
				return v, nil, nil, nil, nil, fail("lane %s cannot move from %s to %s (allowed: %s)", laneID, ln.State, to, strings.Join(lanes.Next(ln.State), ", "))
			}
			cv, hasCheckout := op.Get("checkout")
			if hasCheckout && to != lanes.Prepared {
				return v, nil, nil, nil, nil, fail("checkout is recorded when a lane becomes prepared")
			}
			if hasCheckout {
				co, err := e.checkoutFor(cv)
				if err != nil {
					return v, nil, nil, nil, nil, err
				}
				for _, other := range l.Lanes {
					if other.ID != laneID && lanes.Active(other.State) && other.Checkout != nil && other.Checkout.Path == co.Path {
						return v, nil, nil, nil, nil, fail("checkout %s is already used by active lane %s", co.Path, other.ID)
					}
				}
				ln.Checkout = co
			}
			if to == lanes.Merged {
				warnings = append(warnings, e.mergeWarnings(ln, ix)...)
			}
			ln.State, ln.UpdatedAt = to, now
			ln.History = append(ln.History, lanes.Event{State: to, At: now, Source: source})
		case "claims":
			if ln == nil || !lanes.Active(ln.State) {
				return v, nil, nil, nil, nil, fail("lane %s is not an active lane", laneID)
			}
			rm, _ := getList(op, "remove")
			for _, c := range rm {
				rel, err := e.normalizeClaim(at+".remove", c)
				if err != nil {
					return v, nil, nil, nil, nil, errors.New("Invalid lanes input: " + err.Error())
				}
				kept := ln.Claims[:0:0]
				for _, x := range ln.Claims {
					if x != rel {
						kept = append(kept, x)
					}
				}
				if len(kept) == len(ln.Claims) {
					return v, nil, nil, nil, nil, fail("lane %s does not claim %s", laneID, rel)
				}
				ln.Claims = kept
			}
			add, _ := getList(op, "add")
			for j, c := range add {
				rel, err := e.normalizeClaim(fmt.Sprintf("%s.add.%d", at, j), c)
				if err != nil {
					return v, nil, nil, nil, nil, errors.New("Invalid lanes input: " + err.Error())
				}
				ln.Claims = appendDedupe(ln.Claims, []string{rel})
			}
			ln.UpdatedAt = now
		}
		if _, cf := l.ClaimTrie(""); cf != nil {
			return v, nil, nil, nil, nil, fail("claim %s of lane %s overlaps %s claimed by lane %s", cf.Path, cf.By, cf.With, cf.Lane)
		}
		touched = appendDedupe(touched, []string{laneID})
	}
	l.UpdatedAt = now
	rv := make([]ojson.Value, 0, len(touched))
	for _, tid := range touched {
		ln := l.Find(tid)
		rv = append(rv, ojson.NewObject(5).
			Set("laneId", ojson.StringValue(ln.ID)).
			Set("state", ojson.StringValue(ln.State)).
			Set("steps", ojson.IntValue(int64(len(ln.Steps)))).
			Set("claims", ojson.StringsValue(orEmpty(ln.Claims))).
			Set("next", ojson.StringsValue(orEmpty(lanes.Next(ln.State)))).Value())
	}
	res := ojson.NewObject(2).Set("path", ojson.StringValue(v.rel)).Set("lanes", ojson.ArrayValue(rv)).Value()
	return v, l, lanes.Encode(l), &res, warnings, nil
}

// mergeWarnings: recording merged is the caller's statement; Shiori only
// checks what it can read.
func (e *Engine) mergeWarnings(ln *lanes.Lane, ix *index.Plan) []string {
	var out []string
	for _, r := range ln.Steps {
		if st, ok := ix.Step(index.StepKey{PhaseID: r.PhaseID, StepID: r.StepID}); ok && st.Status != "completed" && st.Status != "cancelled" {
			out = append(out, fmt.Sprintf("Lane %s is recorded merged while step %s/%s is %s", ln.ID, r.PhaseID, r.StepID, st.Status))
		}
	}
	if ln.Checkout == nil {
		return out
	}
	ctx := context.Background()
	repo, err := gitview.Open(ctx, ln.Checkout.Path)
	if err != nil || repo.Head == "" {
		return out
	}
	if ok, err := gitview.IsAncestor(ctx, e.Root, repo.Head, "HEAD"); err == nil && !ok {
		out = append(out, fmt.Sprintf("Lane %s head %s is not contained in the project HEAD; integrate it (or its reviewed patch) before relying on the merge", ln.ID, repo.Head[:12]))
	}
	return out
}

// laneTrees computes the working tree of every active lane checkout.
func (e *Engine) laneTrees(l *lanes.Ledger, scope []string) map[string]*gitview.Tree {
	out := map[string]*gitview.Tree{}
	if l == nil {
		return out
	}
	for _, ln := range l.Lanes {
		if !lanes.Active(ln.State) || ln.Checkout == nil {
			continue
		}
		if t, err := gitview.Snapshot(context.Background(), ln.Checkout.Path, snapshot.WorkplanDir, scope); err == nil {
			out[ln.ID] = t
		}
	}
	return out
}

// laneDoctor is doctor's per-plan lanes member.
func (e *Engine) laneDoctor(p *model.Plan, v lnView, g *index.Graph) ojson.Value {
	b := ojson.NewObject(6).
		Set("path", ojson.StringValue(v.rel)).
		Set("valid", ojson.BoolValue(v.ledger != nil))
	if v.ledger == nil {
		return b.Set("issues", ojson.StringsValue(v.issues)).Value()
	}
	l := v.ledger
	ix := index.Build(p)
	ctx := context.Background()
	prereqs := map[string][]string{}
	changedBy := map[string][]string{} // path -> active lanes changing it
	var items []ojson.Value
	for i := range l.Lanes {
		ln := &l.Lanes[i]
		var issues []string
		done := 0
		for _, r := range ln.Steps {
			k := index.StepKey{PhaseID: r.PhaseID, StepID: r.StepID}
			st, ok := ix.Step(k)
			if !ok {
				issues = append(issues, fmt.Sprintf("step %s/%s is not in the plan", r.PhaseID, r.StepID))
				continue
			}
			if st.Status == "completed" || st.Status == "cancelled" {
				done++
			}
			if g == nil || !lanes.Active(ln.State) {
				continue
			}
			for _, pr := range g.Prereqs(k) {
				if o := l.OwnerOf(model.StepRef{PhaseID: pr.Key.PhaseID, StepID: pr.Key.StepID}); o != nil && o.ID != ln.ID {
					prereqs[ln.ID] = appendDedupe(prereqs[ln.ID], []string{o.ID})
				}
			}
		}
		active := lanes.Active(ln.State)
		if active && len(ln.Steps) > 0 && done == len(ln.Steps) && ln.State != lanes.Integrating {
			issues = append(issues, "every step is completed but the lane is not integrated; integrate it or record its disposition")
		}
		lb := ojson.NewObject(10).
			Set("laneId", ojson.StringValue(ln.ID)).
			Set("state", ojson.StringValue(ln.State)).
			Set("steps", ojson.IntValue(int64(len(ln.Steps)))).
			Set("stepsDone", ojson.IntValue(int64(done))).
			Set("claims", ojson.StringsValue(orEmpty(ln.Claims)))
		if ln.Checkout == nil {
			lb.Set("checkout", ojson.NullValue())
		} else {
			cb := ojson.NewObject(5).Set("path", ojson.StringValue(ln.Checkout.Path)).Set("branch", ojson.NullableString(ln.Checkout.Branch))
			_, statErr := os.Stat(ln.Checkout.Path)
			exists := statErr == nil
			cb.Set("exists", ojson.BoolValue(exists))
			switch {
			case active && !exists:
				issues = append(issues, "checkout is missing; recover the work or abandon the lane")
			case !active && exists:
				issues = append(issues, "cleanup required: the "+ln.State+" lane's checkout still exists; remove it only after confirming nothing in it is unintegrated")
			}
			if exists && active {
				if repo, err := gitview.Open(ctx, ln.Checkout.Path); err == nil {
					if ln.Checkout.Branch != nil && repo.Branch != *ln.Checkout.Branch {
						issues = append(issues, fmt.Sprintf("checkout is on %q, not %q", repo.Branch, *ln.Checkout.Branch))
					}
					if j, _ := filepath.Glob(filepath.Join(ln.Checkout.Path, filepath.FromSlash(snapshot.WorkplanDir), "*.transaction.json")); len(j) > 0 {
						issues = append(issues, "a workplan transaction is pending inside the lane checkout; workplan writes belong to the parent root")
					}
					out, changed := e.outOfClaims(ln)
					for _, p := range changed {
						changedBy[p] = append(changedBy[p], ln.ID)
					}
					if out != nil {
						cb.Set("outsideClaims", *out)
						if n, _ := out.Get("count"); n.NumberLiteral() != "0" {
							issues = append(issues, "the checkout changes paths outside the lane's claims")
						}
					}
				}
			}
			lb.Set("checkout", cb.Value())
		}
		if pre := prereqs[ln.ID]; len(pre) > 0 {
			lb.Set("blockedBy", ojson.StringsValue(pre))
		}
		lb.Set("issues", ojson.StringsValue(orEmpty(issues)))
		items = append(items, lb.Value())
	}
	order, cycle := l.MergeOrder(prereqs)
	b.Set("lanes", ojson.ArrayValue(items)).
		Set("mergeOrder", ojson.StringsValue(orEmpty(order)))
	// Files two active lanes both change merge with conflicts, whatever
	// their claims say.
	var overlaps []ojson.Value
	for _, p := range sortedKeys(changedBy) {
		if ids := changedBy[p]; len(ids) > 1 && len(overlaps) < evidenceListCap {
			overlaps = append(overlaps, ojson.NewObject(2).Set("path", ojson.StringValue(p)).Set("lanes", ojson.StringsValue(ids)).Value())
		}
	}
	if len(overlaps) > 0 {
		b.Set("changeOverlaps", ojson.ArrayValue(overlaps))
	}
	if len(cycle) > 0 {
		b.Set("mergeCycle", ojson.StringsValue(cycle))
	}
	if wts, err := gitview.Worktrees(ctx, e.Root); err == nil {
		owned := map[string]bool{}
		for _, ln := range l.Lanes {
			if ln.Checkout != nil {
				owned[ln.Checkout.Path] = true
			}
		}
		var unowned []string
		for i, w := range wts {
			if rel, err := filepath.Rel(w.Path, e.Root); i == 0 || w.Bare || owned[w.Path] || (err == nil && !strings.HasPrefix(rel, "..")) {
				continue // the main worktree, and the one holding this root
			}
			if real, err := filepath.EvalSymlinks(w.Path); err == nil && owned[real] {
				continue
			}
			unowned = append(unowned, w.Path)
		}
		if len(unowned) > 0 {
			b.Set("unownedWorktrees", ojson.StringsValue(unowned))
		}
	}
	return b.Value()
}

// outOfClaims lists the paths a lane checkout changed since its baseline
// commit that none of its claims cover.
func (e *Engine) outOfClaims(ln *lanes.Lane) (*ojson.Value, []string) {
	if ln.Baseline.Head == nil {
		return nil, nil
	}
	ctx := context.Background()
	cur, err := gitview.Snapshot(ctx, ln.Checkout.Path, snapshot.WorkplanDir, nil)
	if err != nil {
		return nil, nil
	}
	base, err := gitview.HeadTree(ctx, ln.Checkout.Path, *ln.Baseline.Head, snapshot.WorkplanDir)
	if err != nil {
		return nil, nil
	}
	changed := base.Changed(cur)
	var out []string
	for _, p := range changed {
		if !lanes.Covers(ln.Claims, p) {
			out = append(out, p)
		}
	}
	shown := out
	if len(shown) > evidenceListCap {
		shown = shown[:evidenceListCap]
	}
	v := ojson.NewObject(2).Set("count", ojson.IntValue(int64(len(out)))).Set("paths", ojson.StringsValue(orEmpty(shown))).Value()
	return &v, changed
}

// stepLanes maps each step owned by an active lane to the lane.
func stepLanes(l *lanes.Ledger) map[model.StepRef]*lanes.Lane {
	out := map[model.StepRef]*lanes.Lane{}
	if l == nil {
		return out
	}
	for i := range l.Lanes {
		if lanes.Active(l.Lanes[i].State) {
			for _, r := range l.Lanes[i].Steps {
				out[r] = &l.Lanes[i]
			}
		}
	}
	return out
}

// laneRef is the compact lane reference inspect and resume show.
func laneRef(ln *lanes.Lane) ojson.Value {
	co := ojson.NullValue()
	if ln.Checkout != nil {
		co = ojson.StringValue(ln.Checkout.Path)
	}
	return ojson.NewObject(3).
		Set("laneId", ojson.StringValue(ln.ID)).
		Set("state", ojson.StringValue(ln.State)).
		Set("checkout", co).Value()
}

// resumeLanes is resume's compact lanes advice.
func resumeLanes(l *lanes.Ledger, cur *index.StepKey) ojson.Value {
	n := 0
	for _, ln := range l.Lanes {
		if lanes.Active(ln.State) {
			n++
		}
	}
	b := ojson.NewObject(2).Set("active", ojson.IntValue(int64(n)))
	if cur != nil {
		if ln := stepLanes(l)[model.StepRef{PhaseID: cur.PhaseID, StepID: cur.StepID}]; ln != nil {
			b.Set("current", laneRef(ln))
		}
	}
	return b.Value()
}

// LaneCheckout is where a lane's commands run: its checkout, or the
// project root for a lane that shares it.
func (e *Engine) LaneCheckout(planID, laneID string) (string, error) {
	id, err := normalizeRequested(planID)
	if err != nil {
		return "", err
	}
	v, err := e.loadLanes(id)
	if err != nil {
		return "", err
	}
	if v.ledger == nil {
		return "", fmt.Errorf("Plan %s has no valid lanes sidecar", id)
	}
	ln := v.ledger.Find(laneID)
	if ln == nil || !lanes.Active(ln.State) {
		return "", fmt.Errorf("%s is not an active lane of %s", laneID, id)
	}
	if ln.Checkout == nil {
		return e.Root, nil
	}
	return ln.Checkout.Path, nil
}
