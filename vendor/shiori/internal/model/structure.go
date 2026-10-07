package model

import (
	"fmt"
	"strconv"
)

// ValidateStructure applies the executable-structure rules
// (plan-v2.schema.json x-shiori-structure-rules) to a decoded plan and
// returns "path: message" strings in the reference order. expectedID, when
// non-nil, is the raw filename/requested id that the stored id must match
// after normalization.
func ValidateStructure(p *Plan, expectedID *string) []string {
	var out []string
	add := func(path, msg string) { out = append(out, path+": "+msg) }

	if Blank(p.ID) {
		add("id", "Must not be empty")
	} else if expectedID != nil {
		want, err := NormalizeID(*expectedID)
		if err != nil {
			want = ""
		}
		if p.ID != want {
			add("id", "Does not match filename id "+want)
		}
	}
	if Blank(p.Kind) {
		add("kind", "Must not be empty")
	}
	if Blank(p.Goal) {
		add("goal", "Must not be empty")
	}
	if Blank(p.PlanFile) {
		add("planFile", "Must not be empty")
	}
	if !ValidDatetime(p.CreatedAt) {
		add("createdAt", "Invalid datetime")
	}
	if !ValidDatetime(p.UpdatedAt) {
		add("updatedAt", "Invalid datetime")
	}
	if len(p.Phases) == 0 {
		add("phases", "At least one phase is required")
	}
	seenPhase := map[string]bool{}
	for i := range p.Phases {
		ph := &p.Phases[i]
		pp := "phases." + strconv.Itoa(i)
		if Blank(ph.ID) {
			add(pp+".id", "Must not be empty")
		} else if seenPhase[ph.ID] {
			add(pp+".id", "Duplicate phase id "+ph.ID)
		} else {
			seenPhase[ph.ID] = true
		}
		if Blank(ph.Title) {
			add(pp+".title", "Must not be empty")
		}
		if len(ph.Steps) == 0 {
			add(pp+".steps", "At least one step is required")
		}
		seenStep := map[string]bool{}
		for j := range ph.Steps {
			st := &ph.Steps[j]
			sp := pp + ".steps." + strconv.Itoa(j)
			if Blank(st.ID) {
				add(sp+".id", "Must not be empty")
			} else if seenStep[st.ID] {
				add(sp+".id", fmt.Sprintf("Duplicate step id %s in phase %s", st.ID, ph.ID))
			} else {
				seenStep[st.ID] = true
			}
			if Blank(st.Title) {
				add(sp+".title", "Must not be empty")
			}
			if st.Action == nil || Blank(*st.Action) {
				add(sp+".action", "Required for executable workplans")
			}
			if st.Validation == nil || Blank(*st.Validation) {
				add(sp+".validation", "Required for executable workplans")
			}
		}
	}
	return out
}

// UniqueIDError returns the reference refusal for targeted operations
// (inspect/resume) on a plan whose phase or step ids are ambiguous, or nil.
func UniqueIDError(p *Plan) error {
	seenPhase := map[string]bool{}
	for i := range p.Phases {
		ph := &p.Phases[i]
		if seenPhase[ph.ID] {
			return fmt.Errorf("Workplan %s has duplicate phase id: %s. Migrate ids before targeted inspect or update calls.", p.ID, ph.ID)
		}
		seenPhase[ph.ID] = true
		seenStep := map[string]bool{}
		for j := range ph.Steps {
			if seenStep[ph.Steps[j].ID] {
				return fmt.Errorf("Workplan %s has duplicate step id in phase %s: %s. Migrate ids before targeted inspect or update calls.", p.ID, ph.ID, ph.Steps[j].ID)
			}
			seenStep[ph.Steps[j].ID] = true
		}
	}
	return nil
}
