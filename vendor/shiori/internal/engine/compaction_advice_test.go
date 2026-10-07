package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// historyNote builds note i of the synthetic history (invented content). Its
// kind is fixed by i%10 for the 110 notes older than the latest 20:
//
//	0 decision (USER marker)   1 decision ("decided")   2 names open step p2/s8
//	3 [Pinned] marker          4 archive pointer (i==4) 5 names completed p0/s1
//	6 near-miss p2/s8x/xp2/s8  7 quotes the open finding 8 lowercase "user"
//	9 plain
func historyNote(i int) string {
	var head string
	switch {
	case i == 4:
		return "Compaction archive: .opencode/workplan/archive/hist/state-0123456789ab-0123456789ab.json"
	case i%10 == 0:
		head = fmt.Sprintf("2026-09-%02d USER DECISION %d: keep the queue", 1+i%28, i)
	case i%10 == 1:
		head = fmt.Sprintf("note %d: the owner decided to ship the parser", i)
	case i%10 == 2:
		head = fmt.Sprintf("note %d: progress on p2/s8, half done", i)
	case i%10 == 3:
		head = fmt.Sprintf("note %d [Pinned]: rollback recipe", i)
	case i%10 == 5:
		head = fmt.Sprintf("note %d: p0/s1 finished", i)
	case i%10 == 6:
		head = fmt.Sprintf("note %d: p2/s8x and xp2/s8 are other names", i)
	case i%10 == 7:
		head = fmt.Sprintf("note %d: see Open finding about the parser cache", i)
	case i%10 == 8:
		head = fmt.Sprintf("note %d: the user asked for a status", i)
	default:
		head = fmt.Sprintf("note %d: routine receipt", i)
	}
	return words(head, 800)
}

const historyNotes = 130

// seedHistory creates a plan with a long, mixed history and a fresh
// checkpoint: an archivable completed phase p0, a completed phase p1 with a
// cancelled step (not archivable), open phase p2, six resolved findings,
// one open finding and 130 notes.
func seedHistory(t *testing.T, notes int) (*Engine, testutil.Root) {
	t.Helper()
	root := testutil.NewRoot(t, "empty-workspace")
	e := mustEngine(t, root.Path)
	step := func(id, status string) string {
		return fmt.Sprintf(`{"id":%q,"title":%q,"target":"src/%s.go","action":%q,"validation":%q,"status":%q}`,
			id, "Step "+id, id, words("Action "+id, 900), words("Validation "+id, 400), status)
	}
	var ns []string
	for i := 0; i < notes; i++ {
		ns = append(ns, fmt.Sprintf("%q", historyNote(i)))
	}
	var fs []string
	for i := 0; i < 6; i++ {
		fs = append(fs, fmt.Sprintf(`{"severity":"minor","title":"Resolved finding %d","detail":%q,"status":"resolved"}`, i, words("Detail", 1200)))
	}
	fs = append(fs, `{"severity":"major","title":"Open finding about the parser cache","detail":"still open"}`)
	mustMutate(t, e, "workplan_create", fmt.Sprintf(`{"id":"hist","title":"History","goal":"Keep a long plan small","status":"in_progress",
		"phases":[{"id":"p0","title":"Done","status":"completed","steps":[%s,%s]},
		{"id":"p1","title":"Done with a cancelled step","status":"completed","steps":[%s,%s]},
		{"id":"p2","title":"Open","status":"in_progress","steps":[%s,%s]}],
		"reviewFindings":[%s],"notes":[%s]}`,
		step("s1", "completed"), step("s2", "completed"), step("s3", "completed"), step("s4", "cancelled"),
		step("s8", "draft"), step("s9", "in_progress"), strings.Join(fs, ","), strings.Join(ns, ",")))
	mustMutate(t, e, "workplan_checkpoint", `{"id":"hist","summary":"history seeded","nextAction":"continue p2/s9","phaseId":"p2","stepId":"s9"}`)
	return e, root
}

func historyDoctor(t *testing.T, e *Engine) (ojson.Value, string) {
	t.Helper()
	id := "hist"
	v, err := e.Doctor(input.DoctorInput{ID: &id})
	if err != nil {
		t.Fatal(err)
	}
	plans, _ := v.Get("plans")
	return plans.Elems()[0], string(ojson.Pretty(v))
}

