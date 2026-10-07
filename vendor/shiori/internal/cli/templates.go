package cli

import (
	"sort"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Plan templates for `create --template`: standard phases whose steps
// carry a target, an action and a validation to replace with the real
// command, so a new plan starts with checkable steps.
type tmplStep struct{ id, title, target, action, validation string }

type tmplPhase struct {
	id, title string
	steps     []tmplStep
}

var templates = map[string][]tmplPhase{
	"feature": {
		{"design", "Design", []tmplStep{
			{"scope", "Agree scope and interfaces", "docs/", "Write down the behaviour, interfaces and non-goals", "The design note is reviewed and linked from the plan"},
		}},
		{"build", "Build", []tmplStep{
			{"tests-first", "Write failing tests for the new behaviour", "tests", "Add tests that pin the agreed behaviour", "`TEST_COMMAND` fails only on the new tests"},
			{"implement", "Implement the feature", "src", "Make the change behind the agreed interfaces", "`TEST_COMMAND` passes"},
		}},
		{"verify", "Verify and ship", []tmplStep{
			{"full-check", "Run the full check suite", ".", "Run lint, type checks and every test", "`CHECK_COMMAND` passes"},
			{"docs", "Update user-facing documentation", "docs/", "Document the feature and any migration", "Docs build and link checks pass"},
		}},
	},
	"bugfix": {
		{"reproduce", "Reproduce", []tmplStep{
			{"failing-test", "Reproduce the bug in a test", "tests", "Write the smallest test that shows the bug", "`TEST_COMMAND` fails with the reported symptom"},
		}},
		{"fix", "Fix", []tmplStep{
			{"root-cause", "Fix the root cause", "src", "Change the code that causes the bug, not its symptom", "`TEST_COMMAND` passes, including the new test"},
		}},
		{"verify", "Verify", []tmplStep{
			{"regressions", "Check for regressions", ".", "Run the full suite and the affected flows", "`CHECK_COMMAND` passes"},
		}},
	},
	"migration": {
		{"inventory", "Inventory", []tmplStep{
			{"usages", "List every usage of the old path", ".", "Find and record each caller, job and config that uses it", "The inventory is complete (a search for the old path finds only listed uses)"},
		}},
		{"prepare", "Prepare", []tmplStep{
			{"compat", "Make the change backward compatible", "src", "Support old and new side by side", "`TEST_COMMAND` passes with both paths enabled"},
		}},
		{"migrate", "Migrate", []tmplStep{
			{"batches", "Move callers in batches", "src", "Migrate callers in small, revertible batches", "`TEST_COMMAND` passes after each batch"},
		}},
		{"cleanup", "Clean up", []tmplStep{
			{"remove-old", "Remove the old path", "src", "Delete the old code, flags and config", "`CHECK_COMMAND` passes and a search for the old path finds nothing"},
		}},
	},
}

func templateNames() string {
	names := make([]string, 0, len(templates))
	for n := range templates {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// templatePhases renders a template as workplan_create phases.
func templatePhases(name string) (ojson.Value, bool) {
	t, ok := templates[name]
	if !ok {
		return ojson.Value{}, false
	}
	phases := make([]ojson.Value, len(t))
	for i, ph := range t {
		steps := make([]ojson.Value, len(ph.steps))
		for j, st := range ph.steps {
			steps[j] = ojson.NewObject(5).
				Set("id", ojson.StringValue(st.id)).
				Set("title", ojson.StringValue(st.title)).
				Set("target", ojson.StringValue(st.target)).
				Set("action", ojson.StringValue(st.action)).
				Set("validation", ojson.StringValue(st.validation)).Value()
		}
		phases[i] = ojson.NewObject(3).
			Set("id", ojson.StringValue(ph.id)).
			Set("title", ojson.StringValue(ph.title)).
			Set("steps", ojson.ArrayValue(steps)).Value()
	}
	return ojson.ArrayValue(phases), true
}
