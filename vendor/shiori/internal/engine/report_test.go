package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/testutil"
)

func TestReport(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	mustRun(t, e, "update", `{"id":"full-plan","expectedHash":"`+stateHash(t, e, "full-plan")+`","updateSteps":[{"phaseId":"phase-b","stepId":"step-b1","title":"B1 | piped"}]}`)
	v, md, err := e.Report(context.Background(), "full-plan", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Full plan", "2 of 5 steps completed (40%)", "| phase-a Phase A | completed | 2/2 |",
		"`phase-b/step-b1` B1 | piped", "(waiting on prerequisites)", "## Critical path", "**blocker** Blocker finding",
		"## Recent activity", "agent `update`: phases/phase-b/steps/step-b1 changed"} {
		if !strings.Contains(md, want) {
			t.Fatalf("report lacks %q:\n%s", want, md)
		}
	}
	if n, _ := v.Get("stepsCompleted"); n.NumberLiteral() != "2" {
		t.Fatal("stepsCompleted")
	}
	if _, ok := v.Get("recentActivity"); !ok {
		t.Fatal("no recentActivity")
	}
}
