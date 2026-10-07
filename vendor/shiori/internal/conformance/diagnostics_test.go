package conformance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// Diagnostics that validate, doctor and list add to the reference output.
// Each inverse removes exactly its members and requires them present; the
// additions are restated independently from the fixture's raw bytes.

// ---- additive warnings and stray artifacts ----

var additiveWarningPrefixes = []string{
	"planFile: Linked Markdown is not the generated rendering of the plan JSON",
	"Orphaned sidecar ",
	"Unclassified file ",
}

func checkAdditiveWarnings(v ojson.Value) error {
	if v.Kind() != ojson.Array || len(v.Elems()) == 0 {
		return fmt.Errorf("warnings must be a nonempty array when present")
	}
	for _, w := range v.Elems() {
		ok := false
		for _, p := range additiveWarningPrefixes {
			ok = ok || strings.HasPrefix(w.Str(), p)
		}
		if !ok {
			return fmt.Errorf("unexpected warning %q", w.Str())
		}
	}
	return nil
}

// stripAdditive removes the additive members, checking them.
func stripAdditive(text string) (string, int, error) {
	p, err := ojson.Parse([]byte(text))
	if err != nil {
		return "", 0, err
	}
	removed := 0
	var ms []ojson.Member
	for _, m := range p.Value.Members() {
		switch m.Key {
		case "warnings":
			if err := checkAdditiveWarnings(m.Value); err != nil {
				return "", 0, err
			}
			removed++
			continue
		case "strayArtifacts", "strayArtifactCount", "omittedStrayArtifacts":
			removed++
			continue
		case "plans":
			var plans []ojson.Value
			for _, pl := range m.Value.Elems() {
				var pms []ojson.Member
				for _, x := range pl.Members() {
					if x.Key == "warnings" {
						if err := checkAdditiveWarnings(x.Value); err != nil {
							return "", 0, err
						}
						removed++
						continue
					}
					pms = append(pms, x)
				}
				plans = append(plans, ojson.ObjectValue(pms))
			}
			if plans == nil {
				plans = []ojson.Value{}
			}
			m.Value = ojson.ArrayValue(plans)
		}
		ms = append(ms, m)
	}
	return string(ojson.Pretty(ojson.ObjectValue(ms))), removed, nil
}

// additiveCompat: removing the additive members yields the oracle output
// byte-for-byte, or passes the vector's oracle-divergence comparator.
func additiveCompat(v *vector, got string, x testutil.Expectation) error {
	stripped, removed, err := stripAdditive(got)
	if err != nil {
		return err
	}
	if removed == 0 {
		return fmt.Errorf("no additive member present")
	}
	for _, c := range x.Compare {
		if div, ok := divergences[c]; ok {
			return div(v, stripped)
		}
	}
	want := oracleText(v)
	if stripped != want || sha(stripped) != v.Expect.OutputSha256 {
		return fmt.Errorf("output without the additive members differs from the oracle\n%s", firstDiff(stripped, want))
	}
	return nil
}

// ---- unreadable plans: raw-byte hashes ----

// withoutRawHashes drops the raw-byte hashes of an unreadable-plan entry.
func withoutRawHashes(v ojson.Value) ojson.Value {
	if has(v, "workplan") || has(v, "path") || !has(v, "planHash") || !has(v, "stateHash") {
		return v
	}
	return objWithout(v, "planHash", "stateHash")
}

func rawHashesInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	out := parsed.Value
	switch v.Call.Tool {
	case "workplan_validate":
		out = withoutRawHashes(out)
	case "workplan_doctor":
		out = mapPlans(out, "plans", withoutRawHashes)
	case "workplan_list":
		out = mapPlans(out, "workplans", func(p ojson.Value) ojson.Value {
			if has(p, "kind") || !has(p, "planHash") {
				return p
			}
			return objWithout(p, "planHash", "stateHash", "recoveryRequired")
		})
	default:
		t.Fatalf("raw-hashes does not apply to %s", v.Call.Tool)
	}
	if err := restateRawHashEntries(root.Path, v, text); err != nil {
		t.Fatalf("raw-hashes restatement: %v", err)
	}
	return reencode(text, out)
}

// mapPlans applies f to every element of the array member key.
func mapPlans(v ojson.Value, key string, f func(ojson.Value) ojson.Value) ojson.Value {
	list, _ := v.Get(key)
	out := []ojson.Value{}
	for _, p := range list.Elems() {
		out = append(out, f(p))
	}
	return objReplace(v, key, ojson.ArrayValue(out))
}

