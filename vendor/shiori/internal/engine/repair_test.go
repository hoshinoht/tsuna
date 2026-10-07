package engine

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// TestUnreadablePlanRepair: a truncated plan gets raw-byte
// hashes from doctor/validate/list, and create overwrite with that hash
// repairs it (keeping the Markdown unless replaceMarkdown=true).
func TestUnreadablePlanRepair(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	jp := filepath.Join(root.Path, ".opencode/workplan/minimal.json")
	md := filepath.Join(root.Path, ".opencode/workplan/minimal.md")
	data, _ := os.ReadFile(jp)
	os.WriteFile(jp, data[:len(data)/2], 0o644) // truncated
	mdBefore, _ := os.ReadFile(md)

	doc, _ := e.Doctor(input.DoctorInput{})
	plans, _ := doc.Get("plans")
	entry := plans.Elems()[0]
	sh := strMember(entry, "stateHash")
	ph := strMember(entry, "planHash")
	if len(sh) != 64 || len(ph) != 64 {
		t.Fatalf("doctor gives no raw hashes: %s", ojson.Compact(entry))
	}
	v, _ := e.Validate(input.ValidateInput{ID: "minimal"})
	if strMember(v, "stateHash") != sh || strMember(v, "planHash") != ph {
		t.Fatalf("validate hashes differ: %s", ojson.Compact(v))
	}
	l, _ := e.List(input.ListInput{})
	ws, _ := l.Get("workplans")
	le := ws.Elems()[0]
	if strMember(le, "stateHash") != sh || len(strs(func() ojson.Value { x, _ := le.Get("issues"); return x }())) != 1 {
		t.Fatalf("list entry: %s", ojson.Compact(le))
	}
	if _, ok := le.Get("issue"); ok {
		t.Fatal("list entry still carries the single issue string")
	}
	// A stale hash is refused; reads stayed read-only.
	if _, err, n := mutateErr(t, e, "workplan_create", `{"id":"minimal","goal":"g","overwrite":true,"expectedHash":"`+strings.Repeat("0", 64)+`"}`); err == nil || !strings.Contains(err.Error(), "current stateHash is "+sh) || n != 0 {
		t.Fatalf("stale overwrite: %v", err)
	}
	out, err, n := mutateErr(t, e, "workplan_create", `{"id":"minimal","goal":"Repaired goal","overwrite":true,"expectedHash":"`+sh+`"}`)
	if err != nil || n != 1 {
		t.Fatalf("repair: %v (authorizations %d)", err, n)
	}
	if v, _ := out.Value.Get("overwritten"); !v.Bool() {
		t.Fatal("not reported as overwritten")
	}
	if got, _ := os.ReadFile(md); string(got) != string(mdBefore) {
		t.Fatal("existing Markdown replaced without replaceMarkdown")
	}
	if p := planFile(t, root.Path, "minimal"); p.Goal != "Repaired goal" {
		t.Fatalf("goal %q", p.Goal)
	}
	// With replaceMarkdown the Markdown is regenerated.
	os.WriteFile(jp, []byte("{"), 0o644)
	sh = stateHashFor(t, e, "minimal")
	mustMutate(t, e, "workplan_create", `{"id":"minimal","goal":"Again","overwrite":true,"replaceMarkdown":true,"expectedHash":"`+sh+`"}`)
	s, _ := snapshot.Load(root.Path, "minimal", snapshot.DefaultLimits)
	if gen, _ := e.generatedMarkdown(s); !gen {
		t.Fatal("replaceMarkdown did not regenerate")
	}
}

// planWithLink writes an unreadable plan whose raw bytes still carry a
// planFile member, linked Markdown at that path, and returns the paths.
func planWithLink(t *testing.T, root string, raw string) (string, string) {
	t.Helper()
	jp := filepath.Join(root, ".opencode/workplan/minimal.json")
	md := filepath.Join(root, ".opencode/workplan/docs/minimal-notes.md")
	os.MkdirAll(filepath.Dir(md), 0o755)
	os.WriteFile(md, []byte("# Handwritten notes\n"), 0o644)
	os.WriteFile(jp, []byte(raw), 0o644)
	return jp, md
}

