package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/resume"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func mustMutate(t *testing.T, e *Engine, tool, input string) Output {
	t.Helper()
	p, err := ojson.Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	out, err := runMutation(context.Background(), e, tool, p.Value, &countingAuth{})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func words(prefix string, n int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, " clause-%d keeps the text readable", i)
	}
	return b.String()[:n]
}

// seedRoadmap creates a synthetic 13-phase/38-step roadmap with long prose
// and a fresh checkpoint (the shape of the owner's real plan, invented
// content only).
func seedRoadmap(t *testing.T) (*Engine, testutil.Root) {
	t.Helper()
	root := testutil.NewRoot(t, "empty-workspace")
	e := mustEngine(t, root.Path)
	var phases []string
	step := 0
	for p := 0; p < 13; p++ {
		n := 3
		if p == 12 {
			n = 2
		}
		var steps []string
		for s := 0; s < n; s++ {
			step++
			status := "draft"
			if p < 2 {
				status = "completed"
			}
			steps = append(steps, fmt.Sprintf(`{"id":"s%d","title":%q,"target":%q,"action":%q,"validation":%q,"status":%q}`,
				step, words(fmt.Sprintf("Step %d title", step), 70), words("src/module/, tests/", 220),
				words(fmt.Sprintf("R%02d action", step), 330), words("Validation", 300), status))
		}
		status := "draft"
		if p < 2 {
			status = "completed"
		}
		phases = append(phases, fmt.Sprintf(`{"id":"p%d","title":%q,"status":%q,"steps":[%s]}`, p, words(fmt.Sprintf("Phase %d title", p), 90), status, strings.Join(steps, ",")))
	}
	list := func(prefix string, n, size int) string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("%q", words(fmt.Sprintf("%s %d", prefix, i), size)))
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	var files []string
	for i := 0; i < 8; i++ {
		files = append(files, fmt.Sprintf("%q", fmt.Sprintf(".opencode/workplan/some-long-related-plan-name-%d.json", i)))
	}
	mustMutate(t, e, "workplan_create", fmt.Sprintf(`{"id":"roadmap","title":"Synthetic staged roadmap","goal":%q,
		"scope":%s,"nonGoals":%s,"constraints":%s,"relevantFiles":[%s],"phases":[%s],
		"reviewFindings":[{"severity":"minor","title":%q,"detail":%q}],"notes":["n1","n2"]}`,
		words("Goal", 290), list("Scope", 6, 180), list("Non-goal", 5, 150), list("Constraint", 8, 200), strings.Join(files, ","),
		strings.Join(phases, ","), words("Finding", 90), words("Detail", 200)))
	mustMutate(t, e, "workplan_checkpoint", fmt.Sprintf(`{"id":"roadmap","summary":%q,"nextAction":%q,"phaseId":"p2","stepId":"s7",
		"guardrails":%s,"references":[".opencode/workplan/roadmap.json",".opencode/workplan/roadmap.md","docs/spec-a.md","docs/spec-b.md"],
		"recentValidation":%s}`, words("Summary", 700), words("Next action", 300), list("Guardrail", 3, 200), list("Validation", 3, 200)))
	return e, root
}

// walkStrings visits every string with its path.
func walkStrings(v ojson.Value, path string, f func(path, s string)) {
	switch v.Kind() {
	case ojson.Object:
		for _, m := range v.Members() {
			walkStrings(m.Value, path+"."+m.Key, f)
		}
	case ojson.Array:
		for _, el := range v.Elems() {
			walkStrings(el, path+"[]", f)
		}
	case ojson.String:
		f(path, v.Str())
	}
}

func pageReturned(v ojson.Value) int {
	pg, _ := v.Get("page")
	r, _ := pg.Get("returned")
	n, _ := r.Float()
	return int(n)
}

// checkReadable asserts the readability invariants for a packet that
// is not at an emergency level (more than one page item): no display
// string below the minimums, and no protected string shortened.
func checkReadable(t *testing.T, label string, v ojson.Value) {
	t.Helper()
	walkStrings(v, "", func(p, s string) {
		if !strings.HasSuffix(s, "…") {
			return
		}
		n := ojson.UTF16Len(s)
		protected := p == ".path" || p == ".planFile" || p == ".instruction" ||
			strings.HasSuffix(p, ".relevantFiles[]") || strings.HasSuffix(p, ".references[]") || strings.HasSuffix(p, ".reference")
		title := strings.HasSuffix(p, "itle")
		switch {
		case protected:
			t.Errorf("%s: protected %s truncated", label, p)
		case title && n < resume.MinTitle-1:
			t.Errorf("%s: title %s shortened to %d", label, p, n)
		case !title && n < resume.MinLong-1:
			t.Errorf("%s: %s shortened to %d", label, p, n)
		}
	})
}

