package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/testutil"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestJSONOutputMatchesVectors: --json prints exactly the tool output text
// (JSON.stringify(result, null, 2)) the corpus records.
func TestJSONOutputMatchesVectors(t *testing.T) {
	cases := []struct {
		vector string
		args   []string
	}{
		{"tools/full-valid/full-plan--read.json", []string{"read", "full-plan"}},
		{"tools/full-valid/full-plan--read-step.json", []string{"read", "--phase", "phase-b", "--step", "step-b2", "full-plan"}},
		{"tools/full-valid/full-plan--validate.json", []string{"validate", "full-plan"}},
		{"tools/full-valid/full-plan--inspect.json", []string{"inspect", "full-plan"}},
		{"tools/full-valid/doctor-limit1.json", []string{"doctor", "--limit", "1"}},
		{"resume/large-paging/big-plan--max4096.json", []string{"resume", "big-plan", "--max-chars", "4096"}},
	}
	for _, c := range cases {
		t.Run(c.vector, func(t *testing.T) {
			var v struct {
				Fixture string `json:"fixture"`
				Expect  struct {
					OutputSha256 string  `json:"outputSha256"`
					OutputText   *string `json:"outputText"`
				} `json:"expect"`
			}
			testutil.ReadJSON(t, testutil.Testdata("vectors", c.vector), &v)
			root := testutil.NewRoot(t, v.Fixture)
			before := testutil.Fingerprint(t, root.Path)
			code, out, errOut := run(append(c.args, "--json", "--root", root.Path)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
				t.Fatalf("CLI wrote: %v", d)
			}
			got := root.Normalize(strings.TrimSuffix(out, "\n"))
			// A vector whose output differs on purpose is pinned in
			// testdata/expected.
			if x, ok := testutil.Expected(t)[strings.TrimSuffix(c.vector, ".json")]; ok && x.Pinned() {
				if !root.SameLength() && strings.HasPrefix(c.vector, "resume/") {
					t.Skip("budget-sensitive vector needs a generation-length root")
				}
				if sum := sha256hex(got); sum != x.OutputSha256 {
					t.Fatalf("sha %s want the pinned %s", sum, x.OutputSha256)
				}
				return
			}
			if v.Expect.OutputText != nil && got != *v.Expect.OutputText {
				t.Fatalf("resume text differs")
			}
			sum := sha256hex(got)
			if sum != v.Expect.OutputSha256 {
				t.Fatalf("sha %s want %s", sum, v.Expect.OutputSha256)
			}
		})
	}
}

func TestExitCodesAndErrors(t *testing.T) {
	root := testutil.NewRoot(t, "invalid-structure")
	if code, _, _ := run("validate", "broken-plan", "--root", root.Path); code != 1 {
		t.Fatalf("invalid plan must exit 1, got %d", code)
	}
	if code, _, _ := run("frobnicate"); code != 2 {
		t.Fatal("unknown command must exit 2")
	}
	if code, _, _ := run("list", "--limit", "3", "--root", root.Path); code != 2 {
		t.Fatal("inapplicable flag must exit 2")
	}
	code, out, _ := run("read", "nope", "--json", "--root", root.Path)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	var e struct {
		OK    bool `json:"ok"`
		Error struct {
			Class   string `json:"class"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &e); err != nil || e.OK || e.Error.Class != "missing_artifact" {
		t.Fatalf("error object %q (%v)", out, err)
	}
	code, out, _ = run("resume", "x", "--max-chars", "10", "--json", "--root", root.Path)
	if code != 1 || !strings.Contains(out, `"invalid_input"`) || !strings.Contains(out, `"path": "maxChars"`) {
		t.Fatalf("invalid input: %d %s", code, out)
	}
	code, out, _ = run("read", "--input", `{"id":"broken-plan","includeMarkdown":false}`, "--json", "--root", root.Path)
	if code != 0 || !strings.Contains(out, `"selection"`) || strings.Contains(out, `"content"`) {
		t.Fatalf("--input: %d %s", code, out)
	}
}

// TestReadSliceFlags covers the filtered-read flags.
func TestReadSliceFlags(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	code, out, _ := run("read", "full-plan", "--phase", "phase-b", "--json", "--root", root.Path)
	if code != 0 || strings.Contains(out, `"content"`) || strings.Contains(out, `"reviewFindings"`) || !strings.Contains(out, `"slice"`) {
		t.Fatalf("slice: %d %s", code, out)
	}
	code, out, _ = run("read", "full-plan", "--phase", "phase-b", "--notes", "--markdown", "--json", "--root", root.Path)
	if code != 0 || !strings.Contains(out, `"content"`) || !strings.Contains(out, `"reviewFindings"`) || !strings.Contains(out, `"notesIncluded": true`) {
		t.Fatalf("slice with notes and Markdown: %d %s", code, out)
	}
	if code, _, _ := run("read", "full-plan", "--markdown", "--no-markdown", "--root", root.Path); code != 2 {
		t.Fatal("--markdown with --no-markdown must be a usage error")
	}
}

// TestHumanOutputAllCommands smoke-tests the human renderer on every
// read-only command and the read-only gate for each.
func TestHumanOutputAllCommands(t *testing.T) {
	for _, fx := range []struct{ fixture, id string }{{"full-valid", "full-plan"}, {"pending-journal", "tx-plan"}, {"resume-stress", "stress-plan"}, {"empty-workspace", "x"}} {
		root := testutil.NewRoot(t, fx.fixture)
		before := testutil.Fingerprint(t, root.Path)
		for _, args := range [][]string{{"list"}, {"doctor"}, {"read", fx.id}, {"inspect", fx.id}, {"validate", fx.id}, {"resume", fx.id, "--max-chars", "4096"}} {
			code, out, errOut := run(append(args, "--root", root.Path)...)
			if code == 2 || (code != 0 && errOut == "" && args[0] != "validate") {
				t.Fatalf("%s %v: exit %d %s", fx.fixture, args, code, errOut)
			}
			if code == 0 && out == "" {
				t.Fatalf("%s %v: no output", fx.fixture, args)
			}
		}
		if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
			t.Fatalf("%s: CLI wrote %v", fx.fixture, d)
		}
	}
}