func historyResume(t *testing.T, e *Engine, max, limit int) (ojson.Value, string) {
	t.Helper()
	v, text, err := e.Resume(input.ResumeInput{ID: "hist", MaxChars: &max, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	return v, text
}

func workplanFile(t *testing.T, root testutil.Root, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root.Path, ".opencode/workplan", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAdvisorReport: doctor has the full advice, resume the compact
// form; the counts restate from the raw plan; thresholds nobody crosses
// give output byte-identical to advice off.
func TestAdvisorReport(t *testing.T) {
	e, root := seedHistory(t, historyNotes)
	entry, _ := historyDoctor(t, e)
	a, ok := entry.Get("compactionRecommended")
	if !ok {
		t.Fatalf("no advice: %s", ojson.Compact(entry))
	}
	if err := checkAdvice(root.Path, "hist", "workplan_doctor", a); err != nil {
		t.Fatal(err)
	}
	kept := map[string]int{"latest": 20, "decision": 22, "openReference": 22, "pinned": 11, "archivePointer": 1}
	for k, n := range kept {
		if got := numMember(a, "notes", "kept", k); got != n {
			t.Errorf("kept.%s = %d, want %d", k, got, n)
		}
	}
	if got := numMember(a, "notes", "eligible"); got != 110-56 {
		t.Errorf("eligible %d", got)
	}
	if got := strs(getPath(a, "selection", "completedPhaseIds")); strings.Join(got, ",") != "p0" {
		t.Errorf("phases %v", got)
	}
	if got := numMember(a, "terminalSteps", "total"); got != 4 {
		t.Errorf("terminal %d", got)
	}
	if got := numMember(a, "terminalSteps", "archivable"); got != 2 {
		t.Errorf("archivable %d", got)
	}
	if strMember(a, "estimate", "markdown", "treatment") != "generated-refresh" || numMember(a, "estimate", "markdown", "saved") <= 0 {
		t.Errorf("markdown estimate %s", ojson.Compact(getPath(a, "estimate", "markdown")))
	}
	if !containsPrefix(strs(getPath(a, "reasons")), "notes: 54 notes are eligible") {
		t.Errorf("reasons %v", strs(getPath(a, "reasons")))
	}
	// Resume carries the compact form with the same figures.
	r, _ := historyResume(t, e, 64000, 20)
	c, ok := r.Get("compactionRecommended")
	if !ok {
		t.Fatal("resume has no compact advice")
	}
	if numMember(c, "savedJsonBytes") != numMember(a, "estimate", "json", "saved") || numMember(c, "savedJsonPercent") != numMember(a, "estimate", "json", "percent") ||
		numMember(c, "notes") != 54 || numMember(c, "terminalSteps") != 2 || numMember(c, "resolvedFindings") != 6 {
		t.Errorf("resume advice %s vs doctor %s", ojson.Compact(c), ojson.Compact(a))
	}
	// A threshold nobody crosses: byte-identical to advice off.
	off := adviceOff(e)
	offEntry, offDoc := historyDoctor(t, off)
	offV, offRes := historyResume(t, off, 12000, 20)
	if testutil.Has(offEntry, "compactionRecommended") || testutil.Has(offV, "compactionRecommended") {
		t.Fatal("advice off still advises")
	}
	for _, th := range []*advisor.Thresholds{{MinSavingsBytes: 1 << 30}, {Notes: 1000, TerminalPercent: 100, PlanBytes: 1 << 30}} {
		q := *e
		q.Compaction = th
		if _, d := historyDoctor(t, &q); d != offDoc {
			t.Errorf("%+v: doctor differs from advice off", *th)
		}
		if _, rs := historyResume(t, &q, 12000, 20); rs != offRes {
			t.Errorf("%+v: resume differs from advice off", *th)
		}
	}
}

// adviceOff is e with the compaction advice turned off.
func adviceOff(e *Engine) *Engine {
	off := *e
	off.Compaction = &advisor.Thresholds{Off: true}
	return &off
}

func getPath(v ojson.Value, keys ...string) ojson.Value {
	for _, k := range keys {
		v, _ = v.Get(k)
	}
	return v
}

// TestAdviceMatchesApply: applying the advised selection writes exactly
// the estimated JSON and Markdown sizes, and the plan is then no longer
// recommended.
func TestAdviceMatchesApply(t *testing.T) {
	e, root := seedHistory(t, historyNotes)
	entry, _ := historyDoctor(t, e)
	a := getPath(entry, "compactionRecommended")
	sel := getPath(a, "selection")
	in := fmt.Sprintf(`{"id":"hist","archiveReason":"advice","completedPhaseIds":%s,"noteRollover":%s,"resolvedFindingIndexes":%s}`,
		ojson.Compact(getPath(sel, "completedPhaseIds")), ojson.Compact(getPath(sel, "noteRollover")), ojson.Compact(getPath(sel, "resolvedFindingIndexes")))
	pv := mustMutate(t, e, "workplan_compact", in).Value
	for _, k := range []string{"json", "markdown", "total"} {
		if ojson.Compact(getPath(pv, "estimatedSavings", k)) == nil || numMember(pv, "estimatedSavings", k, "after") != numMember(a, "estimate", k, "after") {
			t.Errorf("%s: preview %s, advice %s", k, ojson.Compact(getPath(pv, "estimatedSavings", k)), ojson.Compact(getPath(a, "estimate", k)))
		}
	}
	h := stateHashFor(t, e, "hist")
	apply := strings.TrimSuffix(in, "}") + fmt.Sprintf(`,"mode":"apply","previewToken":%q,"confirmation":"ARCHIVE_SELECTED_HISTORY","expectedHash":%q}`, strMember(pv, "previewToken"), h)
	out := mustMutate(t, e, "workplan_compact", apply).Value
	if got := len(workplanFile(t, root, "hist.json")); got != numMember(a, "estimate", "json", "after") || got != numMember(out, "savings", "json", "after") {
		t.Errorf("plan JSON %d bytes, estimated %d, reported %d", got, numMember(a, "estimate", "json", "after"), numMember(out, "savings", "json", "after"))
	}
	if got := len(workplanFile(t, root, "hist.md")); got != numMember(a, "estimate", "markdown", "after") {
		t.Errorf("Markdown %d bytes, estimated %d", got, numMember(a, "estimate", "markdown", "after"))
	}
	entry, _ = historyDoctor(t, e)
	if testutil.Has(entry, "compactionRecommended") {
		t.Errorf("still recommended after applying the advice: %s", ojson.Compact(getPath(entry, "compactionRecommended", "reasons")))
	}
}

// TestAdviceResumeBudget: every packet fits; the advice is never kept in a
// packet below the readability minimums, a packet without it is
// byte-identical to the packet with advice off, and a packet with it is
// that packet plus the member (the advice never costs page items or text).
func TestAdviceResumeBudget(t *testing.T) {
	e, _ := seedHistory(t, historyNotes)
	// A heavy pinned header (the shape of the owner's roadmap) so that the
	// small budgets need every level.
	list := func(prefix string, n, size int) string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("%q", words(fmt.Sprintf("%s %d", prefix, i), size)))
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	h := stateHashFor(t, e, "hist")
	mustMutate(t, e, "workplan_update", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"goal":%q,"scope":%s,"nonGoals":%s,"constraints":%s}`,
		h, words("Goal", 290), list("Scope", 6, 180), list("Non-goal", 5, 150), list("Constraint", 8, 200)))
	mustMutate(t, e, "workplan_checkpoint", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"summary":%q,"nextAction":%q,"phaseId":"p2","stepId":"s9","guardrails":%s,"recentValidation":%s}`,
		stateHashFor(t, e, "hist"), words("Summary", 700), words("Next action", 300), list("Guardrail", 3, 200), list("Validation", 3, 200)))
	off := adviceOff(e)
	shown, dropped := 0, 0
	for max := 4096; max <= 16000; max += budgetStep(max) {
		for _, limit := range []int{1, 8, 20} {
			on, text := historyResume(t, e, max, limit)
			if ojson.UTF16Len(text) > max {
				t.Fatalf("%d/%d: %d over budget", max, limit, ojson.UTF16Len(text))
			}
			if !testutil.Has(on, "compactionRecommended") {
				dropped++
				if _, o := historyResume(t, off, max, limit); o != text {
					t.Fatalf("%d/%d: advice dropped but the packet differs from advice off", max, limit)
				}
				continue
			}
			shown++
			if belowMinimums(on) {
				t.Fatalf("%d/%d: advice kept in a packet below the readability minimums", max, limit)
			}
			// The advice never costs page content: the packet minus the
			// advice is the advice-off packet byte-for-byte (same page
			// items, same text caps).
			offV, offText := historyResume(t, off, max, limit)
			if pageReturned(on) != pageReturned(offV) {
				t.Fatalf("%d/%d: advice reduced the page from %d to %d items", max, limit, pageReturned(offV), pageReturned(on))
			}
			inv := string(ojson.Pretty(testutil.Without(on, "compactionRecommended")))
			if !strings.HasPrefix(text, "{\n") {
				inv = string(ojson.Compact(testutil.Without(on, "compactionRecommended")))
			}
			if inv != offText {
				t.Fatalf("%d/%d: packet minus the advice differs from advice off\n%s", max, limit, testutil.FirstDiff(inv, offText))
			}
		}
	}
	t.Logf("advice shown in %d packets, dropped in %d", shown, dropped)
	if shown == 0 || dropped == 0 {
		t.Fatal("the budgets did not exercise both keeping and dropping the advice")
	}
}

