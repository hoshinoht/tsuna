package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
)

func pointerNotes(notes []string) []string {
	var out []string
	for _, n := range notes {
		if strings.HasPrefix(n, advisorPointerPrefix) {
			out = append(out, n)
		}
	}
	return out
}

// TestArchivePointerRetention: successive rollovers keep the
// latest three archive pointer notes; older ones are archived (complete
// text) like ordinary notes, so after an apply the plan holds at most
// four (three kept plus the new one).
func TestArchivePointerRetention(t *testing.T) {
	e, _ := seedHistory(t, 30) // note 4 is an archive pointer
	auth := &countingAuth{}
	run := func(in string) ojson.Value {
		t.Helper()
		v, _ := ojson.Parse([]byte(in))
		out, err := runMutation(context.Background(), e, "workplan_compact", v.Value, auth)
		if err != nil {
			t.Fatal(err)
		}
		return out.Value
	}
	for round := 1; round <= 6; round++ {
		h := stateHashFor(t, e, "hist")
		mustMutate(t, e, "workplan_update", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"appendNotes":["round %d a","round %d b","round %d c"]}`, h, round, round, round))
		mustMutate(t, e, "workplan_checkpoint", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"summary":"round %d","nextAction":"continue","phaseId":"p2","stepId":"s9"}`,
			stateHashFor(t, e, "hist"), round))
		s, err := e.load("hist")
		if err != nil {
			t.Fatal(err)
		}
		before := s.Plan.Notes
		ptrs := pointerNotes(before)
		wantArchived := map[string]bool{}
		if len(ptrs) > advisor.KeepArchivePointers {
			for _, n := range ptrs[:len(ptrs)-advisor.KeepArchivePointers] {
				wantArchived[n] = true
			}
		}
		roll := `{"keepLatest":2}`
		pv := run(fmt.Sprintf(`{"id":"hist","archiveReason":"round %d","noteRollover":%s}`, round, roll))
		keptPtr := len(ptrs)
		if keptPtr > advisor.KeepArchivePointers {
			keptPtr = advisor.KeepArchivePointers
		}
		if got := numMember(pv, "noteRollover", "kept", "archivePointer"); got != keptPtr {
			t.Fatalf("round %d: kept.archivePointer %d, want %d", round, got, keptPtr)
		}
		out := run(fmt.Sprintf(`{"id":"hist","archiveReason":"round %d","noteRollover":%s,"mode":"apply","previewToken":%q,"confirmation":"ARCHIVE_SELECTED_HISTORY","expectedHash":%q}`,
			round, roll, strMember(pv, "previewToken"), stateHashFor(t, e, "hist")))
		arch, err := os.ReadFile(strMember(out, "archivePath"))
		if err != nil {
			t.Fatal(err)
		}
		archived := map[string]bool{}
		for _, r := range getPath(parseT(t, string(arch)), "removed", "noteIndexes").Elems() {
			i := numMember(r, "index")
			if strMember(r, "text") != before[i] {
				t.Fatalf("round %d: archived note %d is not the complete original", round, i)
			}
			if strings.HasPrefix(before[i], advisorPointerPrefix) {
				archived[before[i]] = true
			}
		}
		if len(archived) != len(wantArchived) {
			t.Fatalf("round %d: archived %d pointer notes, want %d", round, len(archived), len(wantArchived))
		}
		for n := range wantArchived {
			if !archived[n] {
				t.Fatalf("round %d: older pointer %q not archived", round, n)
			}
		}
		s, err = e.load("hist")
		if err != nil {
			t.Fatal(err)
		}
		after := pointerNotes(s.Plan.Notes)
		if want := keptPtr + 1; len(after) != want {
			t.Fatalf("round %d: %d pointer notes after apply, want %d", round, len(after), want)
		}
		if strings.Join(after[:len(after)-1], "\x00") != strings.Join(ptrs[len(ptrs)-keptPtr:], "\x00") {
			t.Fatalf("round %d: the kept pointers are not the latest %d", round, keptPtr)
		}
		t.Logf("round %d: %d pointer notes before, %d archived, %d after", round, len(ptrs), len(archived), len(after))
	}
}

