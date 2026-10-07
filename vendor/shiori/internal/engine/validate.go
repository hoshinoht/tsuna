package engine

import (
	"fmt"
	"strings"

	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// validationIssues is the ordered issue list for workplan_validate:
// structure rules, linked Markdown, linked specs, dependency metadata and
// pending recovery.
func (e *Engine) validationIssues(s *snapshot.Snapshot, requested string) ([]string, depView) {
	p := s.Plan
	issues := model.ValidateStructure(p, &requested)
	if !s.Markdown.Exists || model.Blank(string(s.Markdown.Bytes)) {
		issues = append(issues, "planFile: Linked Markdown is missing or empty: "+p.PlanFile)
	}
	for _, sp := range s.Specs {
		if !sp.Exists || model.Blank(string(sp.Bytes)) {
			issues = append(issues, "specFiles: Linked spec is missing or empty: "+sp.Rel)
		}
	}
	for _, bp := range s.BackslashPaths {
		// The manifest rewrites '\' to '/' for hash parity,
		// which can alias a different file. The plan stays readable; the
		// ambiguity is diagnosed here and writers refuse new such links.
		field := "specFiles"
		if bp == p.PlanFile {
			field = "planFile"
		}
		issues = append(issues, field+": Linked path contains a backslash and has an ambiguous manifest identity: "+bp)
	}
	dv := e.dependencies(s, nil)
	for _, is := range dv.issues {
		issues = append(issues, "dependencies: "+is)
	}
	if s.Journal.Exists {
		issues = append(issues, "transaction: Recovery required at "+s.Journal.Rel)
	}
	if issues == nil {
		issues = []string{}
	}
	return issues, dv
}

// msgMarkdownDrift is the non-failing warning for linked Markdown that is
// not the generated rendering of the plan JSON.
const msgMarkdownDrift = "planFile: Linked Markdown is not the generated rendering of the plan JSON (handwritten or edited): %s. " +
	"Later JSON changes (status, notes, findings, compaction) are not written into it. " +
	"To regenerate it from the JSON (discarding the edits) use workplan_reset with mode \"markdown-only\" and replaceMarkdown=true; otherwise keep it in sync by hand."

// validationWarnings are non-failing diagnostics; they never change
// "valid": Markdown drift and then the dependency-graph warnings (order
// violations, cancelled prerequisites, backward links, empty entries). A
// missing or empty Markdown is an issue, not a warning, and a plan whose
// ids cannot render is diagnosed by the structure rules.
func (e *Engine) validationWarnings(s *snapshot.Snapshot, dv depView) []string {
	var w []string
	if s.Markdown.Exists && !model.Blank(string(s.Markdown.Bytes)) {
		if gen, err := e.isGeneratedCached(s.Plan, s.JSON.SHA256, s.Markdown.SHA256, s.Markdown.Bytes); err == nil && !gen {
			w = append(w, fmt.Sprintf(msgMarkdownDrift, s.Markdown.Rel))
			if m := e.missingStepMarkers(s); m != "" {
				w = append(w, m)
			}
		}
	}
	if dv.deps != nil && len(dv.issues) == 0 {
		w = append(w, graphWarnings(e.graph(index.Build(s.Plan), dv))...)
	}
	return w
}

// Validate implements workplan_validate. Load failures are reported as a
// single issue instead of an error.
func (e *Engine) Validate(in input.ValidateInput) (ojson.Value, error) {
	id, err := normalizeRequested(in.ID)
	if err != nil {
		return ojson.Value{}, err
	}
	s, err := e.load(id)
	if err != nil {
		if IsUnsupported(err) {
			return ojson.Value{}, err
		}
		b := ojson.NewObject(5).
			Set("valid", ojson.BoolValue(false)).
			Set("issueCount", ojson.IntValue(1)).
			Set("issues", ojson.StringsValue([]string{err.Error()}))
		// The raw-byte hashes of an unreadable plan, so workplan_create
		// overwrite can repair it.
		if u := e.unreadable(id, err); u != nil {
			b.Set("planHash", ojson.StringValue(u.PlanHash)).
				Set("stateHash", ojson.StringValue(u.StateHash))
		}
		return b.Value(), nil
	}
	issues, dv := e.validationIssues(s, in.ID)
	b := ojson.NewObject(11).
		Set("path", ojson.StringValue(s.JSON.Path)).
		Set("planPath", ojson.StringValue(s.Markdown.Path)).
		Set("valid", ojson.BoolValue(len(issues) == 0)).
		Set("issueCount", ojson.IntValue(int64(len(issues)))).
		Set("issues", ojson.StringsValue(issues))
	// Additive, present only when there is something to report, so
	// outputs without warnings stay byte-identical to the reference.
	if w := e.validationWarnings(s, dv); len(w) > 0 {
		b.Set("warnings", ojson.StringsValue(w))
	}
	return b.
		Set("workplan", s.Plan.Summary()).
		Set("planHash", ojson.StringValue(s.PlanHash)).
		Set("stateHash", ojson.StringValue(s.StateHash)).
		Set("planFresh", ojson.BoolValue(len(s.MissingPlanArtifacts) == 0)).
		Set("dependenciesRecorded", ojson.BoolValue(dv.recorded)).Value(), nil
}

// maxMarkerList bounds the steps named in the missing-marker warning.
const maxMarkerList = 10

// missingStepMarkers is the non-failing warning for handwritten Markdown
// that lacks a step's "<!-- workplan-step-id: <id> -->" marker. Generated
// Markdown always has them. Shiori never rewrites handwritten Markdown to
// add them.
func (e *Engine) missingStepMarkers(s *snapshot.Snapshot) string {
	md := string(s.Markdown.Bytes)
	var missing []string
	total := 0
	for i := range s.Plan.Phases {
		ph := &s.Plan.Phases[i]
		for j := range ph.Steps {
			total++
			marker, err := model.StepMarker(ph.Steps[j].ID)
			if err != nil || strings.Contains(md, marker) {
				continue
			}
			missing = append(missing, ph.ID+"/"+ph.Steps[j].ID)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	listed := missing
	more := ""
	if len(listed) > maxMarkerList {
		listed = listed[:maxMarkerList]
		more = fmt.Sprintf(" and %d more", len(missing)-maxMarkerList)
	}
	return fmt.Sprintf("planFile: Linked Markdown %s has no step marker (<!-- workplan-step-id: <stepId> -->) for %d of %d steps: %s%s. "+
		"Tools and readers that locate a step's section by its marker cannot find these steps. Add the markers by hand; Shiori never rewrites handwritten Markdown.",
		s.Markdown.Rel, len(missing), total, strings.Join(listed, ", "), more)
}