// TestNoEligibleHistoryUnchanged: fixtures and a short plan without
// qualifying history give resume and doctor output byte-identical to
// advice off.
func TestNoEligibleHistoryUnchanged(t *testing.T) {
	check := func(label string, e *Engine, id string) {
		off := adviceOff(e)
		for _, max := range []int{4096, 12000, 64000} {
			_, a, err1 := e.Resume(input.ResumeInput{ID: id, MaxChars: &max})
			_, b, err2 := off.Resume(input.ResumeInput{ID: id, MaxChars: &max})
			if (err1 == nil) != (err2 == nil) || a != b {
				t.Errorf("%s resume %d differs", label, max)
			}
		}
		d1, _ := e.Doctor(input.DoctorInput{})
		d2, _ := off.Doctor(input.DoctorInput{})
		if string(ojson.Pretty(d1)) != string(ojson.Pretty(d2)) {
			t.Errorf("%s doctor differs", label)
		}
	}
	for fixture, id := range map[string]string{"minimal-valid": "minimal", "full-valid": "full-plan", "large-paging": "big-plan", "resume-stress": "stress-plan", "handwritten-md": "hand-plan"} {
		root := testutil.NewRoot(t, fixture)
		check(fixture, mustEngine(t, root.Path), id)
	}
	e, _ := seedHistory(t, 25)
	check("25 notes", e, "hist")
}

