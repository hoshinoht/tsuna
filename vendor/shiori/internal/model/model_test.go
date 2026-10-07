package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func parseRaw(t *testing.T, raw json.RawMessage) ojson.Parsed {
	t.Helper()
	p, err := ojson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestMarkdownVectors renders every markdown vector byte-exactly.
func TestMarkdownVectors(t *testing.T) {
	files, _ := filepath.Glob(testutil.Testdata("vectors", "markdown", "*.json"))
	n := 0
	for _, f := range files {
		var v struct {
			ID       string          `json:"id"`
			Function string          `json:"function"`
			Input    json.RawMessage `json:"input"`
			Expect   struct {
				Kind    string `json:"kind"`
				File    string `json:"file"`
				SHA256  string `json:"sha256"`
				Message string `json:"message"`
			} `json:"expect"`
		}
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		if v.Function != "renderWorkplanMarkdown" {
			continue
		}
		n++
		t.Run(v.ID, func(t *testing.T) {
			p, err := DecodePlan(parseRaw(t, v.Input))
			if err != nil {
				t.Fatal(err)
			}
			if x, ok := testutil.Expected(t)[v.ID]; ok && x.Has("missing-specfiles-renders") {
				// The reference renderer throws a JavaScript TypeError on a raw
				// legacy document; Go renders only normalized documents, where
				// absent specFiles is [].
				if p.HasSpecFiles {
					t.Fatal("fixture should lack specFiles")
				}
				if _, err := RenderMarkdown(p); err != nil {
					t.Fatal(err)
				}
				return
			}
			// The reference rendering is RenderMarkdownLegacy; the current
			// rendering changes only the finding status spacing, checked
			// below against the same oracle file.
			out, err := RenderMarkdownLegacy(p)
			if v.Expect.Kind == "error" {
				if err == nil || err.Error() != v.Expect.Message {
					t.Fatalf("error %v want %q", err, v.Expect.Message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(testutil.Testdata("vectors", v.Expect.File))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(out)
			if string(out) != string(want) || hex.EncodeToString(sum[:]) != v.Expect.SHA256 {
				t.Fatalf("markdown mismatch\n got %q\nwant %q", out, want)
			}
			checkFindingRendering(t, v.ID, p, want)
		})
	}
	testutil.WritePins(t, "markdown/")
	if n != 13 {
		t.Fatalf("expected 13 render vectors, ran %d", n)
	}
}

// TestStructureVectors checks compatible decoding and structure issues
// with exact field paths (validation/structure).
func TestStructureVectors(t *testing.T) {
	files, _ := filepath.Glob(testutil.Testdata("vectors", "validation", "structure", "*.json"))
	if len(files) != 37 {
		t.Fatalf("expected 37 structure vectors, got %d", len(files))
	}
	for _, f := range files {
		var v struct {
			ID    string `json:"id"`
			Input struct {
				Document   json.RawMessage `json:"document"`
				ExpectedID *string         `json:"expectedId"`
			} `json:"input"`
			Expect struct {
				DocumentDecoded bool     `json:"documentDecoded"`
				Issues          []string `json:"issues"`
			} `json:"expect"`
		}
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		t.Run(v.ID, func(t *testing.T) {
			p, err := DecodePlan(parseRaw(t, v.Input.Document))
			var got []string
			if err != nil {
				if v.Expect.DocumentDecoded {
					t.Fatalf("unexpected decode failure: %v", err)
				}
				for _, is := range err.(*DecodeError).Issues {
					got = append(got, is.String())
				}
			} else {
				if !v.Expect.DocumentDecoded {
					t.Fatal("expected decode failure")
				}
				got = ValidateStructure(p, v.Input.ExpectedID)
			}
			if strings.Join(got, "\n") != strings.Join(v.Expect.Issues, "\n") {
				t.Fatalf("issues\n got %q\nwant %q", got, v.Expect.Issues)
			}
		})
	}
}

func TestNormalizeID(t *testing.T) {
	cases := map[string]string{
		"  FULL Plan ":           "full-plan",
		"phase-𝒜-x":              "phase-x",
		"step-été":               "step-t",
		"STEP--X__y":             "step-x-y",
		"  Phase A_1 !! ":        "phase-a-1",
		"İx":                     "i-x",
		strings.Repeat("x", 100): strings.Repeat("x", 80),
	}
	for in, want := range cases {
		got, err := NormalizeID(in)
		if err != nil || got != want {
			t.Errorf("NormalizeID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "!!!", "𝒜"} {
		if _, err := NormalizeID(bad); err == nil {
			t.Errorf("NormalizeID(%q) should fail", bad)
		}
	}
}

func TestValidDatetime(t *testing.T) {
	good := []string{"2026-01-02T03:04:05.000Z", "2026-01-02T03:04Z", "2024-02-29T00:00:00Z", "2026-01-02T03:04:05.123456Z"}
	bad := []string{"2026-01-02", "2026-01-02T03:04:05+08:00", "2025-02-29T00:00Z", "yesterday", "2026-13-01T00:00Z"}
	for _, s := range good {
		if !ValidDatetime(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if ValidDatetime(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

// ---- finding rendering ----

// checkFindingRendering: the current rendering equals the oracle
// (reference) rendering with a space before every finding's "(status)",
// and nothing else changes. A render vector whose output changes is
// listed in testdata/expected and pinned.
func checkFindingRendering(t *testing.T, id string, p *Plan, oracle []byte) {
	t.Helper()
	cur, err := RenderMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	want := string(oracle)
	for i := range p.Findings {
		f := &p.Findings[i]
		if f.Status == nil || *f.Status == "" {
			continue
		}
		old := "- [" + f.Severity + "] " + f.Title + "(" + *f.Status + ")"
		if !strings.Contains(want, old) {
			t.Fatalf("finding line %q not in the oracle rendering", old)
		}
		want = strings.Replace(want, old, "- ["+f.Severity+"] "+f.Title+" ("+*f.Status+")", 1)
	}
	if string(cur) != want {
		t.Fatalf("rendering differs beyond the finding status spacing\n got %q\nwant %q", cur, want)
	}
	// Markdown generated either way is recognized as generated.
	for _, md := range [][]byte{oracle, cur} {
		if gen, err := IsGeneratedMarkdown(p, md); err != nil || !gen {
			t.Fatalf("IsGeneratedMarkdown = %v, %v", gen, err)
		}
	}
	x, listed := testutil.Expected(t)[id]
	if string(cur) == string(oracle) {
		if listed {
			t.Fatalf("%s is listed in %s but renders like the oracle", id, testutil.ExpectedFile)
		}
		return
	}
	if !listed || !x.Has("finding-rendering") {
		t.Fatalf("%s renders differently from the oracle but is not listed with finding-rendering", id)
	}
	sum := sha256.Sum256(cur)
	testutil.CheckPin(t, id, x, hex.EncodeToString(sum[:]), len(cur), true)
}
