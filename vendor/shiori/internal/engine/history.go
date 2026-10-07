package engine

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hoshinoht/shiori/internal/evidence"

	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

func historyRel(id string) string { return snapshot.SidecarRel(id, history.Suffix) }

// source is the write's origin recorded in the change log.
func (e *Engine) source() string {
	if e.Source != "" {
		return e.Source
	}
	return history.SourceAgent
}

// logged attaches the change-log entry to a prepared write and finalizes
// it. pre is the state the write was prepared on (nil: none); post is the
// plan it writes (nil: reload it from the intent).
func (e *Engine) logged(prep *Prepared, pre *snapshot.Snapshot, post *model.Plan, extra ...history.Change) *Prepared {
	in := prep.Intent
	if in == nil || e.NoHistory {
		return finalize(prep)
	}
	en := history.Entry{At: in.CreatedAt, Op: in.Operation, Source: e.source(), Tx: in.TransactionID}
	var before *model.Plan
	if pre != nil {
		en.Before = &history.Hashes{PlanHash: pre.PlanHash, StateHash: pre.StateHash}
		before = pre.Plan
	}
	known := false
	if pre != nil && post != nil {
		if ph, sh, ok := e.postHashes(pre, in, post); ok {
			en.After, known = &history.Hashes{PlanHash: ph, StateHash: sh}, true
		}
	}
	if !known {
		post = nil
		if ps, err := e.postSnapshot(in, in.WorkplanID); err == nil {
			en.After, post = &history.Hashes{PlanHash: ps.PlanHash, StateHash: ps.StateHash}, ps.Plan
		}
	}
	en.Changes = history.Diff(before, post)
	en.Changes = append(en.Changes, extra...)
	for _, t := range in.Targets {
		switch t.Kind {
		case "dependencies", "checkpoint", "evidence", "lanes", "links":
			op := "changed"
			if !t.BeforeExists {
				op = "added"
			} else if !t.AfterExists {
				op = "removed"
			}
			en.Changes = append(en.Changes, history.Change{Path: t.Kind, Op: op})
		}
	}
	in.History = &storage.Append{Rel: historyRel(in.WorkplanID), Payload: history.Payload(en), RotateAt: history.RotateAt}
	// A log near the rotation size names its archive now, so the intent
	// lists every path the commit may write.
	if st, err := os.Stat(e.absRel(in.History.Rel)); err == nil && st.Size() >= history.RotateAt-history.RotateMargin {
		in.History.ArchiveRel = snapshot.WorkplanDir + "/archive/" + in.WorkplanID + "/history-" +
			strings.NewReplacer("-", "", ":", "").Replace(in.CreatedAt) + "-" + in.TransactionID[:8] + ".jsonl"
	}
	return finalize(prep)
}

// rebase is a stale update applied over newer writes the change log
// connects (contracts §24).
type rebase struct {
	from, current string
	entries       []history.Entry
}

// loadForUpdate is loadForMutation, except that a stale expectedHash
// with rebase requested loads the current state when the change log
// connects the two hashes.
func (e *Engine) loadForUpdate(raw string, data ojson.Value) (*snapshot.Snapshot, *rebase, error) {
	expected := optHash(data)
	s, err := e.loadForMutation(raw, expected)
	var stale *StaleHashError
	if err == nil || !errors.As(err, &stale) || !e.rebaseWanted(data) {
		return s, nil, err
	}
	if s, err = e.loadForMutation(raw, nil); err != nil {
		return nil, nil, err
	}
	l, err := history.Load(e.absRel(historyRel(s.ID)))
	if err != nil {
		return nil, nil, &StaleHashError{Current: s.StateHash, NotRebased: "the change log is unreadable"}
	}
	entries, ok := l.Since(*expected, s.StateHash)
	if !ok {
		return nil, nil, &StaleHashError{Current: s.StateHash, NotRebased: "the change log does not connect expectedHash to it (a write outside Shiori, or one older than the log)"}
	}
	return s, &rebase{from: *expected, current: s.StateHash, entries: entries}, nil
}

func (e *Engine) rebaseWanted(data ojson.Value) bool {
	if v, ok := data.Get("rebase"); ok {
		return v.Bool()
	}
	return e.Rebase
}

// check refuses the rebase when this write changes an element a newer
// write changed.
func (rb *rebase) check(cur, next *model.Plan, targets []targetSpec, extra []history.Change) error {
	if rb == nil {
		return nil
	}
	ours := append(history.Diff(cur, next), extra...)
	for _, t := range targets {
		switch t.kind {
		case "evidence", "lanes", "links":
			ours = append(ours, history.Change{Path: t.kind, Op: "changed"})
		}
	}
	c, by, bad := history.Conflict(ours, rb.entries)
	if !bad {
		return nil
	}
	return &StaleHashError{Current: rb.current, NotRebased: c.Path + " was also changed by " + by.Op + " (" + by.Source + ", " + by.At + ")"}
}

