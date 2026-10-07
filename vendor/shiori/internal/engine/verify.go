package engine

import (
	"context"
	"sort"

	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/gitview"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Rerun is a recorded command `shiori verify` may repeat.
type Rerun struct {
	PhaseID, StepID string
	Command         string
	State           string // the record's state now
	Scope           []string
	Lane            *string
}

// EvidenceReruns lists the latest record of each command for steps whose
// evidence is stale or failing (every step with records when all is set).
// Only commands the operator ran through `shiori evidence -- COMMAND`
// (source cli-run) are returned; skipped names the others, which are
// never re-run because their text came from elsewhere.
func (e *Engine) EvidenceReruns(rawID string, all bool) (runs []Rerun, skipped []string, err error) {
	id, err := normalizeRequested(rawID)
	if err != nil {
		return nil, nil, err
	}
	s, err := e.load(id)
	if err != nil {
		return nil, nil, err
	}
	ev, err := e.loadEvidence(id)
	if err != nil {
		return nil, nil, err
	}
	if len(ev.issues) > 0 {
		return nil, nil, msgEvidenceInvalid(ev)
	}
	if ev.ledger == nil {
		return nil, nil, nil
	}
	views := ev.ledger.Views(e.currentTree(ev.ledger, e.lanesOf(id), nil))
	for _, ref := range planOrder(s.Plan, views) {
		v := views[ref]
		for _, c := range v.Commands {
			if !all && c.State != evidence.StateStale && c.State != evidence.StateFailing {
				continue
			}
			if c.Record.Source != evidence.SourceCLIRun {
				skipped = append(skipped, ref.PhaseID+"/"+ref.StepID+": "+c.Record.Command+" (recorded by "+c.Record.Source+")")
				continue
			}
			r := Rerun{PhaseID: ref.PhaseID, StepID: ref.StepID, Command: c.Record.Command, State: c.State, Lane: c.Record.Lane}
			for _, sc := range c.Record.Scope {
				r.Scope = append(r.Scope, sc.Path)
			}
			runs = append(runs, r)
		}
	}
	return runs, skipped, nil
}

// planOrder is the views' steps in plan order, unknown steps last.
func planOrder(p *model.Plan, views map[model.StepRef]*evidence.StepView) []model.StepRef {
	pos := map[model.StepRef]int{}
	n := 0
	for _, ph := range p.Phases {
		for _, st := range ph.Steps {
			pos[model.StepRef{PhaseID: ph.ID, StepID: st.ID}] = n
			n++
		}
	}
	var out []model.StepRef
	for ref := range views {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		a, aok := pos[out[i]]
		b, bok := pos[out[j]]
		if aok != bok {
			return aok
		}
		if aok {
			return a < b
		}
		return out[i].PhaseID+"/"+out[i].StepID < out[j].PhaseID+"/"+out[j].StepID
	})
	return out
}

// CommitLink is the commit whose content a passing record tested.
type CommitLink struct {
	Commit string
	Match  string // "tree": the whole tree; "scope": the record's scope paths
}

// EvidenceCommits links each step's latest passing records to the newest
// commits (up to n from HEAD) whose content is what the record tested.
// Over a run of commits with that content the oldest is named (the one
// that introduced it). Read-only git; no git work tree gives no links.
func (e *Engine) EvidenceCommits(ctx context.Context, id string, n int) (map[model.StepRef]map[string]CommitLink, error) {
	ev, err := e.loadEvidence(id)
	if err != nil || ev.ledger == nil || len(ev.issues) > 0 {
		return nil, err
	}
	commits, err := gitview.Commits(ctx, e.Root, snapshot.WorkplanDir, n)
	if err != nil {
		return nil, nil // not a repository, or no commits yet: no links
	}
	out := map[model.StepRef]map[string]CommitLink{}
	views := ev.ledger.Views(evidence.Current{})
	for ref, v := range views {
		for _, c := range v.Commands {
			r := c.Record
			if !r.Passed() || r.TreeOID == nil || r.Lane != nil {
				continue
			}
			match := func(cm gitview.Commit) bool {
				if len(r.Scope) == 0 {
					return cm.ContentOID == *r.TreeOID
				}
				for _, sc := range r.Scope {
					d := cm.Tree.Digest(sc.Path)
					if (d == nil) != (sc.Digest == nil) || d != nil && *d != *sc.Digest {
						return false
					}
				}
				return true
			}
			found := -1
			for i, cm := range commits {
				if match(cm) {
					found = i
				} else if found >= 0 {
					break
				}
			}
			if found < 0 {
				continue
			}
			kind := "tree"
			if len(r.Scope) > 0 {
				kind = "scope"
			}
			if out[ref] == nil {
				out[ref] = map[string]CommitLink{}
			}
			out[ref][r.Command] = CommitLink{Commit: commits[found].Hash, Match: kind}
		}
	}
	return out, nil
}

// CurrentStateHash is a plan's state hash, read fresh.
func (e *Engine) CurrentStateHash(rawID string) (string, error) {
	id, err := normalizeRequested(rawID)
	if err != nil {
		return "", err
	}
	s, err := e.loadFresh(id)
	if err != nil {
		return "", err
	}
	return s.StateHash, nil
}