func fileSHA(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}

// restateRawHashes recomputes the raw-byte hashes of an unreadable plan
// straight from the hash contract: the primary JSON, plus the three
// sidecars for the state hash, sorted by path.
func restateRawHashes(root, id string) (string, string) {
	type entry struct{ path, sha string }
	rel := func(suffix string) string { return ".opencode/workplan/" + id + suffix }
	get := func(r string) entry {
		s, ok := fileSHA(filepath.Join(root, r))
		if !ok {
			return entry{r, ""}
		}
		return entry{r, s}
	}
	manifest := func(es []entry) string {
		sort.Slice(es, func(i, j int) bool { return es[i].path < es[j].path })
		var parts []string
		for _, e := range es {
			if e.sha == "" {
				parts = append(parts, fmt.Sprintf(`{"path":%q,"missing":true}`, e.path))
			} else {
				parts = append(parts, fmt.Sprintf(`{"path":%q,"sha256":%q}`, e.path, e.sha))
			}
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	h := func(version, m string) string {
		sum := sha256.Sum256([]byte(version + "\n" + m + "\n"))
		return hex.EncodeToString(sum[:])
	}
	plan := []entry{get(rel(".json"))}
	state := []entry{get(rel(".json")), get(rel(".checkpoint.json")), get(rel(".dependencies.json")), get(rel(".transaction.json"))}
	return h("workplan-plan-v1", manifest(plan)), h("workplan-state-v1", manifest(state))
}

// restateRawHashEntries checks every raw-hash entry of an output.
func restateRawHashEntries(root string, v *vector, text string) error {
	var doc map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return err
	}
	checkRaw := func(id string, e map[string]any) error {
		ph, _ := e["planHash"].(string)
		sh, _ := e["stateHash"].(string)
		wp, wsh := restateRawHashes(root, id)
		if ph != wp || sh != wsh {
			return fmt.Errorf("%s: raw hashes %s/%s, restated %s/%s", id, ph, sh, wp, wsh)
		}
		return nil
	}
	n := 0
	switch v.Call.Tool {
	case "workplan_validate":
		if _, ok := doc["path"]; !ok {
			if _, ok := doc["planHash"]; ok {
				n++
				if err := checkRaw(strings.ToLower(inputID(v.Call.Input)), doc); err != nil {
					return err
				}
			}
		}
	case "workplan_doctor", "workplan_list":
		key := "plans"
		if v.Call.Tool == "workplan_list" {
			key = "workplans"
		}
		for _, p := range asList(doc[key]) {
			e := p.(map[string]any)
			_, wp := e["workplan"]
			_, kind := e["kind"]
			if _, ok := e["planHash"]; ok && !wp && !kind {
				n++
				if err := checkRaw(e["id"].(string), e); err != nil {
					return err
				}
			}
		}
	}
	if n == 0 {
		return fmt.Errorf("no raw-hash entry")
	}
	return nil
}

// ---- list issues array ----

// listIssuesInverse maps an invalid list entry's issues array back to the
// reference's single issue string.
func listIssuesInverse(t *testing.T, v *vector, _ testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	if v.Call.Tool != "workplan_list" {
		t.Fatalf("list-issues does not apply to %s", v.Call.Tool)
	}
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	out := mapPlans(parsed.Value, "workplans", func(p ojson.Value) ojson.Value {
		if has(p, "kind") {
			return p
		}
		issues, _ := p.Get("issues")
		if len(issues.Elems()) != 1 || has(p, "issue") {
			t.Fatalf("invalid list entry needs exactly one issue: %s", ojson.Compact(p))
		}
		id, _ := p.Get("id")
		valid, _ := p.Get("valid")
		rest := objWithout(p, "id", "valid", "issues")
		if len(rest.Members()) != 0 {
			t.Fatalf("invalid list entry has unexpected members: %s", ojson.Compact(p))
		}
		return ojson.NewObject(3).Set("id", id).Set("valid", valid).Set("issue", issues.Elems()[0]).Value()
	})
	return reencode(text, out)
}

// ---- stale Markdown copies ----

func staleMarkdownInverse(t *testing.T, v *vector, _ testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	out := parsed.Value
	strays, ok := out.Get("strayArtifacts")
	if v.Call.Tool != "workplan_doctor" || !ok {
		t.Fatalf("stale-markdown needs doctor stray artifacts")
	}
	if om, _ := out.Get("omittedStrayArtifacts"); om.NumberLiteral() != "0" {
		t.Fatalf("stray inverse with omitted entries is not supported")
	}
	var keep []ojson.Value
	for _, s := range strays.Elems() {
		if k, _ := s.Get("kind"); k.Str() != "stale-markdown" {
			keep = append(keep, s)
		}
	}
	var warn []string
	w, _ := out.Get("warnings")
	for _, x := range w.Elems() {
		if !strings.HasPrefix(x.Str(), "Stale Markdown copy ") {
			warn = append(warn, x.Str())
		}
	}
	if len(keep) == 0 {
		out = objWithout(out, "strayArtifacts", "strayArtifactCount", "omittedStrayArtifacts", "warnings")
	} else {
		out = objReplace(out, "strayArtifacts", ojson.ArrayValue(keep))
		out = objReplace(out, "strayArtifactCount", ojson.IntValue(int64(len(keep))))
		out = objReplace(out, "warnings", ojson.StringsValue(warn))
	}
	return reencode(text, out)
}

// ---- missing step markers ----

const markerNeedle = "has no step marker (<!-- workplan-step-id: <stepId> -->)"

// withoutMarkerWarning drops the step-marker warning; the member goes when
// nothing else is left in it.
func withoutMarkerWarning(v ojson.Value) ojson.Value {
	w, ok := v.Get("warnings")
	if !ok {
		return v
	}
	var keep []string
	for _, x := range w.Elems() {
		if !strings.Contains(x.Str(), markerNeedle) {
			keep = append(keep, x.Str())
		}
	}
	if len(keep) == 0 {
		return objWithout(v, "warnings")
	}
	return objReplace(v, "warnings", ojson.StringsValue(keep))
}

func stepMarkersInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	out := parsed.Value
	var doc map[string]any
	json.Unmarshal([]byte(text), &doc)
	check := func(id string, e map[string]any) {
		for _, x := range asList(e["warnings"]) {
			if s, _ := x.(string); strings.Contains(s, markerNeedle) {
				if err := restateMarkers(root.Path, id, s); err != nil {
					t.Fatalf("step-markers restatement: %v", err)
				}
			}
		}
	}
	switch v.Call.Tool {
	case "workplan_validate":
		out = withoutMarkerWarning(out)
		check(strings.ToLower(inputID(v.Call.Input)), doc)
	case "workplan_doctor":
		out = mapPlans(out, "plans", withoutMarkerWarning)
		for _, p := range asList(doc["plans"]) {
			e := p.(map[string]any)
			check(e["id"].(string), e)
		}
	default:
		t.Fatalf("step-markers does not apply to %s", v.Call.Tool)
	}
	return reencode(text, out)
}