func (rb *rebase) result(b *ojson.Builder) {
	if rb == nil {
		return
	}
	writes := make([]ojson.Value, len(rb.entries))
	for i, en := range rb.entries {
		writes[i] = ojson.NewObject(4).
			Set("seq", ojson.IntValue(en.Seq)).
			Set("op", ojson.StringValue(en.Op)).
			Set("source", ojson.StringValue(en.Source)).
			Set("at", ojson.StringValue(en.At)).Value()
	}
	b.Set("rebased", ojson.NewObject(2).
		Set("fromHash", ojson.StringValue(rb.from)).
		Set("over", ojson.ArrayValue(writes)).Value())
}

// sinceCheckpoint summarizes the logged writes after the newest logged
// checkpoint, when the log reaches the current state (nil otherwise).
func (e *Engine) sinceCheckpoint(s *snapshot.Snapshot) *ojson.Value {
	if e.NoHistory {
		return nil
	}
	l, err := history.Load(e.absRel(historyRel(s.ID)))
	if err != nil || len(l.Entries) == 0 {
		return nil
	}
	last := -1
	for i := len(l.Entries) - 1; i >= 0; i-- {
		if l.Entries[i].Op == "checkpoint" {
			last = i
			break
		}
	}
	if last < 0 || l.Entries[last].After == nil {
		return nil
	}
	entries, ok := l.Since(l.Entries[last].After.StateHash, s.StateHash)
	if !ok || len(entries) == 0 {
		return nil
	}
	v := summarizeWrites(entries)
	return &v
}

// sinceStepCap bounds the step transitions listed.
const sinceStepCap = 8

