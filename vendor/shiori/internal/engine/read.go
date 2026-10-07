package engine

import (
	"fmt"
	"strings"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Read implements workplan_read: the normalized document, an optional
// phase/step selection, the linked Markdown and the dependency view.
func (e *Engine) Read(in input.ReadInput) (ojson.Value, error) {
	id, err := normalizeRequested(in.ID)
	if err != nil {
		return ojson.Value{}, err
	}
	s, err := e.load(id)
	if err != nil {
		return ojson.Value{}, err
	}
	if s.Journal.Exists {
		return recoveryPacket(s, "read"), nil
	}
	p := s.Plan
	selection, err := selectPhases(p, in.PhaseID, in.StepID)
	if err != nil {
		return ojson.Value{}, err
	}
	filtered := in.PhaseID != nil || in.StepID != nil
	// A filtered read is a slice. Linked Markdown is included only on an
	// explicit includeMarkdown=true; an unfiltered read keeps the default
	// true.
	includeMD := in.IncludeMarkdown == nil || *in.IncludeMarkdown
	if filtered {
		includeMD = in.IncludeMarkdown != nil && *in.IncludeMarkdown
	}
	plan := ojson.NewObject(3).
		Set("path", ojson.StringValue(s.Markdown.Path)).
		Set("exists", ojson.BoolValue(s.Markdown.Exists))
	if includeMD {
		if s.Markdown.Exists {
			plan.Set("content", ojson.StringValue(bytesToString(s.Markdown.Bytes)))
		} else {
			plan.Set("content", ojson.NullValue())
		}
	}
	var out ojson.Value
	if !filtered {
		doc := p.ToValue()
		// includeNotes=false leaves notes out of a full read (they are
		// most of a long plan); absent keeps the reference output.
		dropNotes := in.IncludeNotes != nil && !*in.IncludeNotes
		if dropNotes {
			doc = withoutMember(doc, "notes")
		}
		ob := ojson.NewObject(8).
			Set("path", ojson.StringValue(s.JSON.Path)).
			Set("workplan", doc).
			Set("selection", selection).
			Set("plan", plan.Value()).
			Set("dependencies", e.dependencies(s, nil).value()).
			Set("planHash", ojson.StringValue(s.PlanHash)).
			Set("stateHash", ojson.StringValue(s.StateHash))
		if dropNotes {
			ob.Set("notesOmitted", ojson.NewObject(2).
				Set("count", ojson.IntValue(int64(len(p.Notes)))).
				Set("read", ojson.StringValue("workplan_read id="+p.ID)).Value())
		}
		out = ob.Value()
	} else {
		notes := in.IncludeNotes != nil && *in.IncludeNotes
		out = ojson.NewObject(8).
			Set("path", ojson.StringValue(s.JSON.Path)).
			Set("workplan", planHeader(p, notes)).
			Set("selection", selection).
			Set("plan", plan.Value()).
			Set("dependencies", e.dependencies(s, nil).sliceValue(selectedSteps(p, in.PhaseID, in.StepID))).
			Set("planHash", ojson.StringValue(s.PlanHash)).
			Set("stateHash", ojson.StringValue(s.StateHash)).
			Set("slice", ojson.NewObject(9).
				Set("filtered", ojson.BoolValue(true)).
				Set("phaseCount", ojson.IntValue(int64(len(p.Phases)))).
				Set("stepCount", ojson.IntValue(int64(p.StepCount()))).
				Set("findingCount", ojson.IntValue(int64(len(p.Findings)))).
				Set("noteCount", ojson.IntValue(int64(len(p.Notes)))).
				Set("notesIncluded", ojson.BoolValue(notes)).
				Set("markdownIncluded", ojson.BoolValue(includeMD)).
				Set("dependenciesFiltered", ojson.BoolValue(true)).
				Set("full", ojson.StringValue("workplan_read id="+p.ID)).Value()).
			Value()
	}
	// The reference returns unbounded output; Go refuses above the
	// response frame limit. Estimate cheaply before measuring exactly.
	est := 3*len(s.JSON.Bytes) + 2*len(s.Markdown.Bytes)
	if est > e.maxResponse()/2 {
		return e.checkResponse(out, "workplan_read", id)
	}
	return out, nil
}

// planHeader is the document without its phases (the selection carries
// the selected slice) and, unless includeNotes, without reviewFindings and
// notes. Every other member, including unknown metadata, is kept in order.
func planHeader(p *model.Plan, includeNotes bool) ojson.Value {
	ms := p.ToValue().Members()
	kept := make([]ojson.Member, 0, len(ms))
	for _, m := range ms {
		switch m.Key {
		case "phases":
			continue
		case "reviewFindings", "notes":
			if !includeNotes {
				continue
			}
		}
		kept = append(kept, m)
	}
	return ojson.ObjectValue(kept)
}

// selectedSteps lists the (phaseId, stepId) keys of a valid selection.
func selectedSteps(p *model.Plan, phaseID, stepID *string) map[model.StepRef]bool {
	out := map[model.StepRef]bool{}
	for i := range p.Phases {
		ph := &p.Phases[i]
		if phaseID != nil && ph.ID != *phaseID {
			continue
		}
		for j := range ph.Steps {
			if stepID != nil && ph.Steps[j].ID != *stepID {
				continue
			}
			out[model.StepRef{PhaseID: ph.ID, StepID: ph.Steps[j].ID}] = true
		}
	}
	return out
}

// bytesToString decodes UTF-8 with U+FFFD replacement (TextDecoder).
func bytesToString(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

func selectPhases(p *model.Plan, phaseID, stepID *string) (ojson.Value, error) {
	var phases []ojson.Value
	candidates := make([]int, 0, len(p.Phases))
	if phaseID != nil {
		for i := range p.Phases {
			if p.Phases[i].ID == *phaseID {
				candidates = append(candidates, i)
				break
			}
		}
		if len(candidates) == 0 {
			return ojson.Value{}, fmt.Errorf("Phase not found: %s", *phaseID)
		}
	} else {
		for i := range p.Phases {
			candidates = append(candidates, i)
		}
	}
	if stepID == nil {
		for _, i := range candidates {
			phases = append(phases, p.Phases[i].ToValue())
		}
	} else {
		matches := 0
		for _, i := range candidates {
			ph := p.Phases[i]
			var kept []model.Step
			for _, st := range ph.Steps {
				if st.ID == *stepID {
					kept = append(kept, st)
				}
			}
			if len(kept) > 0 {
				matches += len(kept)
				ph.Steps = kept
				phases = append(phases, ph.ToValue())
			}
		}
		if matches != 1 {
			return ojson.Value{}, fmt.Errorf("Step filter must identify exactly one step: %s", *stepID)
		}
	}
	return ojson.NewObject(3).
		Set("phaseId", ojson.NullableString(phaseID)).
		Set("stepId", ojson.NullableString(stepID)).
		Set("phases", ojson.ArrayValue(phases)).Value(), nil
}

// withoutMember is an object without one member (order kept).
func withoutMember(v ojson.Value, key string) ojson.Value {
	var ms []ojson.Member
	for _, m := range v.Members() {
		if m.Key != key {
			ms = append(ms, m)
		}
	}
	return ojson.ObjectValue(ms)
}
