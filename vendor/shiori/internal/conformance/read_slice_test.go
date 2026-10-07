package conformance

import (
	"encoding/json"
	"fmt"
)

// readSliceCompat: a filtered read is exactly the
// oracle's document minus phases/findings/notes, the same selection, no
// Markdown content, the dependency view restricted to the selection and a
// correct slice descriptor.
func readSliceCompat(v *vector, got string) error {
	g, err := decodeAny(got)
	if err != nil {
		return err
	}
	gm := g.(map[string]any)
	wm := expectedAny(v).(map[string]any)
	for _, k := range []string{"path", "planHash", "stateHash", "selection"} {
		if !jsonEqual(gm[k], wm[k]) {
			return fmt.Errorf("%s differs", k)
		}
	}
	ww := wm["workplan"].(map[string]any)
	header := map[string]any{}
	for k, x := range ww {
		if k != "phases" && k != "reviewFindings" && k != "notes" {
			header[k] = x
		}
	}
	if !jsonEqual(gm["workplan"], header) {
		return fmt.Errorf("workplan header is not the oracle document minus phases/findings/notes")
	}
	wplan := wm["plan"].(map[string]any)
	if !jsonEqual(gm["plan"], map[string]any{"path": wplan["path"], "exists": wplan["exists"]}) {
		return fmt.Errorf("plan must carry path/exists without Markdown content")
	}
	// Dependencies restricted to the selection (independent restatement).
	sel := map[[2]string]bool{}
	for _, ph := range wm["selection"].(map[string]any)["phases"].([]any) {
		pm := ph.(map[string]any)
		for _, st := range pm["steps"].([]any) {
			sel[[2]string{pm["id"].(string), st.(map[string]any)["id"].(string)}] = true
		}
	}
	key := func(x any) [2]string {
		m := x.(map[string]any)
		return [2]string{m["phaseId"].(string), m["stepId"].(string)}
	}
	wd := wm["dependencies"].(map[string]any)
	deps := []any{}
	refs := map[[2]string]bool{}
	for _, en := range wd["dependencies"].([]any) {
		keep := sel[key(en)]
		for _, r := range en.(map[string]any)["dependsOn"].([]any) {
			keep = keep || sel[key(r)]
		}
		if keep {
			deps = append(deps, en)
			for _, r := range en.(map[string]any)["dependsOn"].([]any) {
				refs[key(r)] = true
			}
		}
	}
	terms := []any{}
	for _, ts := range wd["terminalSummaries"].([]any) {
		if refs[key(ts)] || sel[key(ts)] {
			terms = append(terms, ts)
		}
	}
	wantDeps := map[string]any{"recorded": wd["recorded"], "dependencies": deps, "terminalSummaries": terms, "issues": wd["issues"]}
	if !jsonEqual(gm["dependencies"], wantDeps) {
		return fmt.Errorf("dependencies are not the oracle view restricted to the selection")
	}
	steps := 0
	for _, ph := range ww["phases"].([]any) {
		steps += len(ph.(map[string]any)["steps"].([]any))
	}
	wantSlice := map[string]any{
		"filtered": true, "phaseCount": json.Number(fmt.Sprint(len(ww["phases"].([]any)))),
		"stepCount": json.Number(fmt.Sprint(steps)), "findingCount": json.Number(fmt.Sprint(len(ww["reviewFindings"].([]any)))),
		"noteCount": json.Number(fmt.Sprint(len(ww["notes"].([]any)))), "notesIncluded": false, "markdownIncluded": false,
		"dependenciesFiltered": true, "full": "workplan_read id=" + ww["id"].(string),
	}
	if !jsonEqual(gm["slice"], wantSlice) {
		return fmt.Errorf("slice descriptor %v want %v", gm["slice"], wantSlice)
	}
	if len(gm) != 8 {
		return fmt.Errorf("unexpected members: %d", len(gm))
	}
	return nil
}
