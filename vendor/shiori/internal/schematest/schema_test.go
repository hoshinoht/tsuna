// Package schematest proves that the JSON Schemas in schema/ agree with
// the corpus. The validator is a test-only dependency and
// is never linked into the shipped binary.
package schematest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/lanes"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
	"github.com/hoshinoht/shiori/internal/testutil"
)

const idBase = "https://shiori.invalid/schema/v1/"

var uEscape = regexp.MustCompile(`\\u([0-9A-Fa-f]{4})`)

// ecmaRegexp compiles the schemas' ECMA-262 patterns with Go's RE2 after
// translating \uXXXX escapes (the only ECMA-specific syntax they use).
func ecmaRegexp(s string) (jsonschema.Regexp, error) {
	return regexp.Compile(uEscape.ReplaceAllString(s, `\x{$1}`))
}

func compiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseRegexpEngine(ecmaRegexp)
	root := filepath.Join(testutil.RepoRoot(), "schema", "v1")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		doc, err := jsonschema.UnmarshalJSON(f)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		return c.AddResource(idBase+filepath.ToSlash(rel), doc)
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func load(t *testing.T, path string) any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil // not JSON: rejected before schema validation
	}
	return v
}

// TestSchemasCompile compiles every schema (2020-12).
func TestSchemasCompile(t *testing.T) {
	c := compiler(t)
	root := filepath.Join(testutil.RepoRoot(), "schema", "v1")
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			rel, _ := filepath.Rel(root, p)
			if _, err := c.Compile(idBase + filepath.ToSlash(rel)); err != nil {
				t.Errorf("%s: %v", rel, err)
			}
		}
		return nil
	})
}

// TestStorageSchemasAgreeWithFixtures: valid primary plans and sidecars
// validate; the invalid-schema fixtures do not.
func TestStorageSchemasAgreeWithFixtures(t *testing.T) {
	c := compiler(t)
	schemas := map[string]*jsonschema.Schema{}
	for k, rel := range map[string]string{
		"plan": "plan-v2.schema.json", "checkpoint": "checkpoint.schema.json",
		"dependencies": "dependencies-v1.schema.json", "transaction": "transaction-journal-v1.schema.json",
	} {
		s, err := c.Compile(idBase + rel)
		if err != nil {
			t.Fatal(err)
		}
		schemas[k] = s
	}
	invalid := map[string]bool{
		"invalid-schema/not-json.json": true, "invalid-schema/not-object.json": true,
		"invalid-schema/wrong-types.json": true, "invalid-schema/wrong-version.json": true,
		"list-mixed/broken.json": true, "list-mixed/UPPER.json": true, "list-mixed/Bad Name.json": true,
		"checkpoint-corrupt/cp-bad.checkpoint.json": true, "checkpoint-corrupt/cp-bad.dependencies.json": true,
		"list-mixed/a-plan.checkpoint.json": true,
	}
	fixtures := testutil.Testdata("fixtures")
	checked := 0
	filepath.WalkDir(fixtures, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") || !strings.Contains(p, "/.opencode/workplan/") || strings.Contains(p, "/archive/") {
			return err
		}
		name := filepath.Base(p)
		if strings.HasPrefix(name, ".") {
			return nil
		}
		kind := "plan"
		for _, k := range []string{"checkpoint", "dependencies", "transaction"} {
			if strings.HasSuffix(name, "."+k+".json") {
				kind = k
			}
		}
		rel, _ := filepath.Rel(fixtures, p)
		key := strings.SplitN(rel, string(filepath.Separator), 2)[0] + "/" + name
		v := load(t, p)
		var verr error = os.ErrInvalid
		if v != nil {
			verr = schemas[kind].Validate(v)
		}
		checked++
		if invalid[key] && verr == nil {
			t.Errorf("%s: expected schema rejection", key)
		}
		if !invalid[key] && verr != nil {
			t.Errorf("%s: %v", key, verr)
		}
		return nil
	})
	if checked < 40 {
		t.Fatalf("only %d artifacts checked", checked)
	}
}

// TestToolSchemasAgreeWithNativeParser: the native tool schema accepts
// exactly the inputs the reference native parser accepted (all 55).
func TestToolSchemasAgreeWithNativeParser(t *testing.T) {
	c := compiler(t)
	files, _ := filepath.Glob(testutil.Testdata("vectors", "validation", "input", "*.json"))
	for _, f := range files {
		var v struct {
			ID     string          `json:"id"`
			Tool   string          `json:"tool"`
			Input  json.RawMessage `json:"input"`
			Expect struct {
				Native struct {
					OK bool `json:"ok"`
				} `json:"native"`
			} `json:"expect"`
		}
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		s, err := c.Compile(idBase + "tools/" + v.Tool + ".input.schema.json")
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(v.Input)))
		if err != nil {
			t.Fatal(err)
		}
		verr := s.Validate(inst)
		if (verr == nil) != v.Expect.Native.OK {
			t.Errorf("%s: schema ok=%v, native ok=%v (%v)", v.ID, verr == nil, v.Expect.Native.OK, verr)
		}
	}
}

