package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// get walks nested object keys.
func get(v ojson.Value, keys ...string) ojson.Value {
	for _, k := range keys {
		v, _ = v.Get(k)
	}
	return v
}

func str(v ojson.Value) string {
	switch v.Kind() {
	case ojson.String:
		return v.Str()
	case ojson.Null, ojson.Undefined:
		return "-"
	case ojson.Bool:
		if v.Bool() {
			return "yes"
		}
		return "no"
	case ojson.Number:
		return itoa(v)
	}
	return string(ojson.Compact(v))
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r, cut := ojson.TruncateUTF16(s, max); cut {
		return r
	}
	return s
}

func issues(w io.Writer, indent string, v ojson.Value) {
	for _, is := range v.Elems() {
		fmt.Fprintf(w, "%s- %s\n", indent, is.Str())
	}
}

// human renders a concise, read-only summary. Machine consumers must use
// --json; this format is not a contract.
func human(w io.Writer, cmd string, v ojson.Value) {
	switch cmd {
	case "list":
		fmt.Fprintf(w, "%s  (%s plans)\n", str(get(v, "directory")), str(get(v, "count")))
		for _, p := range get(v, "workplans").Elems() {
			if _, summary := p.Get("kind"); !summary {
				// An unlistable plan carries an issues array (and, when
				// unreadable, the raw-byte stateHash for a repair).
				fmt.Fprintf(w, "  %-24s INVALID\n", str(get(p, "id")))
				issues(w, "      ", get(p, "issues"))
				if h := get(p, "stateHash"); h.Kind() == ojson.String {
					fmt.Fprintf(w, "      stateHash %s (repair with create --overwrite --expected-hash)\n", h.Str())
				}
				continue
			}
			state := "ok"
			if !get(p, "valid").Bool() {
				state = "issues"
			}
			if get(p, "recoveryRequired").Bool() {
				state = "RECOVERY"
			}
			title := get(p, "title")
			label := str(get(p, "goal"))
			if title.Kind() == ojson.String {
				label = title.Str()
			}
			fmt.Fprintf(w, "  %-24s %-11s %-8s %s/%s steps  %s\n", str(get(p, "id")), str(get(p, "status")), state,
				str(get(p, "phaseCount")), str(get(p, "stepCount")), oneLine(label, 60))
			issues(w, "      ", get(p, "issues"))
		}
		if n := len(get(v, "sidecars").Elems()); n > 0 {
			fmt.Fprintf(w, "  sidecars: %d\n", n)
			for _, s := range get(v, "sidecars").Elems() {
				fmt.Fprintf(w, "    %-12s %s\n", str(get(s, "kind")), str(get(s, "name")))
			}
		}
	case "read":
		if get(v, "recoveryRequired").Bool() {
			fmt.Fprintf(w, "RECOVERY REQUIRED: %s\nstateHash %s\n", str(get(v, "journalPath")), str(get(v, "stateHash")))
			return
		}
		wp := get(v, "workplan")
		fmt.Fprintf(w, "%s  [%s]  %s\n", str(get(wp, "id")), str(get(wp, "status")), str(get(wp, "title")))
		fmt.Fprintf(w, "goal:      %s\nplanFile:  %s\nplanHash:  %s\nstateHash: %s\n",
			oneLine(str(get(wp, "goal")), 200), str(get(wp, "planFile")), str(get(v, "planHash")), str(get(v, "stateHash")))
		if sl := get(v, "slice"); sl.Kind() == ojson.Object {
			fmt.Fprintf(w, "slice of %s phases / %s steps (findings %s, notes %s: included=%s)\n",
				str(get(sl, "phaseCount")), str(get(sl, "stepCount")), str(get(sl, "findingCount")), str(get(sl, "noteCount")), str(get(sl, "notesIncluded")))
		}
		for pi, ph := range get(v, "selection", "phases").Elems() {
			fmt.Fprintf(w, "%d. [%s] %s  %s\n", pi+1, str(get(ph, "status")), str(get(ph, "id")), str(get(ph, "title")))
			for _, st := range get(ph, "steps").Elems() {
				fmt.Fprintf(w, "   - [%s] %s  %s\n", str(get(st, "status")), str(get(st, "id")), str(get(st, "title")))
			}
		}
		if c := get(v, "plan", "content"); c.Kind() == ojson.String {
			fmt.Fprintf(w, "\n--- %s ---\n%s", str(get(v, "plan", "path")), c.Str())
		}
	case "inspect":
		if get(v, "recoveryRequired").Bool() {
			fmt.Fprintf(w, "RECOVERY REQUIRED: %s\n", str(get(v, "journalPath")))
			return
		}
		for _, ph := range get(v, "phases").Elems() {
			fmt.Fprintf(w, "%s. [%s] %s  %s\n", str(get(ph, "indexLabel")), str(get(ph, "status")), str(get(ph, "id")), str(get(ph, "title")))
		}
		for _, st := range get(v, "steps").Elems() {
			fmt.Fprintf(w, "  %s [%s] %s/%s  %s%s\n", str(get(st, "indexPath")), str(get(st, "status")), str(get(st, "phaseId")), str(get(st, "id")), str(get(st, "title")), graphNote(st))
		}
		criticalPath(w, get(v, "criticalPath"), "")
		pg := get(v, "pagination")
		fmt.Fprintf(w, "items %s-%s of %s", str(get(pg, "offset")), itoaSum(get(pg, "offset"), get(pg, "returned")), str(get(pg, "total")))
		if c := get(pg, "nextCursor"); c.Kind() == ojson.String {
			fmt.Fprintf(w, "; next: --cursor %s", c.Str())
		}
		fmt.Fprintln(w)
	case "validate":
		if get(v, "valid").Bool() {
			fmt.Fprintln(w, "VALID")
		} else {
			fmt.Fprintf(w, "INVALID (%s issues)\n", str(get(v, "issueCount")))
		}
		issues(w, "  ", get(v, "issues"))
		for _, x := range get(v, "warnings").Elems() {
			fmt.Fprintf(w, "  warning: %s\n", x.Str())
		}
		if h := get(v, "stateHash"); h.Kind() == ojson.String {
			fmt.Fprintf(w, "planHash:  %s\nstateHash: %s\n", str(get(v, "planHash")), h.Str())
		}
	case "resume":
		if get(v, "recoveryRequired").Bool() {
			fmt.Fprintf(w, "RECOVERY REQUIRED: %s\nstateHash %s\n", str(get(v, "journalPath")), str(get(v, "stateHash")))
			return
		}
		cp := get(v, "checkpoint")
		fmt.Fprintf(w, "%s  checkpoint: %s  stateHash: %s\n", str(get(v, "workplan", "id")), str(get(cp, "freshness")), str(get(v, "hashes", "stateHash")))
		if d := get(cp, "diagnostic"); d.Kind() == ojson.String {
			fmt.Fprintf(w, "stale:     %s\n", oneLine(d.Str(), 240))
		}
		if ws := get(cp, "withheld").Elems(); len(ws) > 0 {
			names := make([]string, len(ws))
			for i, x := range ws {
				names[i] = x.Str()
			}
			fmt.Fprintf(w, "withheld:  %s (stored, not shown; see the instruction)\n", strings.Join(names, ", "))
		}
		if c := get(v, "criticalPath"); c.Kind() == ojson.Object {
			fmt.Fprintf(w, "critical:  %s steps, next %s/%s\n", str(get(c, "length")), str(get(c, "nextStep", "phaseId")), str(get(c, "nextStep", "stepId")))
		}
		if c := get(v, "compactionRecommended"); c.Kind() == ojson.Object {
			fmt.Fprintf(w, "compact:   recommended, plan JSON saves %s bytes (%s%%): %s notes, %s terminal steps, %s resolved findings (see doctor)\n",
				str(get(c, "savedJsonBytes")), str(get(c, "savedJsonPercent")), str(get(c, "notes")), str(get(c, "terminalSteps")), str(get(c, "resolvedFindings")))
		}
		if cur := get(cp, "current"); cur.Kind() == ojson.Object {
			fmt.Fprintf(w, "current:   %s/%s  %s [%s]%s\n", str(get(cur, "phaseId")), str(get(cur, "stepId")), str(get(cur, "stepTitle")), str(get(cur, "stepStatus")), graphNote(cur))
		}
		if na := get(cp, "nextAction"); na.Kind() == ojson.String {
			fmt.Fprintf(w, "next:      %s\n", oneLine(na.Str(), 200))
		}
		for _, f := range get(v, "safety", "highFindings").Elems() {
			fmt.Fprintf(w, "finding:   [%s] %s\n", str(get(f, "severity")), oneLine(str(get(f, "title")), 120))
		}
		for _, s := range get(v, "safety", "unverifiedWarnings").Elems() {
			fmt.Fprintf(w, "warning:   %s\n", oneLine(s.Str(), 200))
		}
		for _, it := range get(v, "page", "items").Elems() {
			// Classify by shape: display strings (including kind) may be
			// truncated at small budgets.
			_, isWork := it.Get("stepId")
			_, isFinding := it.Get("severity")
			switch {
			case isWork:
				fmt.Fprintf(w, "  work     %s/%s  %s%s\n", str(get(it, "phaseId")), str(get(it, "stepId")), oneLine(str(get(it, "title")), 80), graphNote(it))
			case isFinding:
				fmt.Fprintf(w, "  finding  [%s] %s\n", str(get(it, "severity")), oneLine(str(get(it, "title")), 80))
			default:
				fmt.Fprintf(w, "  ref      %s (%s)\n", str(get(it, "reference")), str(get(it, "source")))
			}
		}
		if c := get(v, "page", "nextCursor"); c.Kind() == ojson.String {
			fmt.Fprintf(w, "more: --cursor %s\n", c.Str())
		}
		fmt.Fprintln(w, str(get(v, "instruction")))
	case "doctor":
		fmt.Fprintf(w, "root:      %s\ndirectory: %s\n", str(get(v, "canonicalRoot")), str(get(v, "directory")))
		fmt.Fprintf(w, "plans %s (returned %s)  sidecars %s  locks %s  pending transactions %s\n",
			str(get(v, "planCount")), str(get(v, "returnedPlans")), str(get(v, "sidecarCount")), str(get(v, "lockCount")), str(get(v, "pendingTransactionCount")))
		for _, p := range get(v, "plans").Elems() {
			state := "ok"
			if !get(p, "valid").Bool() {
				state = "issues"
			}
			if get(p, "recoveryRequired").Bool() {
				state = "RECOVERY"
			}
			fmt.Fprintf(w, "  %-24s %-8s checkpoint=%s stateHash=%s\n", str(get(p, "id")), state, str(get(p, "checkpointFreshness")), str(get(p, "stateHash")))
			issues(w, "      ", get(p, "issues"))
			for _, x := range get(p, "warnings").Elems() {
				fmt.Fprintf(w, "      warning: %s\n", x.Str())
			}
			criticalPath(w, get(p, "criticalPath"), "      ")
			if c := get(p, "compactionRecommended"); c.Kind() == ojson.Object {
				t := get(c, "estimate", "total")
				fmt.Fprintf(w, "      compaction recommended (advice only): saves %s of %s bytes (%s%%)\n", str(get(t, "saved")), str(get(t, "before")), str(get(t, "percent")))
				for _, r := range get(c, "reasons").Elems() {
					fmt.Fprintf(w, "        %s\n", r.Str())
				}
			}
			if pf := get(p, "recoveredPlanFile"); pf.Kind() == ojson.String {
				fmt.Fprintf(w, "      recovered planFile %s (kept by create --overwrite unless --plan-file is given)\n", pf.Str())
			}
		}
		for _, l := range get(v, "locks").Elems() {
			fmt.Fprintf(w, "  lock %s  %s\n", str(get(l, "path")), str(get(l, "diagnostic")))
		}
		for _, t := range get(v, "pendingTransactions").Elems() {
			fmt.Fprintf(w, "  journal %s  plan=%s valid=%s targets=%s\n", str(get(t, "path")), str(get(t, "workplanId")), str(get(t, "valid")), str(get(t, "targetCount")))
		}
		if is := get(v, "issues").Elems(); len(is) > 0 {
			fmt.Fprintln(w, "issues:")
			issues(w, "  ", get(v, "issues"))
		}
		if ws := get(v, "warnings").Elems(); len(ws) > 0 {
			fmt.Fprintln(w, "warnings:")
			issues(w, "  ", get(v, "warnings"))
		}
	}
}