// TestAdvisorPointerEstimate: the advisor counts older pointer
// notes beyond the latest three as eligible, and its figures restate from
// the raw plan.
func TestAdvisorPointerEstimate(t *testing.T) {
	e, root := seedHistory(t, historyNotes) // note 4 is an archive pointer
	var add []string
	for i := 0; i < 8; i++ {
		add = append(add, fmt.Sprintf("%q", fmt.Sprintf("Compaction archive: .opencode/workplan/archive/hist/state-%012d-0123456789ab.json", i)))
	}
	for i := 0; i < 25; i++ {
		add = append(add, fmt.Sprintf("%q", words(fmt.Sprintf("later note %d", i), 300)))
	}
	mustMutate(t, e, "workplan_update", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"appendNotes":[%s]}`, stateHashFor(t, e, "hist"), strings.Join(add, ",")))
	mustMutate(t, e, "workplan_checkpoint", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"summary":"pointers","nextAction":"continue","phaseId":"p2","stepId":"s9"}`, stateHashFor(t, e, "hist")))
	entry, _ := historyDoctor(t, e)
	a, ok := entry.Get("compactionRecommended")
	if !ok {
		t.Fatalf("no advice: %s", ojson.Compact(entry))
	}
	if err := checkAdvice(root.Path, "hist", "workplan_doctor", a); err != nil {
		t.Fatal(err)
	}
	if got := numMember(a, "notes", "kept", "archivePointer"); got != advisor.KeepArchivePointers {
		t.Errorf("kept.archivePointer %d, want %d", got, advisor.KeepArchivePointers)
	}
	// 143 notes are older than the latest 20: the 130 seeded notes (65
	// eligible, among them the seeded pointer at index 4, no longer among
	// the latest three pointers),
	// the 8 added pointers (the latest 3 kept, 5 eligible) and 5 later notes.
	if got := numMember(a, "notes", "eligible"); got != 65+5+5 {
		t.Errorf("eligible %d, want %d", got, 65+5+5)
	}
	// The advised selection applies to exactly the estimated size.
	sel := getPath(a, "selection")
	in := fmt.Sprintf(`{"id":"hist","archiveReason":"advice","completedPhaseIds":%s,"noteRollover":%s,"resolvedFindingIndexes":%s}`,
		ojson.Compact(getPath(sel, "completedPhaseIds")), ojson.Compact(getPath(sel, "noteRollover")), ojson.Compact(getPath(sel, "resolvedFindingIndexes")))
	pv := mustMutate(t, e, "workplan_compact", in).Value
	apply := strings.TrimSuffix(in, "}") + fmt.Sprintf(`,"mode":"apply","previewToken":%q,"confirmation":"ARCHIVE_SELECTED_HISTORY","expectedHash":%q}`, strMember(pv, "previewToken"), stateHashFor(t, e, "hist"))
	mustMutate(t, e, "workplan_compact", apply)
	if got := len(workplanFile(t, root, "hist.json")); got != numMember(a, "estimate", "json", "after") {
		t.Errorf("plan JSON %d bytes, estimated %d", got, numMember(a, "estimate", "json", "after"))
	}
}