// summarizeWrites folds entries into counts and per-step first→last statuses.
func summarizeWrites(entries []history.Entry) ojson.Value {
	type move struct{ path, from, to string }
	var order []string
	moves := map[string]*move{}
	var notes, findingsAdded, findingsResolved int
	sources := map[string]bool{}
	var srcs []string
	for _, en := range entries {
		if !sources[en.Source] {
			sources[en.Source] = true
			srcs = append(srcs, en.Source)
		}
		for _, c := range en.Changes {
			switch {
			case c.Path == "notes" && c.Op == "appended":
				notes += c.Count
			case strings.HasPrefix(c.Path, "findings/") && c.Op == "added":
				findingsAdded++
			case strings.HasPrefix(c.Path, "findings/") && c.To == "resolved":
				findingsResolved++
			case strings.Contains(c.Path, "/steps/") && c.From != c.To:
				if m, ok := moves[c.Path]; ok {
					m.to = c.To
				} else {
					moves[c.Path] = &move{c.Path, c.From, c.To}
					order = append(order, c.Path)
				}
			}
		}
	}
	steps := []ojson.Value{}
	changed := 0
	for _, p := range order {
		m := moves[p]
		if m.from == m.to {
			continue
		}
		if changed++; changed > sinceStepCap {
			continue
		}
		parts := strings.Split(p, "/") // phases/<p>/steps/<s>
		b := ojson.NewObject(4).Set("phaseId", ojson.StringValue(parts[1])).Set("stepId", ojson.StringValue(parts[3]))
		b.Set("from", ojson.StringValue(orNone(m.from))).Set("to", ojson.StringValue(orNone(m.to)))
		steps = append(steps, b.Value())
	}
	return ojson.NewObject(8).
		Set("writes", ojson.IntValue(int64(len(entries)))).
		Set("sources", ojson.StringsValue(srcs)).
		Set("since", ojson.StringValue(entries[0].At)).
		Set("steps", ojson.ArrayValue(steps)).
		Set("stepsChanged", ojson.IntValue(int64(changed))).
		Set("findingsAdded", ojson.IntValue(int64(findingsAdded))).
		Set("findingsResolved", ojson.IntValue(int64(findingsResolved))).
		Set("notesAppended", ojson.IntValue(int64(notes))).Value()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// StallAfter is how long a step may stay in_progress, per the change
// log, without new evidence before doctor reports it.
const StallAfter = 72 * time.Hour

// historyDoctor reports the change log of a plan that has one: its
// extent, whether it reaches the current state, and stalled steps.
func (e *Engine) historyDoctor(s *snapshot.Snapshot) (ojson.Value, bool) {
	rel := historyRel(s.ID)
	if _, err := os.Lstat(e.absRel(rel)); err != nil {
		return ojson.Value{}, false
	}
	b := ojson.NewObject(7).Set("path", ojson.StringValue(e.absRel(rel)))
	l, err := history.Load(e.absRel(rel))
	if err != nil {
		return b.Set("issues", ojson.StringsValue([]string{err.Error()})).Value(), true
	}
	current := false
	var lastSeq int64
	for i := len(l.Entries) - 1; i >= 0; i-- {
		en := l.Entries[i]
		if lastSeq == 0 {
			lastSeq = en.Seq
		}
		if en.After != nil {
			current = en.After.StateHash == s.StateHash
			break
		}
	}
	b.Set("entries", ojson.IntValue(int64(len(l.Entries)))).
		Set("lastSeq", ojson.IntValue(lastSeq)).
		Set("reachesCurrentState", ojson.BoolValue(current)).
		Set("truncated", ojson.BoolValue(l.Truncated)).
		Set("issues", ojson.StringsValue(orEmpty(l.Issues)))
	if stalled := e.stalledSteps(s.Plan, l); len(stalled) > 0 {
		b.Set("stalledSteps", ojson.ArrayValue(stalled))
	}
	return b.Value(), true
}

// statusSince maps each step path to when the log last saw its status
// change.
func statusSince(l *history.Log) map[string]string {
	out := map[string]string{}
	for _, en := range l.Entries {
		for _, c := range en.Changes {
			if strings.Contains(c.Path, "/steps/") && (c.From != c.To || c.Op == "added") {
				out[c.Path] = en.At
			}
		}
	}
	return out
}

// stalledSteps lists in_progress steps whose status is older than
// StallAfter with no evidence recorded since.
func (e *Engine) stalledSteps(p *model.Plan, l *history.Log) []ojson.Value {
	since := statusSince(l)
	now := Clock()
	var ledger *evidence.Ledger
	if ev, err := e.loadEvidence(p.ID); err == nil {
		ledger = ev.ledger
	}
	var out []ojson.Value
	for _, ph := range p.Phases {
		for _, st := range ph.Steps {
			if st.Status != "in_progress" {
				continue
			}
			at, ok := since["phases/"+ph.ID+"/steps/"+st.ID]
			t, err := time.Parse(time.RFC3339, at)
			if !ok || err != nil || now.Sub(t) < StallAfter {
				continue
			}
			fresh := false
			if ledger != nil {
				for _, r := range ledger.Records {
					rt, err := time.Parse(time.RFC3339, r.RecordedAt)
					if r.PhaseID == ph.ID && r.StepID == st.ID && err == nil && !rt.Before(t) {
						fresh = true
					}
				}
			}
			if !fresh {
				out = append(out, ojson.NewObject(3).
					Set("phaseId", ojson.StringValue(ph.ID)).
					Set("stepId", ojson.StringValue(st.ID)).
					Set("inProgressSince", ojson.StringValue(at)).Value())
			}
		}
	}
	return out
}

// History is the change log of a plan, newest last: at most limit
// entries (0: all readable), optionally only those after state hash since.
func (e *Engine) History(rawID, since string, limit int) (ojson.Value, error) {
	id, err := normalizeRequested(rawID)
	if err != nil {
		return ojson.Value{}, err
	}
	l, err := history.Load(e.absRel(historyRel(id)))
	if err != nil {
		return ojson.Value{}, err
	}
	entries := l.Entries
	if since != "" {
		at := -1
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].After != nil && entries[i].After.StateHash == since {
				at = i
				break
			}
		}
		if at < 0 {
			return ojson.Value{}, errors.New("State hash " + since + " is not in the readable change log of " + id)
		}
		entries = entries[at+1:]
	}
	omitted := 0
	if limit > 0 && len(entries) > limit {
		omitted = len(entries) - limit
		entries = entries[omitted:]
	}
	vs := make([]ojson.Value, len(entries))
	for i, en := range entries {
		vs[i] = en.Value()
	}
	b := ojson.NewObject(6).
		Set("id", ojson.StringValue(id)).
		Set("path", ojson.StringValue(e.absRel(historyRel(id)))).
		Set("entries", ojson.ArrayValue(vs)).
		Set("omitted", ojson.IntValue(int64(omitted))).
		Set("truncated", ojson.BoolValue(l.Truncated)).
		Set("issues", ojson.StringsValue(orEmpty(l.Issues)))
	if s, err := e.load(id); err == nil {
		b.Set("currentStateHash", ojson.StringValue(s.StateHash))
		if len(entries) > 0 {
			sum := summarizeWrites(entries)
			b.Set("summary", sum)
		}
	}
	return b.Value(), nil
}

// PlanIDs lists the canonical plan ids in the workplan root.
func (e *Engine) PlanIDs() ([]string, error) {
	l, err := e.scanDir()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range l.primary {
		if id, err := model.NormalizeID(name); err == nil && id == name {
			out = append(out, id)
		}
	}
	return out, nil
}

// ChangeToken changes whenever a plan's state or its evidence, lanes or
// change log changes (reads through the cache; for change notifications).
func (e *Engine) ChangeToken(id string) string {
	tok := "missing"
	if s, err := e.load(id); err == nil {
		tok = s.StateHash
	}
	for _, rel := range []string{evidenceRel(id), lanesRel(id), historyRel(id)} {
		if st, err := os.Stat(e.absRel(rel)); err == nil {
			tok += fmt.Sprintf(" %d.%d", st.Size(), st.ModTime().UnixNano())
		} else {
			tok += " -"
		}
	}
	return tok
}
