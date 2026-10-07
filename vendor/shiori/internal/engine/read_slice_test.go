package engine

import (
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// TestFilteredRead is item B.
func TestFilteredRead(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e := mustEngine(t, root.Path)
	before := testutil.Fingerprint(t, root.Path)
	full, err := e.Read(input.ReadInput{ID: "full-plan"})
	if err != nil {
		t.Fatal(err)
	}
	tr := true
	fullNotes, _ := e.Read(input.ReadInput{ID: "full-plan", IncludeNotes: &tr})
	if string(ojson.Pretty(full)) != string(ojson.Pretty(fullNotes)) {
		t.Fatal("includeNotes must not change an unfiltered read")
	}
	wp, _ := full.Get("workplan")
	phases, _ := wp.Get("phases")
	ph0, _ := phases.Elems()[0].Get("id")
	pid := ph0.Str()
	slice, err := e.Read(input.ReadInput{ID: "full-plan", PhaseID: &pid})
	if err != nil {
		t.Fatal(err)
	}
	sw, _ := slice.Get("workplan")
	for _, k := range []string{"phases", "reviewFindings", "notes"} {
		if _, ok := sw.Get(k); ok {
			t.Errorf("slice workplan has %s", k)
		}
	}
	for _, k := range []string{"id", "goal", "planFile", "status", "scope"} {
		if _, ok := sw.Get(k); !ok {
			t.Errorf("slice workplan lacks %s", k)
		}
	}
	pl, _ := slice.Get("plan")
	if _, ok := pl.Get("content"); ok {
		t.Error("filtered read must omit Markdown by default")
	}
	if len(ojson.Pretty(slice)) >= len(ojson.Pretty(full)) {
		t.Errorf("slice (%d) not smaller than full read (%d)", len(ojson.Pretty(slice)), len(ojson.Pretty(full)))
	}
	withNotes, _ := e.Read(input.ReadInput{ID: "full-plan", PhaseID: &pid, IncludeNotes: &tr})
	nw, _ := withNotes.Get("workplan")
	for _, k := range []string{"reviewFindings", "notes"} {
		got, ok := nw.Get(k)
		want, _ := wp.Get(k)
		if !ok || string(ojson.Compact(got)) != string(ojson.Compact(want)) {
			t.Errorf("includeNotes: %s missing or different", k)
		}
	}
	withMD, _ := e.Read(input.ReadInput{ID: "full-plan", PhaseID: &pid, IncludeMarkdown: &tr})
	mp, _ := withMD.Get("plan")
	if _, ok := mp.Get("content"); !ok {
		t.Error("explicit includeMarkdown=true must include Markdown")
	}
	for _, v := range []ojson.Value{slice, withNotes, withMD} {
		for _, k := range []string{"planHash", "stateHash", "selection", "path", "dependencies", "slice"} {
			if _, ok := v.Get(k); !ok {
				t.Errorf("slice lacks %s", k)
			}
		}
	}
	if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
		t.Fatalf("read wrote: %v", d)
	}
	// Input contract: both surfaces accept the boolean, reject others.
	for _, s := range []input.Surface{input.SurfaceCore, input.SurfaceNative} {
		p, _ := ojson.Parse([]byte(`{"id":"full-plan","phaseId":"x","includeNotes":true}`))
		in, err := input.ParseReadInput(p.Value, s)
		if err != nil || in.IncludeNotes == nil || !*in.IncludeNotes {
			t.Fatalf("includeNotes not accepted: %v", err)
		}
		p, _ = ojson.Parse([]byte(`{"id":"full-plan","includeNotes":"yes"}`))
		if _, err := input.ParseReadInput(p.Value, s); err == nil || err.Error() != "Invalid read input: includeNotes: Invalid input: expected boolean, received string" {
			t.Fatalf("bad includeNotes: %v", err)
		}
	}
}

// TestFullReadWithoutNotes: includeNotes=false leaves the notes out of an
// unfiltered read and says how many; the rest of the document is intact.
func TestFullReadWithoutNotes(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	full, err := e.Read(input.ReadInput{ID: "full-plan"})
	if err != nil {
		t.Fatal(err)
	}
	f := false
	slim, err := e.Read(input.ReadInput{ID: "full-plan", IncludeNotes: &f})
	if err != nil {
		t.Fatal(err)
	}
	wp, _ := slim.Get("workplan")
	if _, ok := wp.Get("notes"); ok {
		t.Fatal("notes still present")
	}
	fw, _ := full.Get("workplan")
	notes, _ := fw.Get("notes")
	om, _ := slim.Get("notesOmitted")
	if c, _ := om.Get("count"); c.NumberLiteral() != itoaT(len(notes.Elems())) {
		t.Fatalf("notesOmitted %s", ojson.Compact(om))
	}
	if string(ojson.Compact(withoutMember(fw, "notes"))) != string(ojson.Compact(wp)) {
		t.Fatal("the rest of the document changed")
	}
	tr := true
	again, _ := e.Read(input.ReadInput{ID: "full-plan", IncludeNotes: &tr})
	if string(ojson.Compact(again)) != string(ojson.Compact(full)) {
		t.Fatal("includeNotes=true differs from the default")
	}
}
