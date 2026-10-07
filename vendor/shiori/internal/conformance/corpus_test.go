package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hoshinoht/shiori/internal/history"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// vector is the read-tool corpus vector shape.
type vector struct {
	ID             string `json:"id"`
	Fixture        string `json:"fixture"`
	GenerationRoot string `json:"generationRoot"`
	Call           struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	} `json:"call"`
	Expect struct {
		Kind            string          `json:"kind"`
		Message         string          `json:"message"`
		RawOutputLength int             `json:"rawOutputLength"`
		OutputSha256    string          `json:"outputSha256"`
		Output          json.RawMessage `json:"output"`
		OutputText      *string         `json:"outputText"`
	} `json:"expect"`
	ReadOnly *struct {
		Unchanged bool `json:"bytesAndMtimesUnchanged"`
	} `json:"readOnly"`
	// caseAdapted marks an oracle rewritten for a case-sensitive
	// filesystem (adaptCaseLookup).
	caseAdapted bool
}

// mutationVector is the mutation corpus vector shape.
type mutationVector struct {
	ID      string `json:"id"`
	Fixture string `json:"fixture"`
	Call    struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	} `json:"call"`
	Expect struct {
		Kind         string          `json:"kind"`
		Message      string          `json:"message"`
		OutputSha256 string          `json:"outputSha256"`
		Output       json.RawMessage `json:"output"`
		OutputText   *string         `json:"outputText"`
		Metadata     json.RawMessage `json:"metadata"`
	} `json:"expect"`
	ChangedFiles map[string]changedFile `json:"changedFiles"`
	Prompts      []json.RawMessage      `json:"permissionPrompts"`
}

type changedFile struct {
	SHA256  string `json:"sha256"`
	Deleted bool   `json:"deleted"`
}

// frozen is the clock the mutation corpus was recorded with.
var frozen = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func freezeClock(t testing.TB) {
	old := engine.Clock
	engine.Clock = func() time.Time { return frozen }
	t.Cleanup(func() { engine.Clock = old })
}

// countingAuth records authorization requests (the corpus' permission
// prompts) and allows them.
type countingAuth struct{ n int }

func (c *countingAuth) Authorize(context.Context, engine.AuthRequest) error { c.n++; return nil }

// jscDetail matches engine-specific JavaScriptCore parse text and the Go
// parser's own detail; both are replaced by one placeholder so the stable
// prefix and everything around it still compare byte-exactly.
var jscDetail = regexp.MustCompile(`JSON [Pp]arse error: [^"\\]*`)

func normEngineText(s string) string {
	return jscDetail.ReplaceAllString(s, "JSON parse error: <engine detail>")
}

var errDeferred = fmt.Errorf("not a read tool")

// runTool runs one read-only tool on the core surface and returns its
// exact output text.
func runTool(e *engine.Engine, tool string, in ojson.Value) (string, error) {
	pretty := func(v ojson.Value, err error) (string, error) {
		if err != nil {
			return "", err
		}
		return string(ojson.Pretty(v)), nil
	}
	switch tool {
	case "workplan_read":
		p, err := input.ParseReadInput(in, input.SurfaceCore)
		if err != nil {
			return "", err
		}
		return pretty(e.Read(p))
	case "workplan_list":
		p, err := input.ParseListInput(in, input.SurfaceCore)
		if err != nil {
			return "", err
		}
		return pretty(e.List(p))
	case "workplan_inspect":
		p, err := input.ParseInspectInput(in, input.SurfaceCore)
		if err != nil {
			return "", err
		}
		return pretty(e.Inspect(p))
	case "workplan_validate":
		p, err := input.ParseValidateInput(in, input.SurfaceCore)
		if err != nil {
			return "", err
		}
		return pretty(e.Validate(p))
	case "workplan_doctor":
		p, err := input.ParseDoctorInput(in, input.SurfaceCore)
		if err != nil {
			return "", err
		}
		return pretty(e.Doctor(p))
	case "workplan_resume":
		p, err := input.ParseResumeInput(in, input.SurfaceCore)
		if err != nil {
			return "", err
		}
		_, text, err := e.Resume(p)
		return text, err
	case "workplan_compact":
		data, err := input.ParseMutationInput("compact", in, input.SurfaceCore)
		if err != nil {
			return "", err
		}
		return pretty(e.CompactPreview(data))
	}
	return "", errDeferred
}

// runMutation parses core input and prepares/executes one tool call.
func runMutation(ctx context.Context, e *engine.Engine, tool string, in ojson.Value, auth engine.Authorizer) (engine.Output, error) {
	name := strings.TrimPrefix(tool, "workplan_")
	data, err := input.ParseMutationInput(name, in, input.SurfaceCore)
	if err != nil {
		return engine.Output{}, err
	}
	p, err := e.Prepare(name, data)
	if err != nil {
		return engine.Output{}, err
	}
	return e.Execute(ctx, p, auth, engine.ExecOptions{})
}