func itoaSum(a, b ojson.Value) string {
	x, _ := a.Float()
	y, _ := b.Float()
	return fmt.Sprintf("%d", int64(x+y))
}

// graphNote renders the readiness of a step view (resume current and
// page items, inspect steps): "  [ready, unblocks N]" or
// "  [blocked by p/s (status), ...]"; empty without a dependency sidecar.
func graphNote(v ojson.Value) string {
	switch str(get(v, "readiness")) {
	case "ready":
		return "  [ready, unblocks " + str(get(v, "unblocks")) + "]"
	case "blocked":
		var parts []string
		for _, b := range get(v, "blockedBy").Elems() {
			parts = append(parts, str(get(b, "phaseId"))+"/"+str(get(b, "stepId"))+" ("+str(get(b, "status"))+")")
		}
		if o := get(v, "blockedByOmitted"); o.Kind() == ojson.Number {
			parts = append(parts, "+"+str(o)+" more")
		}
		if len(parts) == 0 {
			for _, b := range get(v, "prerequisites").Elems() {
				if str(get(b, "status")) != "completed" {
					parts = append(parts, str(get(b, "phaseId"))+"/"+str(get(b, "stepId"))+" ("+str(get(b, "status"))+")")
				}
			}
		}
		return "  [blocked by " + strings.Join(parts, ", ") + "]"
	}
	return ""
}

// criticalPath prints the advisory critical path, when present.
func criticalPath(w io.Writer, cp ojson.Value, indent string) {
	if cp.Kind() != ojson.Object {
		return
	}
	var ids []string
	for _, s := range get(cp, "steps").Elems() {
		ids = append(ids, str(get(s, "phaseId"))+"/"+str(get(s, "stepId")))
	}
	fmt.Fprintf(w, "%scritical path (%s steps, advisory): %s\n", indent, str(get(cp, "length")), strings.Join(ids, " -> "))
}
