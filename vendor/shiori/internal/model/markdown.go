package model

import (
	"strconv"
	"strings"
)

// PhaseMarker is the generated Markdown marker for a phase id.
func PhaseMarker(id string) (string, error) {
	n, err := NormalizeID(id)
	if err != nil {
		return "", err
	}
	return "<!-- workplan-phase-id: " + n + " -->", nil
}

// StepMarker is the generated Markdown marker for a step id.
func StepMarker(id string) (string, error) {
	n, err := NormalizeID(id)
	if err != nil {
		return "", err
	}
	return "<!-- workplan-step-id: " + n + " -->", nil
}

// RenderMarkdown renders the generated Markdown for a normalized plan.
// Markers use the lossy normalized id; a marker that normalizes to empty is
// an error. A finding renders as "title (status)" with a space; everything
// else is byte-identical to the reference rendering (RenderMarkdownLegacy,
// testdata/vectors/markdown).
func RenderMarkdown(p *Plan) ([]byte, error) { return renderMarkdown(p, false) }

// RenderMarkdownLegacy is the reference rendering ("title(status)"), as
// produced by the TypeScript reference and earlier Shiori versions. It is
// kept so Markdown generated that way is still recognized as generated.
func RenderMarkdownLegacy(p *Plan) ([]byte, error) { return renderMarkdown(p, true) }

// IsGeneratedMarkdown reports whether md is the generated rendering of p
// in either the current or the legacy finding style, so Markdown generated
// the legacy way is never misclassified as hand-edited.
func IsGeneratedMarkdown(p *Plan, md []byte) (bool, error) {
	out, err := RenderMarkdown(p)
	if err != nil {
		return false, err
	}
	if string(out) == string(md) {
		return true, nil
	}
	if !hasFindingStatus(p) {
		return false, nil // both styles render identically
	}
	legacy, err := RenderMarkdownLegacy(p)
	if err != nil {
		return false, err
	}
	return string(legacy) == string(md), nil
}

func hasFindingStatus(p *Plan) bool {
	for i := range p.Findings {
		if st := p.Findings[i].Status; st != nil && *st != "" {
			return true
		}
	}
	return false
}

func renderMarkdown(p *Plan, legacy bool) ([]byte, error) {
	var b strings.Builder
	title := p.ID
	if p.Title != nil && !Blank(*p.Title) {
		title = TrimJS(*p.Title)
	}
	list := func(items []string) string {
		if len(items) == 0 {
			return "_None_"
		}
		lines := make([]string, len(items))
		for i, s := range items {
			lines[i] = "- " + s
		}
		return strings.Join(lines, "\n")
	}
	sections := []string{
		"# " + title,
		"## Goal\n" + p.Goal,
		"## Scope\n" + list(p.Scope),
		"## Non-goals\n" + list(p.NonGoals),
		"## Constraints\n" + list(p.Constraints),
		"## Relevant files\n" + list(p.RelevantFiles),
		"## Spec files\n" + list(p.SpecFiles),
	}
	phases := "_No phases defined yet._"
	if len(p.Phases) > 0 {
		blocks := make([]string, len(p.Phases))
		for i := range p.Phases {
			ph := &p.Phases[i]
			marker, err := PhaseMarker(ph.ID)
			if err != nil {
				return nil, err
			}
			label := strconv.Itoa(i + 1)
			lines := []string{
				"### " + label + ". " + ph.Title + " " + marker,
				"- Status: " + ph.Status,
				"- Id: " + ph.ID,
			}
			if len(ph.Steps) == 0 {
				lines = append(lines, "- Steps: _None yet_")
			}
			for j := range ph.Steps {
				st := &ph.Steps[j]
				sm, err := StepMarker(st.ID)
				if err != nil {
					return nil, err
				}
				lines = append(lines,
					"#### "+label+"."+strconv.Itoa(j+1)+" "+st.Title+" "+sm,
					"- Status: "+st.Status,
					"- Id: "+st.ID)
				if st.Target != nil && *st.Target != "" {
					lines = append(lines, "- Target: "+*st.Target)
				}
				if st.Action != nil && *st.Action != "" {
					lines = append(lines, "- Action: "+*st.Action)
				}
				if st.Validation != nil && *st.Validation != "" {
					lines = append(lines, "- Validation: "+*st.Validation)
				}
			}
			blocks[i] = strings.Join(lines, "\n")
		}
		phases = strings.Join(blocks, "\n\n")
	}
	sections = append(sections, "## Execution phases\n"+phases)
	findings := "_None_"
	if len(p.Findings) > 0 {
		lines := make([]string, len(p.Findings))
		for i := range p.Findings {
			f := &p.Findings[i]
			line := "- [" + f.Severity + "] " + f.Title
			if f.Status != nil && *f.Status != "" {
				if !legacy {
					line += " "
				}
				line += "(" + *f.Status + ")"
			}
			if f.Detail != nil && *f.Detail != "" {
				line += "— " + *f.Detail
			}
			if f.Source != nil && *f.Source != "" {
				line += " [source: " + *f.Source + "]"
			}
			lines[i] = line
		}
		findings = strings.Join(lines, "\n")
	}
	sections = append(sections,
		"## Adversarial review findings\n"+findings,
		"## Notes\n"+list(p.Notes),
		"## Status\n- Overall status: "+p.Status+
			"\n- Metadata file: .opencode/workplan/"+p.ID+".json"+
			"\n- Detailed plan file: "+p.PlanFile)
	b.WriteString(strings.Join(sections, "\n\n"))
	b.WriteString("\n")
	return []byte(b.String()), nil
}