// TestRolloverPreviewApply covers the selection rule, the token
// binding, the fresh-checkpoint requirement, complete archived originals
// and the resulting notes.
func TestRolloverPreviewApply(t *testing.T) {
	e, root := seedHistory(t, historyNotes)
	orig := workplanFile(t, root, "hist.json")
	preview := func(roll string) ojson.Value {
		return mustMutate(t, e, "workplan_compact", `{"id":"hist","archiveReason":"rollover","noteRollover":`+roll+`}`).Value
	}
	p := preview(`{}`)
	var want []int
	for i := 0; i < historyNotes-20; i++ {
		switch {
		case i == 4, i%10 == 0, i%10 == 1, i%10 == 2, i%10 == 3, i%10 == 7:
		default:
			want = append(want, i)
		}
	}
	if got := string(ojson.Compact(getPath(p, "canonicalSelection", "noteIndexes"))); got != string(ojson.Compact(intsValue(want))) {
		t.Fatalf("selected %s, want %v", got, want)
	}
	if string(ojson.Compact(getPath(p, "canonicalSelection", "noteRollover"))) != `{"keepLatest":20,"pinNoteIndexes":[]}` {
		t.Errorf("canonical rollover %s", ojson.Compact(getPath(p, "canonicalSelection", "noteRollover")))
	}
	if numMember(p, "noteRollover", "selectedCount") != len(want) || numMember(p, "noteRollover", "olderThanLatest") != historyNotes-20 {
		t.Errorf("rollover %s", ojson.Compact(getPath(p, "noteRollover")))
	}
	if numMember(p, "estimatedSavings", "json", "before") != len(orig) {
		t.Errorf("savings before %d, file %d", numMember(p, "estimatedSavings", "json", "before"), len(orig))
	}
	// The token binds keepLatest and the pins.
	p30 := preview(`{"keepLatest":30}`)
	pPin := preview(`{"pinNoteIndexes":[5]}`)
	tok := strMember(p, "previewToken")
	if strMember(p30, "previewToken") == tok || strMember(pPin, "previewToken") == tok {
		t.Fatal("token does not bind the rollover parameters")
	}
	if numMember(pPin, "noteRollover", "kept", "pinned") != 12 || numMember(pPin, "noteRollover", "selectedCount") != len(want)-1 {
		t.Errorf("pin: %s", ojson.Compact(getPath(pPin, "noteRollover")))
	}
	h := stateHashFor(t, e, "hist")
	applyIn := func(roll, token, hash string) string {
		return fmt.Sprintf(`{"id":"hist","archiveReason":"rollover","noteRollover":%s,"mode":"apply","previewToken":%q,"confirmation":"ARCHIVE_SELECTED_HISTORY","expectedHash":%q}`, roll, token, hash)
	}
	auth := &countingAuth{}
	run := func(in string) (Output, error) {
		v, _ := ojson.Parse([]byte(in))
		return runMutation(context.Background(), e, "workplan_compact", v.Value, auth)
	}
	if _, err := run(applyIn(`{"keepLatest":30}`, tok, h)); err == nil || err.Error() != msgWrongToken {
		t.Fatalf("different keepLatest: %v", err)
	}
	// A note appended after the checkpoint: apply needs a fresh one, so
	// no note newer than the checkpoint can be archived.
	mustMutate(t, e, "workplan_update", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"appendNotes":["late note"]}`, h))
	h2 := stateHashFor(t, e, "hist")
	stale := preview(`{}`)
	if _, err := run(applyIn(`{}`, strMember(stale, "previewToken"), h2)); err == nil || err.Error() != msgFreshCheckpoint {
		t.Fatalf("stale checkpoint: %v", err)
	}
	if auth.n != 0 {
		t.Fatalf("refusals reached authorization %d times", auth.n)
	}
	mustMutate(t, e, "workplan_checkpoint", fmt.Sprintf(`{"id":"hist","expectedHash":%q,"summary":"again","nextAction":"continue","phaseId":"p2","stepId":"s9"}`, h2))
	before := workplanFile(t, root, "hist.json")
	var beforeNotes []string
	{
		s, err := e.load("hist")
		if err != nil {
			t.Fatal(err)
		}
		beforeNotes = s.Plan.Notes
	}
	fresh := preview(`{}`)
	h3 := stateHashFor(t, e, "hist")
	out, err := run(applyIn(`{}`, strMember(fresh, "previewToken"), h3))
	if err != nil {
		t.Fatal(err)
	}
	after := workplanFile(t, root, "hist.json")
	if numMember(out.Value, "savings", "json", "before") != len(before) || numMember(out.Value, "savings", "json", "after") != len(after) {
		t.Errorf("savings %s, files %d -> %d", ojson.Compact(getPath(out.Value, "savings", "json")), len(before), len(after))
	}
	// The archive holds complete originals.
	arch, err := os.ReadFile(strMember(out.Value, "archivePath"))
	if err != nil {
		t.Fatal(err)
	}
	av := parseT(t, string(arch))
	if strMember(av, "source", "workplanJson") != string(before) {
		t.Error("archive source.workplanJson is not the original plan JSON")
	}
	sel := map[int]bool{}
	for _, r := range getPath(av, "removed", "noteIndexes").Elems() {
		i := numMember(r, "index")
		sel[i] = true
		if strMember(r, "text") != beforeNotes[i] {
			t.Errorf("archived note %d is not the complete original", i)
		}
	}
	if len(sel) != numMember(fresh, "noteRollover", "selectedCount") {
		t.Errorf("archived %d notes, previewed %d", len(sel), numMember(fresh, "noteRollover", "selectedCount"))
	}
	// Remaining notes: the kept ones in order, then the archive pointer.
	s, err := e.load("hist")
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for i, n := range beforeNotes {
		if !sel[i] {
			keep = append(keep, n)
		}
	}
	keep = append(keep, "Compaction archive: "+strings.TrimPrefix(strMember(out.Value, "archivePath"), root.Path+"/"))
	if strings.Join(s.Plan.Notes, "\x00") != strings.Join(keep, "\x00") {
		t.Errorf("notes after rollover: %d, want %d", len(s.Plan.Notes), len(keep))
	}
	if s.Plan.Notes[len(s.Plan.Notes)-2] != "late note" {
		t.Error("the note newer than the first checkpoint was not kept among the latest")
	}
}

// TestRolloverInput: the new input member on both surfaces.
func TestRolloverInput(t *testing.T) {
	cases := []struct{ in, msg string }{
		{`{"noteRollover":{},"noteIndexes":[1]}`, input.MsgRolloverWithIndexes},
		{`{"noteRollover":{"keepLatest":0}}`, "Too small: expected number to be >=1"},
		{`{"noteRollover":{"keepLatest":10001}}`, "Too big: expected number to be <=10000"},
		{`{"noteRollover":{"keepLatest":2.5}}`, "Invalid input: expected int, received number"},
		{`{"noteRollover":{"keepLatest":"20"}}`, "Invalid input: expected number, received string"},
		{`{"noteRollover":{"keep":20}}`, `Unrecognized key: "keep"`},
		{`{"noteRollover":{"pinNoteIndexes":[-1]}}`, "Too small: expected number to be >=0"},
		{`{"noteRollover":true}`, "Invalid input: expected object, received boolean"},
	}
	for _, tool := range []string{"compact", "compact_preview"} {
		for _, s := range []input.Surface{input.SurfaceCore, input.SurfaceNative} {
			for _, c := range cases {
				in := `{"id":"hist","archiveReason":"r",` + strings.TrimPrefix(c.in, "{")
				v, _ := ojson.Parse([]byte(in))
				_, err := input.ParseMutationInput(tool, v.Value, s)
				var ie *input.InputError
				if !errors.As(err, &ie) || !strings.Contains(err.Error(), c.msg) {
					t.Errorf("%s %v %s: %v", tool, s, c.in, err)
				}
			}
			v, _ := ojson.Parse([]byte(`{"id":"hist","archiveReason":"r","noteRollover":{"keepLatest":5,"pinNoteIndexes":[1,2]}}`))
			if _, err := input.ParseMutationInput(tool, v.Value, s); err != nil {
				t.Errorf("%s %v valid input refused: %v", tool, s, err)
			}
		}
	}
	e, _ := seedHistory(t, 30)
	for in, msg := range map[string]string{
		`{"pinNoteIndexes":[30]}`:  "noteRollover.pinNoteIndexes: Note index out of range: 30",
		`{"pinNoteIndexes":[1,1]}`: "noteRollover.pinNoteIndexes contains duplicate indexes",
	} {
		v, _ := ojson.Parse([]byte(`{"id":"hist","archiveReason":"r","noteRollover":` + in + `}`))
		if _, err := runMutation(context.Background(), e, "workplan_compact", v.Value, &countingAuth{}); err == nil || err.Error() != msg {
			t.Errorf("%s: %v", in, err)
		}
	}
	// Nothing older than the latest 20 is eligible here except plain notes:
	// a rollover that selects nothing cannot be applied.
	v, _ := ojson.Parse([]byte(fmt.Sprintf(`{"id":"hist","archiveReason":"r","noteRollover":{"keepLatest":30},"mode":"apply","previewToken":"x","confirmation":"ARCHIVE_SELECTED_HISTORY","expectedHash":%q}`, stateHashFor(t, e, "hist"))))
	if _, err := runMutation(context.Background(), e, "workplan_compact", v.Value, &countingAuth{}); err == nil || !strings.Contains(err.Error(), "Select at least one") {
		t.Errorf("empty rollover apply: %v", err)
	}
}
