package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// The dependency graph drives work: resume readiness and
// ranking, inspect's graph view, validate/doctor order warnings and the
// critical path. The comparators restate readiness, downstream counts and
// the critical path independently from the fixture's raw JSON
// (encoding/json), not from the engine's graph code.

type gkey struct{ p, s string }

func (k gkey) String() string { return k.p + "/" + k.s }

type restated struct {
	recorded   bool
	order      map[gkey]int
	status     map[gkey]string // plan steps
	terminal   map[gkey]string
	prereqs    map[gkey][]gkey
	dependents map[gkey][]gkey
}

func restateGraph(t testing.TB, root, id string) *restated {
	t.Helper()
	dir := filepath.Join(root, ".opencode", "workplan")
	var plan struct {
		Phases []struct {
			ID    string `json:"id"`
			Steps []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"steps"`
		} `json:"phases"`
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	r := &restated{order: map[gkey]int{}, status: map[gkey]string{}, terminal: map[gkey]string{},
		prereqs: map[gkey][]gkey{}, dependents: map[gkey][]gkey{}}
	n := 0
	for _, ph := range plan.Phases {
		for _, st := range ph.Steps {
			k := gkey{ph.ID, st.ID}
			if _, dup := r.order[k]; !dup {
				r.order[k] = n
				r.status[k] = st.Status
				n++
			}
		}
	}
	type ref struct {
		PhaseID string `json:"phaseId"`
		StepID  string `json:"stepId"`
	}
	var deps struct {
		Dependencies []struct {
			ref
			DependsOn []ref `json:"dependsOn"`
		} `json:"dependencies"`
		TerminalSummaries []struct {
			ref
			Status string `json:"status"`
		} `json:"terminalSummaries"`
	}
	data, err = os.ReadFile(filepath.Join(dir, id+".dependencies.json"))
	if err != nil {
		return r
	}
	r.recorded = true
	json.Unmarshal(data, &deps)
	for _, ts := range deps.TerminalSummaries {
		r.terminal[gkey{ts.PhaseID, ts.StepID}] = ts.Status
	}
	for _, e := range deps.Dependencies {
		from := gkey{e.PhaseID, e.StepID}
		for _, d := range e.DependsOn {
			to := gkey{d.PhaseID, d.StepID}
			r.prereqs[from] = append(r.prereqs[from], to)
			r.dependents[to] = append(r.dependents[to], from)
		}
	}
	return r
}

func (r *restated) statusOf(k gkey) string {
	if s, ok := r.status[k]; ok {
		return s
	}
	return r.terminal[k]
}

func openStatus(s string) bool { return s != "" && s != "completed" && s != "cancelled" }

func (r *restated) unmet(k gkey) []gkey {
	var out []gkey
	for _, p := range r.prereqs[k] {
		if r.statusOf(p) != "completed" {
			out = append(out, p)
		}
	}
	return out
}

func (r *restated) unblocks(k gkey) int {
	seen := map[gkey]bool{k: true}
	var visit func(gkey)
	n := 0
	visit = func(x gkey) {
		for _, d := range r.dependents[x] {
			if !seen[d] {
				seen[d] = true
				if _, inPlan := r.status[d]; inPlan && openStatus(r.status[d]) {
					n++
				}
				visit(d)
			}
		}
	}
	visit(k)
	return n
}

// longest is the step count of the longest chain of open plan steps
// (unweighted; the fixtures carry no estimates).
func (r *restated) longest() int {
	memo := map[gkey]int{}
	var from func(gkey) int
	from = func(k gkey) int {
		if v, ok := memo[k]; ok {
			return v
		}
		best := 1
		for _, d := range r.dependents[k] {
			if openStatus(r.status[d]) {
				if l := 1 + from(d); l > best {
					best = l
				}
			}
		}
		memo[k] = best
		return best
	}
	best := 0
	for k, s := range r.status {
		if openStatus(s) {
			if l := from(k); l > best {
				best = l
			}
		}
	}
	return best
}

func keyOf(m map[string]any) gkey {
	p, _ := m["phaseId"].(string)
	s, _ := m["stepId"].(string)
	if s == "" {
		s, _ = m["id"].(string)
	}
	return gkey{p, s}
}

// checkReadiness verifies one step view's readiness members against the
// restatement: ready ⇔ no unmet prerequisite; a ready step carries its
// downstream count, a blocked one a prefix of its unmet prerequisites with
// the exact omitted count.
func (r *restated) checkReadiness(m map[string]any, k gkey) error {
	rd, _ := m["readiness"].(string)
	unmet := r.unmet(k)
	if (rd == "ready") != (len(unmet) == 0) || (rd != "ready" && rd != "blocked") {
		return fmt.Errorf("%s: readiness %q with %d unmet prerequisites", k, rd, len(unmet))
	}
	if rd == "ready" {
		if _, ok := m["blockedBy"]; ok {
			return fmt.Errorf("%s: ready step with blockedBy", k)
		}
		if num(m["unblocks"]) != r.unblocks(k) {
			return fmt.Errorf("%s: unblocks %v want %d", k, m["unblocks"], r.unblocks(k))
		}
		return nil
	}
	if _, ok := m["unblocks"]; ok {
		return fmt.Errorf("%s: blocked step with unblocks", k)
	}
	bb, _ := m["blockedBy"].([]any)
	omitted := 0
	if o, ok := m["blockedByOmitted"]; ok {
		omitted = num(o)
		if omitted <= 0 {
			return fmt.Errorf("%s: blockedByOmitted must be positive when present", k)
		}
	}
	if len(bb) == 0 || len(bb)+omitted != len(unmet) {
		return fmt.Errorf("%s: blockedBy %d + omitted %d != unmet %d", k, len(bb), omitted, len(unmet))
	}
	for i, x := range bb {
		xm := x.(map[string]any)
		if keyOf(xm) != unmet[i] || xm["status"] != r.statusOf(unmet[i]) || len(xm) != 3 {
			return fmt.Errorf("%s: blockedBy[%d] %v want %s (%s)", k, i, xm, unmet[i], r.statusOf(unmet[i]))
		}
	}
	return nil
}

var readinessKeys = []string{"readiness", "blockedBy", "blockedByOmitted", "unblocks"}

const cancelledWarningPrefix = "Dependency order warning: "

// ---- resume ----

// graphResumeCompat compares a resume packet with the packet without the
// graph additions (here the oracle packet).
func graphResumeCompat(r *restated, text, offText string) error {
	g, err := decodeAny(text)
	if err != nil {
		return err
	}
	w, err := decodeAny(offText)
	if err != nil {
		return err
	}
	gm, wm := g.(map[string]any), w.(map[string]any)
	if !r.recorded {
		return fmt.Errorf("graph members without a dependency sidecar")
	}
	// Current step.
	if cur, ok := gm["checkpoint"].(map[string]any)["current"].(map[string]any); ok {
		if err := r.checkReadiness(cur, keyOf(cur)); err != nil {
			return fmt.Errorf("current: %w", err)
		}
		for _, k := range readinessKeys {
			delete(cur, k)
		}
	}
	// Page items: every active-work item carries readiness; ready work
	// first by non-increasing downstream count, then blocked work.
	pg := gm["page"].(map[string]any)
	items := pg["items"].([]any)
	seenBlocked := false
	lastUnblocks := -1
	for i, it := range items {
		m := it.(map[string]any)
		if m["kind"] != "active-work" {
			for _, k := range readinessKeys {
				if _, ok := m[k]; ok {
					return fmt.Errorf("page.items[%d]: %s on a %v item", i, k, m["kind"])
				}
			}
			continue
		}
		k := keyOf(m)
		if err := r.checkReadiness(m, k); err != nil {
			return fmt.Errorf("page.items[%d]: %w", i, err)
		}
		if m["readiness"] == "ready" {
			u := num(m["unblocks"])
			if seenBlocked || (lastUnblocks >= 0 && u > lastUnblocks) {
				return fmt.Errorf("page.items[%d]: ranking violated", i)
			}
			lastUnblocks = u
		} else {
			seenBlocked = true
		}
		for _, key := range readinessKeys {
			delete(m, key)
		}
	}
	// Cancelled-prerequisite warnings: exactly one per open step behind a
	// cancelled prerequisite.
	sf := gm["safety"].(map[string]any)
	want := 0
	for k, s := range r.status {
		if !openStatus(s) {
			continue
		}
		for _, p := range r.unmet(k) {
			if r.statusOf(p) == "cancelled" {
				want++
				break
			}
		}
	}
	ws, _ := sf["unverifiedWarnings"].([]any)
	var kept []any
	removed := 0
	for _, x := range ws {
		if s, _ := x.(string); strings.HasPrefix(s, cancelledWarningPrefix) {
			removed++
			continue
		}
		kept = append(kept, x)
	}
	if kept == nil {
		kept = []any{}
	}
	total := num(sf["unverifiedWarningCount"])
	offCount := num(wm["safety"].(map[string]any)["unverifiedWarningCount"])
	if total-offCount != want || removed > want {
		return fmt.Errorf("cancelled-prerequisite warnings: count %d (without the graph %d), want %d more", total, offCount, want)
	}
	sf["unverifiedWarnings"] = kept
	sf["unverifiedWarningCount"] = wm["safety"].(map[string]any)["unverifiedWarningCount"]

	// Items: when the packet without the graph holds every item, the page
	// is a prefix of those items ranked; otherwise the same order.
	wpg := wm["page"].(map[string]any)
	witems := wpg["items"].([]any)
	expected := witems
	if num(wpg["offset"]) == 0 && num(wpg["omitted"]) == 0 {
		var ready, blocked, rest []any
		for _, it := range witems {
			m := it.(map[string]any)
			switch {
			case m["kind"] != "active-work":
				rest = append(rest, it)
			case len(r.unmet(keyOf(m))) == 0:
				ready = append(ready, it)
			default:
				blocked = append(blocked, it)
			}
		}
		sort.SliceStable(ready, func(i, j int) bool {
			return r.unblocks(keyOf(ready[i].(map[string]any))) > r.unblocks(keyOf(ready[j].(map[string]any)))
		})
		expected = append(append(append([]any{}, ready...), blocked...), rest...)
	}
	if len(items) > len(expected) && num(wpg["omitted"]) == 0 {
		return fmt.Errorf("page carries %d items, without the graph %d", len(items), len(expected))
	}
	n := len(items)
	if len(expected) < n {
		n = len(expected)
	}
	for i := 0; i < n; i++ {
		a, b := items[i].(map[string]any), expected[i].(map[string]any)
		if a["kind"] != b["kind"] || (a["kind"] == "active-work" && keyOf(a) != keyOf(b)) {
			return fmt.Errorf("page.items[%d]: %v/%v, expected %v/%v", i, a["kind"], keyOf(a), b["kind"], keyOf(b))
		}
		if err := compatible(a, b, "page.items"); err != nil {
			return fmt.Errorf("page.items[%d]: %w", i, err)
		}
	}
	pg["items"], wpg["items"] = []any{}, []any{}
	return compatible(gm, wm, "")
}

// ---- inspect ----

// graphInspectInverse checks inspect's graph view against the restatement
// and removes it.
func graphInspectInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	if v.Call.Tool != "workplan_inspect" {
		t.Fatalf("graph-inspect does not apply to %s", v.Call.Tool)
	}
	r := restateGraph(t, root.Path, inputID(v.Call.Input))
	p, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := decodeAny(text)
	gm := g.(map[string]any)
	for i, st := range gm["steps"].([]any) {
		m := st.(map[string]any)
		k := gkey{m["phaseId"].(string), m["id"].(string)}
		var pre []string
		for _, x := range m["prerequisites"].([]any) {
			xm := x.(map[string]any)
			pre = append(pre, keyOf(xm).String()+"="+xm["status"].(string))
		}
		var wantPre []string
		for _, x := range r.prereqs[k] {
			wantPre = append(wantPre, x.String()+"="+r.statusOf(x))
		}
		if strings.Join(pre, ",") != strings.Join(wantPre, ",") {
			t.Fatalf("steps[%d] prerequisites %v want %v", i, pre, wantPre)
		}
		var deps, wantDeps []string
		for _, x := range m["dependents"].([]any) {
			deps = append(deps, keyOf(x.(map[string]any)).String())
		}
		wd := append([]gkey{}, r.dependents[k]...)
		sort.SliceStable(wd, func(a, b int) bool { return r.order[wd[a]] < r.order[wd[b]] })
		for _, x := range wd {
			wantDeps = append(wantDeps, x.String())
		}
		if strings.Join(deps, ",") != strings.Join(wantDeps, ",") {
			t.Fatalf("steps[%d] dependents %v want %v", i, deps, wantDeps)
		}
		if openStatus(r.status[k]) {
			// Inspect shows readiness and the downstream count on every
			// open step; prerequisites (with statuses) replace blockedBy.
			ready := len(r.unmet(k)) == 0
			if (m["readiness"] == "ready") != ready || (m["readiness"] != "ready" && m["readiness"] != "blocked") {
				t.Fatalf("steps[%d]: readiness %v with %d unmet", i, m["readiness"], len(r.unmet(k)))
			}
			if num(m["unblocks"]) != r.unblocks(k) {
				t.Fatalf("steps[%d]: unblocks %v want %d", i, m["unblocks"], r.unblocks(k))
			}
			if sl, err := m["slack"].(json.Number).Float64(); err != nil || sl < 0 {
				t.Fatalf("steps[%d]: slack %v", i, m["slack"])
			}
		} else if _, ok := m["readiness"]; ok {
			t.Fatalf("steps[%d]: readiness on a closed step", i)
		}
	}
	if err := checkCriticalPath(r, gm["criticalPath"]); err != nil {
		t.Fatal(err)
	}
	var ms []ojson.Member
	for _, m := range p.Value.Members() {
		switch m.Key {
		case "criticalPath":
			continue
		case "steps":
			var steps []ojson.Value
			for _, st := range m.Value.Elems() {
				steps = append(steps, objWithout(st, "prerequisites", "dependents", "readiness", "unblocks", "slack"))
			}
			if steps == nil {
				steps = []ojson.Value{}
			}
			m.Value = ojson.ArrayValue(steps)
		}
		ms = append(ms, m)
	}
	return string(ojson.Pretty(ojson.ObjectValue(ms)))
}

// checkCriticalPath: absent unless the longest open chain has at least
// two steps; otherwise a chain of open steps along stored edges whose
// length is the restated maximum.
func checkCriticalPath(r *restated, cpv any) error {
	want := r.longest()
	if cpv == nil {
		if want >= 2 {
			return fmt.Errorf("criticalPath missing (longest open chain %d)", want)
		}
		return nil
	}
	cp := cpv.(map[string]any)
	steps := cp["steps"].([]any)
	if want < 2 || num(cp["length"]) != want || len(steps) != want {
		return fmt.Errorf("criticalPath length %v want %d", cp["length"], want)
	}
	var prev *gkey
	for i, x := range steps {
		xm := x.(map[string]any)
		k := keyOf(xm)
		if !openStatus(r.status[k]) || xm["status"] != r.status[k] {
			return fmt.Errorf("criticalPath.steps[%d] %s is not open", i, k)
		}
		if prev != nil {
			edge := false
			for _, p := range r.prereqs[k] {
				edge = edge || p == *prev
			}
			if !edge {
				return fmt.Errorf("criticalPath.steps[%d]: %s does not depend on %s", i, k, *prev)
			}
		}
		prev = &k
	}
	return nil
}

// ---- validate / doctor ----

var graphWarningPrefixes = []string{
	"dependencies: Order warning: ",
	"dependencies: Step ",
}

func isGraphWarning(s string) bool {
	for _, p := range graphWarningPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	// dependencies.<i>.dependsOn...: backward link / empty entry
	return strings.HasPrefix(s, "dependencies.") && (strings.Contains(s, ": Backward link: ") || strings.HasSuffix(s, ": Dependency entry lists no prerequisites."))
}

// stripGraphDiag removes the graph warnings (dropping an emptied warnings
// member) and the doctor plan criticalPath; it reports each removed
// criticalPath through f.
func stripGraphDiag(v ojson.Value, f func(id string, cp ojson.Value) error) (ojson.Value, error) {
	var ms []ojson.Member
	for _, m := range v.Members() {
		switch m.Key {
		case "warnings":
			var kept []ojson.Value
			for _, w := range m.Value.Elems() {
				if !isGraphWarning(w.Str()) {
					kept = append(kept, w)
				}
			}
			if len(kept) == 0 {
				continue
			}
			m.Value = ojson.ArrayValue(kept)
		case "criticalPath":
			id, _ := v.Get("id")
			if err := f(id.Str(), m.Value); err != nil {
				return ojson.Value{}, err
			}
			continue
		case "plans":
			var plans []ojson.Value
			for _, pl := range m.Value.Elems() {
				x, err := stripGraphDiag(pl, f)
				if err != nil {
					return ojson.Value{}, err
				}
				plans = append(plans, x)
			}
			if plans == nil {
				plans = []ojson.Value{}
			}
			m.Value = ojson.ArrayValue(plans)
		}
		ms = append(ms, m)
	}
	return ojson.ObjectValue(ms), nil
}

func graphWarningsInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	if v.Call.Tool != "workplan_validate" && v.Call.Tool != "workplan_doctor" {
		t.Fatalf("graph-warnings does not apply to %s", v.Call.Tool)
	}
	p, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := stripGraphDiag(p.Value, func(id string, cp ojson.Value) error {
		var x any
		d := json.NewDecoder(strings.NewReader(string(ojson.Compact(cp))))
		d.UseNumber()
		d.Decode(&x)
		return checkCriticalPath(restateGraph(t, root.Path, id), x)
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(ojson.Pretty(stripped))
}

// ---- resume critical path ----

// resumeCriticalPathInverse checks resume's compact critical path against
// inspect's full path and removes it.
func resumeCriticalPathInverse(t *testing.T, v *vector, _ testutil.Root, e *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := parsed.Value.Get("criticalPath")
	if v.Call.Tool != "workplan_resume" || !ok {
		t.Fatal("resume-critical-path needs a resume packet with criticalPath")
	}
	insp, err := e.Inspect(input.InspectInput{ID: inputID(v.Call.Input)})
	if err != nil {
		t.Fatal(err)
	}
	full, ok := insp.Get("criticalPath")
	if !ok {
		t.Fatal("resume has a critical path but inspect has none")
	}
	steps, _ := full.Get("steps")
	l, _ := c.Get("length")
	fl, _ := full.Get("length")
	if l.NumberLiteral() != fl.NumberLiteral() || strMember(c, "nextStep", "phaseId") != strMember(steps.Elems()[0], "phaseId") ||
		strMember(c, "nextStep", "stepId") != strMember(steps.Elems()[0], "stepId") {
		t.Fatalf("resume critical path %s, inspect %s", ojson.Compact(c), ojson.Compact(full))
	}
	return reencode(text, objWithout(parsed.Value, "criticalPath"))
}