// TestRepairRecoveredLink: doctor recovers the planFile from
// the damaged bytes, create overwrite keeps using it and archives the
// exact damaged bytes first in the same transaction, an explicit planFile
// wins, and writers name the repair.
func TestRepairRecoveredLink(t *testing.T) {
	root := testutil.NewRoot(t, "minimal-valid")
	e := mustEngine(t, root.Path)
	raw := `{"schemaVersion":2,"id":"minimal","kind":"general","goal":"g","planFile":".opencode/workplan/docs/minimal-notes.md","phases":[{"id":"p","title":"P","ste`
	jp, md := planWithLink(t, root.Path, raw)
	doc, _ := e.Doctor(input.DoctorInput{})
	plans, _ := doc.Get("plans")
	entry := plans.Elems()[0]
	if got := strMember(entry, "recoveredPlanFile"); got != ".opencode/workplan/docs/minimal-notes.md" {
		t.Fatalf("recoveredPlanFile %q in %s", got, ojson.Compact(entry))
	}
	sh := strMember(entry, "stateHash")

	// (c) writers name the repair and the doctor hash.
	for _, c := range []struct{ tool, in string }{
		{"workplan_update", `{"id":"minimal","appendNotes":["x"]}`},
		{"workplan_reset", `{"id":"minimal"}`},
		{"workplan_checkpoint", `{"id":"minimal","summary":"s","nextAction":"n"}`},
	} {
		_, err, n := mutateErr(t, e, c.tool, c.in)
		var ue *UnreadablePlanError
		if !errors.As(err, &ue) || n != 0 || !strings.Contains(err.Error(), "workplan_create overwrite=true and expectedHash="+sh) {
			t.Fatalf("%s: %v", c.tool, err)
		}
		if ErrorClass(err) != "invalid_structure" {
			t.Fatalf("%s class %s", c.tool, ErrorClass(err))
		}
	}

	// (a)+(b) the repair keeps the recovered link and archives the bytes.
	out := mustMutate(t, e, "workplan_create", `{"id":"minimal","goal":"Repaired","overwrite":true,"expectedHash":"`+sh+`"}`)
	if pp := strMember(out.Value, "planPath"); pp != e.absRel(".opencode/workplan/docs/minimal-notes.md") {
		t.Fatalf("planPath %s", pp)
	}
	if got, _ := os.ReadFile(md); string(got) != "# Handwritten notes\n" {
		t.Fatal("handwritten Markdown at the recovered link was replaced")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".opencode/workplan/minimal.md")); err != nil {
		t.Fatal("the old default Markdown must stay untouched")
	}
	ap := strMember(out.Value, "archivePath")
	if !regexp.MustCompile(`/\.opencode/workplan/archive/minimal/state-` + sh[:12] + `-[0-9a-f]{12}\.json$`).MatchString(ap) {
		t.Fatalf("archivePath %q", ap)
	}
	st, err := os.Stat(ap)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("archive %v mode %v", err, st)
	}
	var arc struct {
		ArchiveVersion int    `json:"archiveVersion"`
		WorkplanID     string `json:"workplanId"`
		Operation      string `json:"operation"`
		StateHash      string `json:"stateHash"`
		Source         struct {
			WorkplanJSON       *string `json:"workplanJson"`
			WorkplanJSONSha256 string  `json:"workplanJsonSha256"`
			LinkedMarkdownPath string  `json:"linkedMarkdownPath"`
			LinkedMarkdown     *string `json:"linkedMarkdown"`
		} `json:"source"`
	}
	data, _ := os.ReadFile(ap)
	if err := json.Unmarshal(data, &arc); err != nil {
		t.Fatal(err)
	}
	if arc.ArchiveVersion != 1 || arc.WorkplanID != "minimal" || arc.Operation != "create:overwrite" || arc.StateHash != sh ||
		arc.Source.WorkplanJSON == nil || *arc.Source.WorkplanJSON != raw || arc.Source.WorkplanJSONSha256 != testutil.SHA256Hex(raw) ||
		arc.Source.LinkedMarkdownPath != ".opencode/workplan/docs/minimal-notes.md" || arc.Source.LinkedMarkdown == nil {
		t.Fatalf("archive %s", data)
	}
	if p := planFile(t, root.Path, "minimal"); p.PlanFile != ".opencode/workplan/docs/minimal-notes.md" || p.Goal != "Repaired" {
		t.Fatalf("repaired plan %+v", p)
	}

	// Invalid UTF-8 bytes are archived exactly as base64; an explicit
	// planFile wins over the recovered one.
	bad := append([]byte(`{"planFile":".opencode/workplan/docs/minimal-notes.md",`), 0xff, 0xfe)
	os.WriteFile(jp, bad, 0o644)
	sh = stateHashFor(t, e, "minimal")
	out = mustMutate(t, e, "workplan_create", `{"id":"minimal","goal":"Again","overwrite":true,"planFile":".opencode/workplan/minimal.md","replaceMarkdown":true,"expectedHash":"`+sh+`"}`)
	if pp := strMember(out.Value, "planPath"); pp != e.absRel(".opencode/workplan/minimal.md") {
		t.Fatalf("explicit planFile lost: %s", pp)
	}
	var arc2 struct {
		Source struct {
			WorkplanJSON       *string `json:"workplanJson"`
			WorkplanJSONBase64 string  `json:"workplanJsonBase64"`
		} `json:"source"`
	}
	data, _ = os.ReadFile(strMember(out.Value, "archivePath"))
	json.Unmarshal(data, &arc2)
	dec, _ := base64.StdEncoding.DecodeString(arc2.Source.WorkplanJSONBase64)
	if arc2.Source.WorkplanJSON != nil || string(dec) != string(bad) {
		t.Fatalf("non-UTF-8 archive %s", data)
	}

	// A link another readable plan owns is not recovered.
	mustMutate(t, e, "workplan_create", `{"id":"other","goal":"g","planFile":".opencode/workplan/docs/other.md"}`)
	if pf := e.recoverPlanFile("minimal", []byte(`{"planFile":".opencode/workplan/docs/other.md",`)); pf != "" {
		t.Fatalf("recovered another plan's link %q", pf)
	}
	// No or unsafe planFile members: nothing is recovered.
	for _, raw := range []string{`{"id":"minimal"`, `{"planFile":"../outside.md",`, `{"planFile":"a.md","x":{"planFile":"b.md"}`, `{"planFile":"notes.txt",`} {
		os.WriteFile(jp, []byte(raw), 0o644)
		if pf := e.recoverPlanFile("minimal", []byte(raw)); pf != "" {
			t.Fatalf("%s: recovered %q", raw, pf)
		}
		doc, _ := e.Doctor(input.DoctorInput{})
		plans, _ := doc.Get("plans")
		if testutil.Has(plans.Elems()[0], "recoveredPlanFile") {
			t.Fatalf("%s: doctor reports a recovered planFile", raw)
		}
	}
}
