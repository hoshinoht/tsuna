package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// The workplan_patch grammar (reference behaviour pinned by the corpus and
// black-box probes): one "*** Update File:" section addressed to the linked
// planFile, hunks introduced by "@@" (optionally "@@ <anchor line>"), and
// exact line matching from a moving cursor. The JSON is never rewritten.

type hunk struct {
	anchor   string
	hasAnchr bool
	old, new []string
	changes  bool
}

type parsedPatch struct {
	target string
	hunks  []hunk
}

func parsePatch(text string) (*parsedPatch, error) {
	t := model.TrimJS(strings.ReplaceAll(text, "\r\n", "\n"))
	lines := strings.Split(t, "\n")
	if len(lines) < 2 || lines[0] != "*** Begin Patch" || lines[len(lines)-1] != "*** End Patch" {
		return nil, errors.New("Invalid patch format: missing required Begin/End markers")
	}
	body := lines[1 : len(lines)-1]
	empty := true
	for _, l := range body {
		if !model.Blank(l) {
			empty = false
			break
		}
	}
	if empty {
		return nil, errors.New("Patch rejected: empty patch")
	}
	var pp *parsedPatch
	inSection := false
	for i := 0; i < len(body); i++ {
		l := body[i]
		if inSection {
			if strings.HasPrefix(l, "*** ") {
				if strings.HasPrefix(l, "*** Move to:") {
					return nil, errors.New("workplan_patch does not support Move to sections")
				}
				inSection = false
				i--
				continue
			}
			if strings.HasPrefix(l, "@@") {
				a := model.TrimJS(strings.TrimPrefix(l, "@@"))
				pp.hunks = append(pp.hunks, hunk{anchor: a, hasAnchr: a != ""})
				continue
			}
			if len(pp.hunks) == 0 {
				return nil, errors.New("Patch hunks must start with @@: " + l)
			}
			h := &pp.hunks[len(pp.hunks)-1]
			switch {
			case strings.HasPrefix(l, " "):
				h.old = append(h.old, l[1:])
				h.new = append(h.new, l[1:])
			case strings.HasPrefix(l, "-"):
				h.old = append(h.old, l[1:])
				h.changes = true
			case strings.HasPrefix(l, "+"):
				h.new = append(h.new, l[1:])
				h.changes = true
			default:
				return nil, errors.New("Invalid hunk line; expected space, -, or + prefix: " + l)
			}
			continue
		}
		switch {
		case model.Blank(l):
		case strings.HasPrefix(l, "*** Update File:"):
			if pp != nil {
				return nil, errors.New("workplan_patch allows exactly one Update File section")
			}
			pp = &parsedPatch{target: model.TrimJS(strings.TrimPrefix(l, "*** Update File:"))}
			inSection = true
		case strings.HasPrefix(l, "*** Add File:"):
			return nil, errors.New("workplan_patch does not support Add File sections")
		case strings.HasPrefix(l, "*** Delete File:"):
			return nil, errors.New("workplan_patch does not support Delete File sections")
		case strings.HasPrefix(l, "*** Move to:"):
			return nil, errors.New("workplan_patch does not support Move to sections")
		default:
			return nil, errors.New("Invalid patch line outside an update section: " + l)
		}
	}
	if pp == nil {
		return nil, errors.New("Patch rejected: empty patch")
	}
	if len(pp.hunks) == 0 {
		return nil, errors.New("Patch rejected: no hunks found")
	}
	changes := false
	for _, h := range pp.hunks {
		changes = changes || h.changes
	}
	if !changes {
		return nil, errors.New("Patch rejected: no additions or removals found")
	}
	return pp, nil
}