// checkTargetOrder: a page smaller than the target page (while enough
// items remain) only carries page-item prose at the 120 floor, i.e. text
// shrank before the page did.
func checkTargetOrder(t *testing.T, label string, v ojson.Value, budget, limit int) {
	t.Helper()
	pgv, _ := v.Get("page")
	omv, _ := pgv.Get("omitted")
	om, _ := omv.Float()
	ret := pageReturned(v)
	tg := resume.TargetPage(budget, limit)
	if ret == 0 || ret >= tg || ret+int(om) < tg {
		return
	}
	items, _ := pgv.Get("items")
	walkStrings(items, "page.items", func(p, s string) {
		if strings.HasSuffix(s, "…") && !strings.HasSuffix(p, "itle") && ojson.UTF16Len(s) > resume.MinLong {
			t.Errorf("%s: page %d below target %d with %s at %d units", label, ret, tg, p, ojson.UTF16Len(s))
		}
	})
}

// TestResumeReadability is item A on a roadmap-shaped plan: the default
// budget carries full text with fewer items; smaller budgets keep the
// minimums; every page fits and the cursor covers every item.
func TestResumeReadability(t *testing.T) {
	e, _ := seedRoadmap(t)
	for _, b := range []int{64000, 20000, 12000, 8000, 6000} {
		b := b
		in := input.ResumeInput{ID: "roadmap", MaxChars: &b}
		seen, pages := 0, 0
		for {
			v, text, err := e.Resume(in)
			if err != nil {
				t.Fatalf("budget %d: %v", b, err)
			}
			if n := ojson.UTF16Len(text); n > b {
				t.Fatalf("budget %d: %d code units", b, n)
			}
			ret := pageReturned(v)
			if ret == 0 {
				t.Fatalf("budget %d: empty page", b)
			}
			checkReadable(t, fmt.Sprintf("budget %d page %d", b, pages), v)
			cp, _ := v.Get("checkpoint")
			sum, _ := cp.Get("summary")
			next, _ := cp.Get("nextAction")
			if ojson.UTF16Len(sum.Str()) < resume.MinLong-1 || ojson.UTF16Len(next.Str()) < resume.MinLong-1 {
				t.Fatalf("budget %d: summary/nextAction unreadable", b)
			}
			if b >= 20000 {
				// Room for full text at the target page.
				tc, _ := v.Get("truncatedFieldCount")
				if n, _ := tc.Float(); n != 0 {
					t.Errorf("budget %d: %v fields truncated, want none", b, n)
				}
			}
			// Text shrinks before the page drops below the target: a page
			// smaller than the target only carries floor-level item prose.
			checkTargetOrder(t, fmt.Sprintf("budget %d", b), v, b, DefaultResumeLimit)
			if b >= 12000 && (ojson.UTF16Len(sum.Str()) < resume.PinnedMin-1 && !strings.HasPrefix(words("Summary", 700), sum.Str())) {
				t.Errorf("budget %d: current-work summary at %d units, want >= %d", b, ojson.UTF16Len(sum.Str()), resume.PinnedMin)
			}
			seen += ret
			pages++
			pg, _ := v.Get("page")
			nc, _ := pg.Get("nextCursor")
			if nc.Kind() != ojson.String {
				tot, _ := pg.Get("total")
				if n, _ := tot.Float(); int(n) != seen {
					t.Fatalf("budget %d: paged %d of %v", b, seen, n)
				}
				break
			}
			c := nc.Str()
			in.Cursor = &c
		}
		t.Logf("budget %d: %d pages", b, pages)
	}
	// The default budget surveys several items with readable text.
	v, _, err := e.Resume(input.ResumeInput{ID: "roadmap"})
	if err != nil {
		t.Fatal(err)
	}
	if r := pageReturned(v); r < 4 {
		t.Fatalf("default budget returned %d items", r)
	}
}

// TestResumeMinimumBudget: at the smallest accepted budget a packet
// still fits, and anything shortened below the minimums is explicit.
func TestResumeMinimumBudget(t *testing.T) {
	e, _ := seedRoadmap(t)
	b := MinResumeMaxChars
	v, text, err := e.Resume(input.ResumeInput{ID: "roadmap", MaxChars: &b})
	if err != nil {
		t.Fatal(err)
	}
	if ojson.UTF16Len(text) > b || pageReturned(v) != 1 {
		t.Fatalf("len %d returned %d", ojson.UTF16Len(text), pageReturned(v))
	}
	sf, _ := v.Get("safety")
	ov, _ := sf.Get("overflow")
	tc, _ := v.Get("truncatedFieldCount")
	n, _ := tc.Float()
	cut := 0
	walkStrings(v, "", func(p, s string) {
		if strings.HasSuffix(s, "…") {
			cut++
		}
	})
	if cut > 0 && (!ov.Bool() || int(n) < cut) {
		t.Fatalf("truncation not marked: cut %d count %v overflow %v", cut, n, ov.Bool())
	}
	// Machine fields are intact.
	h, _ := v.Get("hashes")
	ph, _ := h.Get("planHash")
	if len(ph.Str()) != 64 {
		t.Fatal("planHash missing")
	}
}