// TestCheckpointMergeSchemaAgreesWithParser:
// the native checkpoint schema and the Go parser accept the same merge and
// appendValidation shapes (the withheld-placeholder refusal is an
// x-shiori-rules refinement, like the other cross-field rules).
func TestCheckpointMergeSchemaAgreesWithParser(t *testing.T) {
	c := compiler(t)
	s, err := c.Compile(idBase + "tools/workplan_checkpoint.input.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	h := `"expectedHash":"` + strings.Repeat("a", 64) + `"`
	cases := map[string]bool{
		`{"id":"p","merge":true,` + h + `}`:                                          true,
		`{"id":"p","merge":true,"appendValidation":"v",` + h + `}`:                   true,
		`{"id":"p","merge":true,"appendValidation":["v","w"],` + h + `}`:             true,
		`{"id":"p","summary":"s","nextAction":"n","appendValidation":"v",` + h + `}`: true,
		`{"id":"p","summary":"s","nextAction":"n","merge":false,` + h + `}`:          true,
		`{"id":"p",` + h + `}`:                                                                   false,
		`{"id":"p","merge":false,` + h + `}`:                                                     false,
		`{"id":"p","merge":true,"summary":"s",` + h + `}`:                                        true,
		`{"id":"p","merge":"yes","summary":"s","nextAction":"n",` + h + `}`:                      false,
		`{"id":"p","merge":true,"appendValidation":3,` + h + `}`:                                 false,
		`{"id":"p","merge":true,"appendValidation":["v",3],` + h + `}`:                           false,
		`{"id":"p","merge":true}`:                                                                false,
		`{"id":"p","summary":"s","nextAction":"n",` + h + `}`:                                    true,
		`{"id":"p","summary":"s","nextAction":"n","merge":true,"appendValidation":[],` + h + `}`: true,
	}
	for in, want := range cases {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Validate(inst) == nil; got != want {
			t.Errorf("schema %s: ok=%v want %v", in, got, want)
		}
		v, err := ojson.Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := input.ParseMutationInput("checkpoint", v.Value, input.SurfaceNative); (err == nil) != want {
			t.Errorf("parser %s: ok=%v want %v (%v)", in, err == nil, want, err)
		}
	}
}

// TestRecordEvidenceSchemaAgreesWithParser: the native update schema and
// the Go parser accept the same recordEvidence shapes (spec 06 X2).
func TestRecordEvidenceSchemaAgreesWithParser(t *testing.T) {
	c := compiler(t)
	s, err := c.Compile(idBase + "tools/workplan_update.input.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	h := `"expectedHash":"` + strings.Repeat("a", 64) + `"`
	rec := func(extra string) string {
		return `{"id":"p",` + h + `,"recordEvidence":[{"phaseId":"a","stepId":"b","command":"go test ./...","exitCode":0` + extra + `}]}`
	}
	cases := map[string]bool{
		rec(``):               true,
		rec(`,"output":"ok"`): true,
		rec(`,"outputDigest":"` + strings.Repeat("b", 64) + `"`):                                            true,
		rec(`,"output":"ok","outputDigest":"` + strings.Repeat("b", 64) + `"`):                              false,
		rec(`,"outputDigest":"xyz"`):                                                                        false,
		rec(`,"summary":"13 packages ok","scope":["internal/engine","go.mod"]`):                             true,
		rec(`,"summary":"` + strings.Repeat("s", 501) + `"`):                                                false,
		rec(`,"scope":"internal"`):                                                                          false,
		rec(`,"exitCode2":1`):                                                                               false,
		rec(`,"recordedAt":"2026-01-01T00:00:00Z"`):                                                         false,
		`{"id":"p",` + h + `,"recordEvidence":[{"phaseId":"a","stepId":"b","command":"x","exitCode":1.5}]}`: false,
		`{"id":"p",` + h + `,"recordEvidence":[{"phaseId":"a","stepId":"b","command":" ","exitCode":0}]}`:   false,
		`{"id":"p",` + h + `,"recordEvidence":[{"phaseId":"a","stepId":"b","exitCode":0}]}`:                 false,
		`{"id":"p",` + h + `,"recordEvidence":[{"phaseId":"a","stepId":"b","command":"x","exitCode":-1}]}`:  true,
		`{"id":"p",` + h + `,"recordEvidence":[]}`:                                                          true,
		`{"id":"p",` + h + `,"recovery":"resume","recordEvidence":[]}`:                                      false,
		`{"id":"p",` + h + `,"recordEvidence":[` + strings.Repeat(`{"phaseId":"a","stepId":"b","command":"x","exitCode":0},`, 20) + `{"phaseId":"a","stepId":"b","command":"x","exitCode":0}]}`: false,
	}
	for in, want := range cases {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Validate(inst) == nil; got != want {
			t.Errorf("schema %s: ok=%v want %v", in, got, want)
		}
		v, err := ojson.Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := input.ParseMutationInput("update", v.Value, input.SurfaceNative); (err == nil) != want {
			t.Errorf("parser %s: ok=%v want %v (%v)", in, err == nil, want, err)
		}
	}
}

// TestEvidenceSchemaAgreesWithDecoder: the ledger the Go writer encodes
// validates, and shape errors are rejected by both.
func TestEvidenceSchemaAgreesWithDecoder(t *testing.T) {
	c := compiler(t)
	s, err := c.Compile(idBase + "evidence-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	d := strings.Repeat("d", 64)
	l := &evidence.Ledger{ID: "p", UpdatedAt: "2026-01-02T03:04:05Z", Records: []evidence.Record{
		{PhaseID: "a", StepID: "b", Command: "go test", ExitCode: 1, OutputDigest: &d, TreeOID: nil,
			Scope: []evidence.ScopeEntry{{Path: "src", Digest: &d}, {Path: "x"}}, Source: evidence.SourceCLI, RecordedAt: "2026-01-02T03:04:05Z"},
	}}
	good := string(evidence.Encode(l))
	cases := map[string]bool{
		good: true,
		strings.Replace(good, `"source": "cli"`, `"source": "model"`, 1):     false,
		strings.Replace(good, `"exitCode": 1`, `"exitCode": 1.5`, 1):         false,
		strings.Replace(good, `"treeOid": null`, `"treeOid": "abc"`, 1):      false,
		strings.Replace(good, `"schemaVersion": 1`, `"schemaVersion": 2`, 1): false,
	}
	for in, want := range cases {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Validate(inst) == nil; got != want {
			t.Errorf("schema ok=%v want %v:\n%s", got, want, in)
		}
		v, _ := ojson.Parse([]byte(in))
		if _, issues := evidence.Decode(v.Value); (len(issues) == 0) != want {
			t.Errorf("decoder ok=%v want %v (%v)", len(issues) == 0, want, issues)
		}
	}
}

// TestJournalV2SchemaMatchesEncoder: the v2 journal the writer encodes
// validates against its schema and decodes.
func TestJournalV2SchemaMatchesEncoder(t *testing.T) {
	c := compiler(t)
	s, err := c.Compile(idBase + "transaction-journal-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	in := &storage.Intent{JournalVersion: 2, TransactionID: "0f1e2d3c-aaaa-bbbb-cccc-000000000000", WorkplanID: "p", Operation: "update", CreatedAt: "2026-01-02T03:04:05Z",
		Targets: []storage.Target{
			{Rel: ".opencode/workplan/p.json", Before: []byte("a"), BeforeExists: true, After: []byte("b"), AfterExists: true, Mode: 0o644, Stage: "s", Backup: "b"},
			{Rel: ".opencode/workplan/p.dependencies.json", Before: []byte("c"), BeforeExists: true, Mode: 0o600, Backup: "b2"},
		}}
	data := storage.EncodeJournal(in)
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(inst); err != nil {
		t.Fatal(err)
	}
	v, _ := ojson.Parse(data)
	j, ok := model.DecodeJournal(v.Value)
	if !ok || j.Version != 2 || j.Targets[1].AfterHash != nil || j.Targets[1].Backup != "b2" {
		t.Fatalf("decode: %v %+v", ok, j)
	}
}

// TestLanesSchemaAgreesWithParser: lanes operations accepted by the
// native schema and the Go parser agree, and the stored sidecar the writer
// encodes validates.
func TestLanesSchemaAgreesWithParser(t *testing.T) {
	c := compiler(t)
	s, err := c.Compile(idBase + "tools/workplan_update.input.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	h := `"expectedHash":"` + strings.Repeat("a", 64) + `"`
	op := func(o string) string { return `{"id":"p",` + h + `,"lanes":[` + o + `]}` }
	cases := map[string]bool{
		op(`{"op":"propose","laneId":"a","steps":[{"phaseId":"p","stepId":"s"}],"claims":["src"]}`):                  true,
		op(`{"op":"propose","laneId":"a","steps":[]}`):                                                               false,
		op(`{"op":"propose","laneId":"a","claims":["src"]}`):                                                         false,
		op(`{"op":"propose","laneId":"a","steps":[{"phaseId":"p","stepId":"s"}],"state":"running"}`):                 false,
		op(`{"op":"transition","laneId":"a","state":"prepared","checkout":{"path":"../wt","branch":"b"}}`):           true,
		op(`{"op":"transition","laneId":"a","state":"done"}`):                                                        false,
		op(`{"op":"transition","laneId":"a"}`):                                                                       false,
		op(`{"op":"transition","laneId":"a","state":"prepared","checkout":{"branch":"b"}}`):                          false,
		op(`{"op":"claims","laneId":"a","add":["x"],"remove":["y"]}`):                                                true,
		op(`{"op":"claims","laneId":"a"}`):                                                                           false,
		op(`{"op":"merge","laneId":"a"}`):                                                                            false,
		op(`{"op":"claims","laneId":" ","add":["x"]}`):                                                               false,
		`{"id":"p",` + h + `,"recordEvidence":[{"phaseId":"a","stepId":"b","command":"x","exitCode":0,"lane":"l"}]}`: true,
	}
	for in, want := range cases {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Validate(inst) == nil; got != want {
			t.Errorf("schema %s: ok=%v want %v", in, got, want)
		}
		v, _ := ojson.Parse([]byte(in))
		if _, err := input.ParseMutationInput("update", v.Value, input.SurfaceNative); (err == nil) != want {
			t.Errorf("parser %s: ok=%v want %v (%v)", in, err == nil, want, err)
		}
	}
	ls, err := c.Compile(idBase + "lanes-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.Repeat("b", 40)
	br := "lane/a"
	l := &lanes.Ledger{ID: "p", UpdatedAt: "2026-01-02T03:04:05Z", Lanes: []lanes.Lane{{ID: "a", State: lanes.Prepared,
		Steps: []model.StepRef{{PhaseID: "p", StepID: "s"}}, Claims: []string{"src"}, Baseline: lanes.Baseline{TreeOID: &oid, Head: &oid},
		Checkout: &lanes.Checkout{Path: "/w/a", Branch: &br}, History: []lanes.Event{{State: lanes.Claimed, At: "2026-01-02T03:04:05Z", Source: "agent"}},
		CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z"}}}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(lanes.Encode(l))))
	if err != nil {
		t.Fatal(err)
	}
	if err := ls.Validate(inst); err != nil {
		t.Fatal(err)
	}
}

// TestHistorySchemaMatchesWriter: lines the engine appends validate, and
// the decoder rejects what the schema rejects.
func TestHistorySchemaMatchesWriter(t *testing.T) {
	c := compiler(t)
	s, err := c.Compile(idBase + "history-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	h := strings.Repeat("a", 64)
	for i, en := range []history.Entry{
		{At: "2026-01-02T03:04:05Z", Op: "create", Source: history.SourceAgent, Tx: "t1", After: &history.Hashes{PlanHash: h, StateHash: h},
			Changes: []history.Change{{Path: "plan", Op: "added"}}},
		{At: "2026-01-02T03:04:06Z", Op: "update", Source: history.SourceMCP, Tx: "t2", Before: &history.Hashes{PlanHash: h, StateHash: h}, After: &history.Hashes{PlanHash: h, StateHash: h},
			Changes: []history.Change{{Path: "notes", Op: "appended", Count: 2}, {Path: "phases/p/steps/s", Op: "changed", From: "draft", To: "completed"}}},
	} {
		in := &storage.Intent{Root: root, WorkplanID: "p", TransactionID: en.Tx, Operation: en.Op, CreatedAt: en.At,
			JournalRel: "p.transaction.json", JournalStage: ".p.transaction." + en.Tx + ".stage",
			History: &storage.Append{Rel: "p" + history.Suffix, Payload: history.Payload(en)}}
		if _, err := storage.Commit(context.Background(), in, storage.Hooks{}); err != nil {
			t.Fatal(i, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "p"+history.Suffix))
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	bad := []string{
		strings.Replace(lines[1], `"source":"mcp"`, `"source":"model"`, 1),
		strings.Replace(lines[1], `"op":"appended"`, `"op":"moved"`, 1),
		strings.Replace(lines[1], `"v":1`, `"v":2`, 1),
	}
	for _, in := range append(lines, bad...) {
		want := !slices.Contains(bad, in)
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Validate(inst) == nil; got != want {
			t.Errorf("schema ok=%v want %v:\n%s", got, want, in)
		}
	}
	if l := history.Parse(data); len(l.Entries) != 2 || len(l.Issues) != 0 {
		t.Fatalf("decoded %+v", l)
	}
}