func mustEngine(t testing.TB, root string) *engine.Engine {
	t.Helper()
	e, err := engine.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func loadVectors(t *testing.T, dirs ...string) []string {
	var files []string
	for _, d := range dirs {
		err := filepath.WalkDir(testutil.Testdata("vectors", d), func(p string, de os.DirEntry, err error) error {
			if err == nil && !de.IsDir() && strings.HasSuffix(p, ".json") {
				files = append(files, p)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)
	return files
}

func sha(s string) string { return testutil.SHA256Hex(s) }

func firstDiff(got, want string) string { return testutil.FirstDiff(got, want) }

func jsonEqual(a, b any) bool { return reflect.DeepEqual(a, b) }

// decodeAny decodes JSON text with exact numbers, after replacing engine
// parse detail by its placeholder.
func decodeAny(s string) (any, error) {
	var v any
	d := json.NewDecoder(strings.NewReader(normEngineText(s)))
	d.UseNumber()
	return v, d.Decode(&v)
}

func expectedAny(v *vector) any {
	var out any
	d := json.NewDecoder(strings.NewReader(normEngineText(string(v.Expect.Output))))
	d.UseNumber()
	d.Decode(&out)
	return out
}

// oracleText is the vector's expected output text.
func oracleText(v *vector) string {
	if v.Expect.OutputText != nil {
		return *v.Expect.OutputText
	}
	p, _ := ojson.Parse(v.Expect.Output)
	return string(ojson.Pretty(p.Value))
}

// mutationOracleText is a mutation vector's expected output text.
func mutationOracleText(v *mutationVector) string {
	if v.Expect.OutputText != nil {
		return *v.Expect.OutputText
	}
	p, _ := ojson.Parse(v.Expect.Output)
	return string(ojson.Pretty(p.Value))
}

func num(v any) int {
	n, _ := v.(json.Number).Int64()
	return int(n)
}

func asList(x any) []any { l, _ := x.([]any); return l }

func objWithout(v ojson.Value, keys ...string) ojson.Value { return testutil.Without(v, keys...) }

func objReplace(v ojson.Value, key string, nv ojson.Value) ojson.Value {
	return testutil.Replace(v, key, nv)
}

func has(v ojson.Value, key string) bool { return testutil.Has(v, key) }

// reencode serializes v in the form of text: pretty, or compact for a
// compact resume packet.
func reencode(text string, v ojson.Value) string {
	if !strings.HasPrefix(text, "{\n") {
		return string(ojson.Compact(v))
	}
	return string(ojson.Pretty(v))
}

func numMember(v ojson.Value, keys ...string) int {
	for _, k := range keys {
		v, _ = v.Get(k)
	}
	n, _ := v.Float()
	return int(n)
}

func strMember(v ojson.Value, keys ...string) string {
	for _, k := range keys {
		v, _ = v.Get(k)
	}
	return v.Str()
}

// inputID is the id member of a vector input.
func inputID(raw json.RawMessage) string {
	var in struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &in)
	return in.ID
}

// checkResumeInvariants: the complete packet fits its budget.
func checkResumeInvariants(t *testing.T, v *vector, text string) {
	t.Helper()
	var in struct {
		MaxChars *int `json:"maxChars"`
	}
	json.Unmarshal(v.Call.Input, &in)
	max := engine.DefaultResumeMaxChars
	if in.MaxChars != nil {
		max = *in.MaxChars
	}
	if n := ojson.UTF16Len(text); n > max {
		t.Fatalf("resume output %d exceeds budget %d", n, max)
	}
}

// snapshotFiles maps relative path -> sha256 for every regular file.
// The change log is a Shiori-only advisory sidecar (contracts §24): the
// reference writes none, so it is left out of every comparison.
func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for rel, st := range testutil.Fingerprint(t, root) {
		if st.Mode.IsRegular() && !strings.HasSuffix(rel, history.Suffix) {
			out[filepath.ToSlash(rel)] = st.SHA
		}
	}
	return out
}

// checkChanged compares the exact changed-file set and bytes with the
// vector's.
func checkChanged(t *testing.T, want map[string]changedFile, before, after map[string]string) {
	t.Helper()
	changed := map[string]string{}
	for k, b := range before {
		if a, ok := after[k]; !ok {
			changed[k] = "deleted"
		} else if a != b {
			changed[k] = a
		}
	}
	for k, a := range after {
		if _, ok := before[k]; !ok {
			changed[k] = a
		}
	}
	var diffs []string
	for k, w := range want {
		got, ok := changed[k]
		switch {
		case !ok:
			diffs = append(diffs, "unchanged but expected change: "+k)
		case w.Deleted && got != "deleted":
			diffs = append(diffs, "expected deletion: "+k)
		case !w.Deleted && got != w.SHA256:
			diffs = append(diffs, fmt.Sprintf("bytes differ: %s", k))
		}
	}
	for k := range changed {
		if _, ok := want[k]; !ok {
			diffs = append(diffs, "unexpected change: "+k)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Fatalf("file changes: %v", diffs)
	}
}

func readOrNil(root, rel string) []byte {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	return b
}

func decodeJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// hashesMatchDisk checks the output hashes against the committed files.
func hashesMatchDisk(t *testing.T, root, id string, out engine.Output) {
	t.Helper()
	s, err := snapshot.Load(root, id, snapshot.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	ph, _ := out.Value.Get("planHash")
	sh, _ := out.Value.Get("stateHash")
	if ph.Str() != s.PlanHash || sh.Str() != s.StateHash {
		t.Fatalf("output hashes %s/%s, disk %s/%s", ph.Str(), sh.Str(), s.PlanHash, s.StateHash)
	}
}