func applyHunks(content string, hunks []hunk, absPath string) (string, error) {
	lines := strings.Split(content, "\n")
	cursor := 0
	for _, h := range hunks {
		if h.hasAnchr {
			found := -1
			for i := cursor; i < len(lines); i++ {
				if lines[i] == h.anchor {
					found = i
					break
				}
			}
			if found < 0 {
				return "", fmt.Errorf("Failed to find context '%s' in %s", h.anchor, absPath)
			}
			cursor = found + 1
		}
		at := cursor
		if len(h.old) > 0 {
			at = -1
			for k := cursor; k+len(h.old) <= len(lines); k++ {
				match := true
				for x := range h.old {
					if lines[k+x] != h.old[x] {
						match = false
						break
					}
				}
				if match {
					at = k
					break
				}
			}
			if at < 0 {
				return "", fmt.Errorf("Failed to find expected lines in %s:\n%s", absPath, strings.Join(h.old, "\n"))
			}
		}
		out := append([]string{}, lines[:at]...)
		out = append(out, h.new...)
		out = append(out, lines[at+len(h.old):]...)
		lines = out
		cursor = at + len(h.new)
	}
	return strings.Join(lines, "\n"), nil
}

// PrepareWorkplanPatch prepares workplan_patch.
func (e *Engine) PreparePatch(data ojson.Value) (*Prepared, error) {
	rawID, _ := getStr(data, "id")
	text, _ := getStr(data, "patchText")
	validate := false
	if v, ok := data.Get("validate"); ok {
		validate = v.Bool()
	}
	pp, err := parsePatch(text)
	if err != nil {
		return nil, err
	}
	s, err := e.loadForMutation(rawID, optHash(data))
	if err != nil {
		return nil, err
	}
	id := s.ID
	pf := s.Plan.PlanFile
	if tgt, ok := snapshotRel(e.Root, pp.target); !ok || tgt != pf {
		return nil, fmt.Errorf("Patch target must match linked planFile %s: %s", pf, pp.target)
	}
	if !s.Markdown.Exists {
		return nil, fmt.Errorf("Plan file not found: %s", pf)
	}
	updated, err := applyHunks(string(s.Markdown.Bytes), pp.hunks, s.Markdown.Path)
	if err != nil {
		return nil, err
	}
	if updated == string(s.Markdown.Bytes) {
		return nil, errors.New("Patch did not change the linked plan file")
	}
	in := e.buildIntent("patch", id, storage.NewUUID(), []targetSpec{{rel: pf, kind: "markdown", before: s.Markdown.Bytes, beforeOK: true, after: []byte(updated), afterOK: true}}, readsOf(s.StateManifest))
	if err := e.checkTargetPaths(in); err != nil {
		return nil, err
	}
	prep := &Prepared{Tool: "workplan_patch", Intent: in}
	prep.result = func(sync bool) (Output, error) {
		post, err := e.postSnapshot(in, id)
		if err != nil {
			return Output{}, err
		}
		text := "Patched workplan " + id + " plan file: " + pf
		meta := ojson.NewObject(7).
			Set("id", ojson.StringValue(id)).
			Set("planFile", ojson.StringValue(pf)).
			Set("patched", ojson.BoolValue(true)).
			Set("validate", ojson.BoolValue(validate))
		if validate {
			issues, dv := e.validationIssues(post, rawID)
			// The issue list itself, not only its count; drift warnings are
			// additive and non-failing.
			vb := ojson.NewObject(4).
				Set("valid", ojson.BoolValue(len(issues) == 0)).
				Set("issueCount", ojson.IntValue(int64(len(issues)))).
				Set("issues", ojson.StringsValue(issues))
			if w := e.validationWarnings(post, dv); len(w) > 0 {
				vb.Set("warnings", ojson.StringsValue(w))
			}
			meta.Set("validation", vb.Value())
		} else {
			text += "\nRun workplan_validate if you need full validation."
		}
		meta.Set("planHash", ojson.StringValue(post.PlanHash)).Set("stateHash", ojson.StringValue(post.StateHash))
		return Output{Text: text, Metadata: meta.Value()}, nil
	}
	return e.logged(prep, s, s.Plan, history.Change{Path: "markdown", Op: "changed"}), nil
}

// snapshotRel normalizes a patch target to a project-relative path.
func snapshotRel(root, raw string) (string, bool) {
	rel, err := snapshot.NormalizeSpecFile(root, raw)
	if err != nil {
		return "", false
	}
	return rel, true
}
