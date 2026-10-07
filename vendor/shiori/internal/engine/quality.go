package engine

import (
	"regexp"
	"sort"
	"strings"

	"github.com/hoshinoht/shiori/internal/index"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// commandLike matches validation text that names something to run: a
// code span, or a leading command word.
var commandLike = regexp.MustCompile("`[^`]+`|^\\s*(?:\\$\\s*)?(?:go|npm|npx|pnpm|yarn|bun|deno|node|make|just|cargo|python3?|pytest|uv|tox|ruby|bundle|rake|mvn|gradle|./gradlew|dotnet|swift|xcodebuild|bash|sh|zsh|docker|kubectl|terraform|git|curl|shiori|\\./)\\b")

// planQuality lists advisory plan-quality findings: open steps without a
// validation or whose validation names nothing to run, and open high
// findings while no step is open to address them. ok=false: nothing to
// report.
func planQuality(p *model.Plan) (ojson.Value, bool) {
	var missing, prose []ojson.Value
	open := 0
	for _, ph := range p.Phases {
		for _, st := range ph.Steps {
			if !isActive(st.Status) {
				continue
			}
			open++
			ref := ojson.NewObject(2).Set("phaseId", ojson.StringValue(ph.ID)).Set("stepId", ojson.StringValue(st.ID)).Value()
			switch {
			case st.Validation == nil || model.Blank(*st.Validation):
				missing = append(missing, ref)
			case !commandLike.MatchString(*st.Validation):
				prose = append(prose, ref)
			}
		}
	}
	high := len(index.BuildBuckets(p).High())
	if len(missing) == 0 && len(prose) == 0 && (high == 0 || open > 0) {
		return ojson.Value{}, false
	}
	b := ojson.NewObject(4)
	if len(missing) > 0 {
		b.Set("stepsWithoutValidation", capped(missing))
	}
	if len(prose) > 0 {
		b.Set("stepsWithoutCommand", capped(prose))
	}
	if high > 0 && open == 0 {
		b.Set("highFindingsWithoutOpenWork", ojson.IntValue(int64(high)))
	}
	return b.Set("note", ojson.StringValue(strings.Join([]string{
		"Advisory: a validation that names a command (in backticks or as its first word) can be recorded and re-run as evidence;",
		"open blocker/critical/major findings need an open step to resolve them before the plan can finish.",
	}, " "))).Value(), true
}

func capped(refs []ojson.Value) ojson.Value {
	b := ojson.NewObject(2).Set("count", ojson.IntValue(int64(len(refs))))
	if len(refs) > evidenceListCap {
		refs = refs[:evidenceListCap]
	}
	return b.Set("steps", ojson.ArrayValue(refs)).Value()
}