// belowMinimums reports a display string cut below every readability
// floor (80 code units), i.e. a packet that needed the emergency caps.
func belowMinimums(v ojson.Value) bool {
	below := false
	walkStrings(v, "", func(_, s string) {
		if strings.HasSuffix(s, "…") && ojson.UTF16Len(s) < resume.MinTitle {
			below = true
		}
	})
	return below
}

// TestResumeAdvisoryBudget: every packet fits its budget, the stale
// diagnostic is always present, and the advisory critical path is dropped
// before any text goes below the readability minimums.
func TestResumeAdvisoryBudget(t *testing.T) {
	e, root := seedGraphRoadmap(t)
	// Make the checkpoint stale so both members are present.
	os.WriteFile(filepath.Join(root.Path, ".opencode/workplan/roadmap.md"), []byte("# edited\n"), 0o644)
	withCP, dropped, costly := 0, 0, 0
	for max := 4096; max <= 16000; max += budgetStep(max) {
		for _, limit := range []int{1, 8, 20} {
			on, text, err := e.Resume(input.ResumeInput{ID: "roadmap", MaxChars: &max, Limit: &limit})
			if err != nil {
				t.Fatal(err)
			}
			if ojson.UTF16Len(text) > max {
				t.Fatalf("%d/%d: %d over budget", max, limit, ojson.UTF16Len(text))
			}
			o, _ := resumeEdited(t, e, input.ResumeInput{ID: "roadmap", MaxChars: &max, Limit: &limit}, withoutAdvisory)
			// The advisory critical path is kept only while no text goes
			// below the readability minimums.
			if testutil.Has(on, "criticalPath") && belowMinimums(on) {
				t.Fatalf("%d/%d: critical path kept in a packet below the readability minimums", max, limit)
			}
			if belowMinimums(on) && !belowMinimums(o) {
				costly++ // the stale diagnostic alone (a pinned safety item)
			}
			cp, _ := on.Get("checkpoint")
			if d, _ := cp.Get("diagnostic"); !strings.HasPrefix(d.Str(), "changed: ") {
				t.Fatalf("%d/%d: diagnostic %s", max, limit, ojson.Compact(d))
			}
			if testutil.Has(on, "criticalPath") {
				withCP++
			} else {
				dropped++
			}
		}
	}
	if withCP == 0 {
		t.Fatal("the critical path never appears")
	}
	t.Logf("critical path shown in %d packets, dropped to keep the minimums in %d; stale diagnostic alone needed the emergency caps in %d", withCP, dropped, costly)
	if dropped == 0 {
		t.Fatal("no budget exercised dropping the critical path")
	}
}

func budgetStep(max int) int {
	if max < 6000 {
		return 8
	}
	return 128
}

// List and path caps between the corpus budgets, as measured on the
// reference (large-paging fixture). The budget policy keeps both caps; it
// only changes which degradation level is chosen, so the large-paging
// packets at these budgets no longer truncate anything. The pinned-list cap
// is still observable there; the listed-path cap is checked on
// resume-stress, which still truncates.
func TestResumeCapsBetweenCorpusBudgets(t *testing.T) {
	root := testutil.NewRoot(t, "large-paging")
	e, _ := New(root.Path)
	stress := testutil.NewRoot(t, "resume-stress")
	es, _ := New(stress.Path)
	cases := []struct{ max, list, paths int }{
		{4096, 4, 16}, {4503, 5, 17}, {5428, 6, 21}, {6316, 7, 24}, {7204, 8, 28}, {8203, 8, 32},
	}
	for _, c := range cases {
		if resume.ListCap(c.max) != c.list || resume.PathCap(c.max) != c.paths {
			t.Errorf("maxChars %d: caps %d/%d want %d/%d", c.max, resume.ListCap(c.max), resume.PathCap(c.max), c.list, c.paths)
		}
		mc := c.max
		v, _, err := e.Resume(input.ResumeInput{ID: "big-plan", MaxChars: &mc})
		if err != nil {
			t.Fatal(err)
		}
		cp, _ := v.Get("checkpoint")
		bl, _ := cp.Get("blockers")
		if len(bl.Elems()) != c.list {
			t.Errorf("maxChars %d: list %d want %d", c.max, len(bl.Elems()), c.list)
		}
		sv, _, err := es.Resume(input.ResumeInput{ID: "stress-plan", MaxChars: &mc})
		if err != nil {
			t.Fatal(err)
		}
		tf, _ := sv.Get("truncatedFields")
		cnt, _ := sv.Get("truncatedFieldCount")
		n, _ := cnt.Float()
		want := c.paths
		if int(n) < want {
			want = int(n)
		}
		if n == 0 || len(tf.Elems()) != want {
			t.Errorf("maxChars %d: stress listed paths %d want %d (count %d)", c.max, len(tf.Elems()), want, int(n))
		}
	}
}
