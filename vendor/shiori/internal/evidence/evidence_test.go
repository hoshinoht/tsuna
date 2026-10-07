package evidence

import (
	"strconv"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/gitview"
	"github.com/hoshinoht/shiori/internal/ojson"
)

func sp(s string) *string { return &s }

func TestEncodeDecodeRoundTrip(t *testing.T) {
	l := &Ledger{ID: "demo", UpdatedAt: "2026-01-02T03:04:05Z", Records: []Record{
		{PhaseID: "p", StepID: "s", Command: "go test", ExitCode: 0, OutputDigest: sp(strings.Repeat("a", 64)), Summary: sp("ok"),
			TreeOID: sp(strings.Repeat("b", 40)), Scope: []ScopeEntry{{Path: "src", Digest: sp(strings.Repeat("c", 64))}, {Path: "gone"}},
			Source: SourceCLIRun, RecordedAt: "2026-01-02T03:04:05Z"},
		{PhaseID: "p", StepID: "s", Command: "lint", ExitCode: -1, Source: SourceAgent, RecordedAt: "2026-01-02T03:04:05Z"},
	}}
	data := Encode(l)
	p, err := ojson.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	got, issues := Decode(p.Value)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	if string(Encode(got)) != string(data) {
		t.Fatalf("round trip differs:\n%s\n%s", Encode(got), data)
	}
}

func TestDecodeIssues(t *testing.T) {
	cases := map[string]string{
		`[]`: ": Invalid input: expected object",
		`{"schemaVersion":2,"id":"d","updatedAt":"2026-01-02T03:04:05Z","records":[]}`:                "schemaVersion: Invalid input: expected 1",
		`{"schemaVersion":1,"id":"d","updatedAt":"x","records":[]}`:                                   "updatedAt: Invalid ISO datetime",
		`{"schemaVersion":1,"id":"d","updatedAt":"2026-01-02T03:04:05Z","records":[{"phaseId":"p"}]}`: "records.0.stepId: Invalid input: expected string, received undefined",
		`{"schemaVersion":1,"id":"d","updatedAt":"2026-01-02T03:04:05Z","records":[{"phaseId":"p","stepId":"s","command":"c","exitCode":0,"outputDigest":null,"treeOid":"zz","source":"agent","recordedAt":"2026-01-02T03:04:05Z"}]}`: "records.0.treeOid",
		`{"schemaVersion":1,"id":"d","updatedAt":"2026-01-02T03:04:05Z","records":[{"phaseId":"p","stepId":"s","command":"c","exitCode":0,"outputDigest":null,"treeOid":null,"source":"model","recordedAt":"2026-01-02T03:04:05Z"}]}`: "records.0.source: Invalid option",
	}
	for in, want := range cases {
		p, err := ojson.Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		_, issues := Decode(p.Value)
		if !strings.Contains(strings.Join(issues, "; "), want) {
			t.Errorf("%s: issues %v, want %q", in, issues, want)
		}
	}
}

func TestAppendRetention(t *testing.T) {
	l := &Ledger{ID: "d"}
	for i := 0; i < KeepPerCommand+3; i++ {
		l.Append(Record{PhaseID: "p", StepID: "s", Command: "a", ExitCode: int64(i)}, Record{PhaseID: "p", StepID: "s", Command: "b"})
	}
	var a []int64
	for _, r := range l.Records {
		if r.Command == "a" {
			a = append(a, r.ExitCode)
		}
	}
	if len(a) != KeepPerCommand || a[0] != 3 || a[len(a)-1] != KeepPerCommand+2 {
		t.Fatalf("kept %v", a)
	}
	big := &Ledger{ID: "d"}
	for i := 0; i < KeepTotal+10; i++ {
		big.Append(Record{PhaseID: "p", StepID: "s" + strconv.Itoa(i), Command: "a"})
	}
	if len(big.Records) != KeepTotal || big.Records[0].StepID != "s10" {
		t.Fatalf("total %d first %s", len(big.Records), big.Records[0].StepID)
	}
}

func TestViews(t *testing.T) {
	tree := sp(strings.Repeat("1", 40))
	other := sp(strings.Repeat("2", 40))
	d1, d2 := sp(strings.Repeat("d", 64)), sp(strings.Repeat("e", 64))
	cur := Current{Tree: &gitview.Tree{OID: *tree, Scope: map[string]*string{"src": d1, "gone": nil}}}
	l := &Ledger{Records: []Record{
		{PhaseID: "p", StepID: "fresh", Command: "a", TreeOID: tree},
		{PhaseID: "p", StepID: "stale", Command: "a", TreeOID: other},
		{PhaseID: "p", StepID: "scoped", Command: "a", TreeOID: other, Scope: []ScopeEntry{{Path: "src", Digest: d1}, {Path: "gone"}}},
		{PhaseID: "p", StepID: "scopedStale", Command: "a", TreeOID: tree, Scope: []ScopeEntry{{Path: "src", Digest: d2}}},
		{PhaseID: "p", StepID: "mixed", Command: "a", TreeOID: tree},
		{PhaseID: "p", StepID: "mixed", Command: "b", TreeOID: tree, ExitCode: 1},
		{PhaseID: "p", StepID: "fixed", Command: "a", TreeOID: tree, ExitCode: 1},
		{PhaseID: "p", StepID: "fixed", Command: "a", TreeOID: tree},
		{PhaseID: "p", StepID: "untracked", Command: "a"},
	}}
	want := map[string]string{"fresh": StateFresh, "stale": StateStale, "scoped": StateFresh, "scopedStale": StateStale,
		"mixed": StateFailing, "fixed": StateFresh, "untracked": StateUnknown}
	views := l.Views(cur)
	for _, v := range views {
		if v.State != want[v.Ref.StepID] {
			t.Errorf("%s: %s want %s", v.Ref.StepID, v.State, want[v.Ref.StepID])
		}
	}
	if v := views[l.Records[7].Ref()]; len(v.Commands) != 1 || v.Commands[0].Count != 2 {
		t.Fatalf("fixed: %+v", v)
	}
	if got := (Current{}).RecordState(l.Records[0]); got != StateUnknown {
		t.Fatalf("no current tree: %s", got)
	}
}
