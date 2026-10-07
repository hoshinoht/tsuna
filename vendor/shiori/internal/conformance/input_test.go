package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

var readOnlyTools = map[string]bool{
	"workplan_read": true, "workplan_list": true, "workplan_inspect": true,
	"workplan_validate": true, "workplan_resume": true, "workplan_doctor": true,
}

// parseForSurface validates input and re-renders the accepted data in the
// reference's key order (schema order, workspaceRoot first on core).
func parseForSurface(tool string, v ojson.Value, s input.Surface) (map[string]any, error) {
	out := map[string]any{}
	put := func(k string, p any) {
		switch x := p.(type) {
		case *string:
			if x != nil {
				out[k] = *x
			}
		case *int:
			if x != nil {
				out[k] = float64(*x)
			}
		case *bool:
			if x != nil {
				out[k] = *x
			}
		case string:
			out[k] = x
		}
	}
	switch tool {
	case "workplan_read":
		in, err := input.ParseReadInput(v, s)
		if err != nil {
			return nil, err
		}
		put("workspaceRoot", in.WorkspaceRoot)
		put("id", in.ID)
		put("phaseId", in.PhaseID)
		put("stepId", in.StepID)
		put("includeMarkdown", in.IncludeMarkdown)
	case "workplan_list":
		in, err := input.ParseListInput(v, s)
		if err != nil {
			return nil, err
		}
		put("workspaceRoot", in.WorkspaceRoot)
	case "workplan_inspect":
		in, err := input.ParseInspectInput(v, s)
		if err != nil {
			return nil, err
		}
		put("workspaceRoot", in.WorkspaceRoot)
		put("id", in.ID)
		put("phaseId", in.PhaseID)
		put("limit", in.Limit)
		put("cursor", in.Cursor)
	case "workplan_validate":
		in, err := input.ParseValidateInput(v, s)
		if err != nil {
			return nil, err
		}
		put("workspaceRoot", in.WorkspaceRoot)
		put("id", in.ID)
	case "workplan_resume":
		in, err := input.ParseResumeInput(v, s)
		if err != nil {
			return nil, err
		}
		put("workspaceRoot", in.WorkspaceRoot)
		put("id", in.ID)
		put("maxChars", in.MaxChars)
		put("limit", in.Limit)
		put("cursor", in.Cursor)
		put("phaseId", in.PhaseID)
		put("stepId", in.StepID)
	case "workplan_doctor":
		in, err := input.ParseDoctorInput(v, s)
		if err != nil {
			return nil, err
		}
		put("workspaceRoot", in.WorkspaceRoot)
		put("id", in.ID)
		put("limit", in.Limit)
	}
	return out, nil
}

var surfaces = []struct {
	name string
	s    input.Surface
}{{"core", input.SurfaceCore}, {"native", input.SurfaceNative}}

// TestInputVectors checks accept/reject and exact messages for the
// read-only tools on both surfaces.
func TestInputVectors(t *testing.T) {
	files, _ := filepath.Glob(testutil.Testdata("vectors", "validation", "input", "*.json"))
	if len(files) != 55 {
		t.Fatalf("expected 55 input vectors, got %d", len(files))
	}
	ran := 0
	for _, f := range files {
		var v struct {
			ID     string          `json:"id"`
			Tool   string          `json:"tool"`
			Input  json.RawMessage `json:"input"`
			Expect map[string]*struct {
				OK      bool           `json:"ok"`
				Data    map[string]any `json:"data"`
				Message string         `json:"message"`
			} `json:"expect"`
		}
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		if !readOnlyTools[v.Tool] {
			continue
		}
		ran++
		t.Run(v.ID, func(t *testing.T) {
			parsed, err := ojson.Parse(v.Input)
			if err != nil {
				t.Fatal(err)
			}
			for _, sf := range surfaces {
				exp := v.Expect[sf.name]
				got, err := parseForSurface(v.Tool, parsed.Value, sf.s)
				if !exp.OK {
					if err == nil || err.Error() != exp.Message {
						t.Fatalf("%s: error %v, want %q", sf.name, err, exp.Message)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: unexpected error %v", sf.name, err)
				}
				if !reflect.DeepEqual(got, exp.Data) {
					t.Fatalf("%s: data %v want %v", sf.name, got, exp.Data)
				}
			}
		})
	}
	if ran != 16 {
		t.Fatalf("expected 16 read-only input vectors, ran %d", ran)
	}
}

// TestMutationInputVectors checks the 39 mutating-tool input vectors on
// both surfaces: accept/reject, exact messages, and the accepted data in
// the reference's key order, through the comparators of listed vectors.
func TestMutationInputVectors(t *testing.T) {
	files, _ := filepath.Glob(testutil.Testdata("vectors", "validation", "input", "*.json"))
	ran, differ := 0, 0
	var notes []string
	for _, f := range files {
		data, _ := os.ReadFile(f)
		parsed, err := ojson.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		vec := parsed.Value
		tool, _ := vec.Get("tool")
		if readOnlyTools[tool.Str()] {
			continue
		}
		name := strings.TrimPrefix(tool.Str(), "workplan_")
		id, _ := vec.Get("id")
		in, _ := vec.Get("input")
		expect, _ := vec.Get("expect")
		x, listed := testutil.Expected(t)[id.Str()]
		ran++
		if listed {
			differ++
			notes = append(notes, id.Str()+": "+x.Reason+" ("+x.Contracts+")")
		}
		t.Run(id.Str(), func(t *testing.T) {
			for _, sf := range surfaces {
				exp, _ := expect.Get(sf.name)
				if exp.Kind() == ojson.Null || exp.Kind() == ojson.Undefined {
					continue
				}
				got, err := input.ParseMutationInput(name, in, sf.s)
				ok, _ := exp.Get("ok")
				msg, _ := exp.Get("message")
				switch {
				case listed && x.Has("nested-unknown-key"):
					// Nested unknown keys reject with their path.
					want := `Invalid create input: phases.0.steps.0: Unrecognized key: "extra"; phases.0: Unrecognized key: "bogus"`
					if err == nil || err.Error() != want {
						t.Fatalf("%s: error %v, want %q", sf.name, err, want)
					}
					continue
				case listed && x.Has("wipe-mode-enum"):
					// The enum lists the "wipe" mode; the message is
					// otherwise the oracle's.
					want := strings.Replace(msg.Str(), `"markdown-only"`, `"markdown-only"|"wipe"`, 1)
					if want == msg.Str() || err == nil || err.Error() != want {
						t.Fatalf("%s: error %v, want %q", sf.name, err, want)
					}
					continue
				}
				if !ok.Bool() {
					if err == nil {
						t.Fatalf("%s: accepted, want error %q", sf.name, msg.Str())
					}
					if sf.s == input.SurfaceNative && listed && x.Has("hash-guidance") {
						checkErrorText(t, id.Str(), err.Error(), msg.Str(), x, listed)
					} else if err.Error() != msg.Str() {
						t.Fatalf("%s: error %v, want %q", sf.name, err, msg.Str())
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: unexpected error %v", sf.name, err)
				}
				want, _ := exp.Get("data")
				if g, w := string(ojson.Compact(got)), string(ojson.Compact(want)); g != w {
					t.Fatalf("%s: data\n got %s\nwant %s", sf.name, g, w)
				}
			}
		})
	}
	if ran != 39 {
		t.Fatalf("ran %d mutating input vectors, want 39", ran)
	}
	sort.Strings(notes)
	t.Logf("mutating input vectors: %d, with documented differences: %d", ran, differ)
	for _, n := range notes {
		t.Log(n)
	}
	testutil.WritePins(t, "validation/input/")
}
