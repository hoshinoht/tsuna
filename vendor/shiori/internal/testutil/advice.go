package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
)

var (
	decisionRe = regexp.MustCompile(`(?i)\b(decision|decisions|decided)\b`)
	userRe     = regexp.MustCompile(`\bUSER\b`)
)

// RestateAdvice is the tests' own statement of the compaction advice
// rules, from the raw plan JSON: archivable = completed phases whose
// every step is completed; terminal = completed or cancelled steps;
// resolved = findings with status resolved; rollover keeps the latest
// keep notes and older notes that are pinned, decision records, name an
// open step / quote an open finding title, or are one of the latest three
// compaction archive pointers.
func RestateAdvice(data []byte, keep int) (map[string]int, error) {
	var p struct {
		Phases []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Steps  []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"steps"`
		} `json:"phases"`
		Findings []struct {
			Title  string  `json:"title"`
			Status *string `json:"status"`
		} `json:"reviewFindings"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	out := map[string]int{}
	var openSteps, openTitles []string
	for _, ph := range p.Phases {
		all := ph.Status == "completed" && len(ph.Steps) > 0
		for _, st := range ph.Steps {
			done := st.Status == "completed" || st.Status == "cancelled"
			if done {
				out["terminal"]++
			} else {
				openSteps = append(openSteps, ph.ID+"/"+st.ID)
			}
			all = all && st.Status == "completed"
		}
		if all {
			out["archivable"] += len(ph.Steps)
		}
	}
	for _, f := range p.Findings {
		if f.Status != nil && *f.Status == "resolved" {
			out["resolved"]++
		} else if t := strings.TrimSpace(f.Title); ojson.UTF16Len(t) >= 12 {
			openTitles = append(openTitles, t)
		}
	}
	names := func(n string) bool {
		for _, s := range openSteps {
			re := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(s) + `($|[^A-Za-z0-9_-])`)
			if re.MatchString(n) {
				return true
			}
		}
		for _, t := range openTitles {
			if strings.Contains(n, t) {
				return true
			}
		}
		return false
	}
	cut := len(p.Notes) - keep
	if cut < 0 {
		cut = 0
	}
	out["kept.latest"] = len(p.Notes) - cut
	// Only the latest three archive pointer notes (anywhere in the list)
	// are kept; older ones roll over like ordinary notes.
	var pointers []int
	for i, n := range p.Notes {
		if strings.HasPrefix(n, "Compaction archive: ") {
			pointers = append(pointers, i)
		}
	}
	if len(pointers) > 3 {
		pointers = pointers[len(pointers)-3:]
	}
	latestPointer := map[int]bool{}
	for _, i := range pointers {
		latestPointer[i] = true
	}
	for i, n := range p.Notes[:cut] {
		switch {
		case strings.Contains(strings.ToLower(n), "[pinned]"):
			out["kept.pinned"]++
		case decisionRe.MatchString(n) || userRe.MatchString(n):
			out["kept.decision"]++
		case names(n):
			out["kept.openReference"]++
		case latestPointer[i]:
			out["kept.archivePointer"]++
		default:
			out["notes"]++
		}
	}
	return out, nil
}

// CheckAdviceCounts compares an advice (resume's compact form or doctor's
// detail) with RestateAdvice over the plan's raw bytes under root.
func CheckAdviceCounts(root, id, tool string, a ojson.Value, keep int) error {
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".json"))
	if err != nil {
		return err
	}
	want, err := RestateAdvice(data, keep)
	if err != nil {
		return err
	}
	num := func(keys ...string) int {
		v := a
		for _, k := range keys {
			v, _ = v.Get(k)
		}
		n, _ := v.Float()
		return int(n)
	}
	got := map[string]int{}
	if tool == "workplan_resume" {
		got["notes"] = num("notes")
		got["archivable"] = num("terminalSteps")
		got["resolved"] = num("resolvedFindings")
	} else {
		got["notes"] = num("notes", "eligible")
		got["archivable"] = num("terminalSteps", "archivable")
		got["terminal"] = num("terminalSteps", "total")
		got["resolved"] = num("resolvedFindings", "count")
		for _, k := range []string{"pinned", "decision", "openReference", "archivePointer", "latest"} {
			got["kept."+k] = num("notes", "kept", k)
		}
		if j := num("estimate", "json", "before"); j != len(data) {
			return fmt.Errorf("estimate.json.before %d, file %d bytes", j, len(data))
		}
	}
	for k, g := range got {
		if want[k] != g {
			return fmt.Errorf("%s: advice %d, restated %d", k, g, want[k])
		}
	}
	return nil
}