var markerCount = regexp.MustCompile(`for (\d+) of (\d+) steps: `)

// restateMarkers counts the steps whose marker is absent from the linked
// Markdown, from the raw plan JSON and Markdown text.
func restateMarkers(root, id, warning string) error {
	var plan struct {
		PlanFile string `json:"planFile"`
		Phases   []struct {
			ID    string `json:"id"`
			Steps []struct {
				ID string `json:"id"`
			} `json:"steps"`
		} `json:"phases"`
	}
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return err
	}
	md, err := os.ReadFile(filepath.Join(root, plan.PlanFile))
	if err != nil {
		return err
	}
	missing, total := 0, 0
	for _, ph := range plan.Phases {
		for _, st := range ph.Steps {
			total++
			if !strings.Contains(string(md), "<!-- workplan-step-id: "+st.ID+" -->") {
				missing++
			}
		}
	}
	m := markerCount.FindStringSubmatch(warning)
	if m == nil || m[1] != fmt.Sprint(missing) || m[2] != fmt.Sprint(total) {
		return fmt.Errorf("%s: marker warning %q, restated %d of %d", id, warning, missing, total)
	}
	return nil
}

// ---- stale checkpoint detail ----

// msgStale is the reference's stale checkpoint issue.
const msgStale = "Checkpoint does not match the current JSON/Markdown/spec manifest"

var staleChanged = regexp.MustCompile(`(\S+) changed \(checkpoint `)

func staleDetailInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	if v.Call.Tool != "workplan_doctor" {
		t.Fatalf("stale-detail does not apply to %s", v.Call.Tool)
	}
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	pre := "checkpoint: " + msgStale + ". "
	out := mapPlans(parsed.Value, "plans", func(p ojson.Value) ojson.Value {
		issues, ok := p.Get("issues")
		if !ok {
			return p
		}
		id, _ := p.Get("id")
		var is []string
		for _, x := range issues.Elems() {
			s := x.Str()
			if strings.HasPrefix(s, pre) {
				if err := restateStale(root.Path, id.Str(), s); err != nil {
					t.Fatalf("stale-detail restatement: %v", err)
				}
				s = "checkpoint: " + msgStale
			}
			is = append(is, s)
		}
		return objReplace(p, "issues", ojson.StringsValue(is))
	})
	return reencode(text, out)
}

// restateStale checks that the stale detail names exactly the checkpoint
// manifest entries whose bytes differ now.
func restateStale(root, id, issue string) error {
	var cp struct {
		Manifest []struct {
			Path    string `json:"path"`
			SHA256  string `json:"sha256"`
			Missing bool   `json:"missing"`
		} `json:"manifest"`
	}
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".checkpoint.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &cp); err != nil {
		return err
	}
	var want []string
	for _, en := range cp.Manifest {
		now, ok := fileSHA(filepath.Join(root, en.Path))
		if en.Missing != !ok || (ok && now != en.SHA256) {
			want = append(want, en.Path)
		}
	}
	var got []string
	for _, m := range staleChanged.FindAllStringSubmatch(issue, -1) {
		got = append(got, m[1])
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s: stale detail names %v, restated %v", id, got, want)
	}
	return nil
}

// ---- recovered planFile of an unparseable plan ----

func recoveredPlanFileInverse(t *testing.T, v *vector, root testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	if v.Call.Tool != "workplan_doctor" {
		t.Fatalf("recovered-planfile does not apply to %s", v.Call.Tool)
	}
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	out := mapPlans(parsed.Value, "plans", func(p ojson.Value) ojson.Value {
		pf, ok := p.Get("recoveredPlanFile")
		if !ok {
			return p
		}
		id, _ := p.Get("id")
		if err := restateRecoveredPlanFile(root.Path, id.Str(), pf.Str()); err != nil {
			t.Fatalf("recovered-planfile restatement: %v", err)
		}
		return objWithout(p, "recoveredPlanFile")
	})
	return reencode(text, out)
}

// restateRecoveredPlanFile finds the planFile member in the raw bytes.
func restateRecoveredPlanFile(root, id, pf string) error {
	raw, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".json"))
	if err != nil {
		return err
	}
	i := bytes.Index(raw, []byte(`"planFile"`))
	if i < 0 {
		return fmt.Errorf("no planFile member in the raw bytes")
	}
	rest := bytes.TrimLeft(raw[i+len(`"planFile"`):], " \t\r\n")
	rest = bytes.TrimLeft(bytes.TrimPrefix(rest, []byte(":")), " \t\r\n")
	var want string
	if err := json.NewDecoder(bytes.NewReader(rest)).Decode(&want); err != nil {
		return fmt.Errorf("raw planFile: %v", err)
	}
	if strings.TrimPrefix(want, "./") != pf {
		return fmt.Errorf("recoveredPlanFile %q, raw member %q", pf, want)
	}
	return nil
}

// ---- wiped-plan note ----

const wipedNeedle = "phases: Plan was wiped (workplan_reset mode=wipe)"

func wipedNoteInverse(t *testing.T, v *vector, _ testutil.Root, _ *engine.Engine, _ ojson.Value, text string) string {
	t.Helper()
	if v.Call.Tool != "workplan_doctor" {
		t.Fatalf("wiped-note does not apply to %s", v.Call.Tool)
	}
	parsed, err := ojson.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	out := mapPlans(parsed.Value, "plans", func(p ojson.Value) ojson.Value {
		w, ok := p.Get("warnings")
		if !ok {
			return p
		}
		var keep []string
		for _, x := range w.Elems() {
			if !strings.HasPrefix(x.Str(), wipedNeedle) {
				keep = append(keep, x.Str())
			}
		}
		if len(keep) == 0 {
			return objWithout(p, "warnings")
		}
		return objReplace(p, "warnings", ojson.StringsValue(keep))
	})
	return reencode(text, out)
}
