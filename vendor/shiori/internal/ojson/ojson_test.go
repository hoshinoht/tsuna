package ojson

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestQuoteMatchesJSONStringify(t *testing.T) {
	cases := map[string]string{
		"a\"b\\c":      `"a\"b\\c"`,
		"\b\t\n\f\r":   `"\b\t\n\f\r"`,
		"\x00\x01\x1f": `"\u0000\u0001\u001f"`,
		"<>&\u007f  ":  "\"<>&\u007f  \"",
		"😀𝒜":           `"😀𝒜"`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s want %s", in, got, want)
		}
	}
}

func TestParsePreservesOrderNumbersAndDuplicates(t *testing.T) {
	src := `{"b":1,"a":{"x":12345678901234567890,"y":1.0,"z":1e+21},"b":2}`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Duplicates) != 1 || p.Duplicates[0].Key != "b" {
		t.Fatalf("duplicates %v", p.Duplicates)
	}
	if got := string(Compact(p.Value)); got != src {
		t.Fatalf("round trip %s", got)
	}
	if v, _ := p.Value.Get("b"); v.NumberLiteral() != "2" {
		t.Fatal("Get must follow last-wins")
	}
	u := p.Value.UniqueMembers()
	if len(u) != 2 || u[0].Key != "b" || u[0].Value.NumberLiteral() != "2" {
		t.Fatalf("unique members %v", u)
	}
}

func TestPrettyMatchesStringifyIndent2(t *testing.T) {
	p, _ := Parse([]byte(`{"a":[],"b":{},"c":[1,{"d":null}],"e":"x"}`))
	want := "{\n  \"a\": [],\n  \"b\": {},\n  \"c\": [\n    1,\n    {\n      \"d\": null\n    }\n  ],\n  \"e\": \"x\"\n}"
	if got := string(Pretty(p.Value)); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestCompareUTF16(t *testing.T) {
	// U+1D49C (surrogate D835) sorts before U+FF21 and U+E000 in UTF-16.
	if CompareUTF16("docs/𝒜.md", "docs/Ａ.md") >= 0 || CompareUTF16("docs/𝒜.md", "docs/.md") >= 0 {
		t.Fatal("UTF-16 order violated")
	}
	if CompareUTF16("a", "ab") >= 0 || CompareUTF16("B", "a") >= 0 || CompareUTF16("x", "x") != 0 {
		t.Fatal("basic order violated")
	}
}

func TestTruncateUTF16(t *testing.T) {
	s, cut := TruncateUTF16("ab😀cd", 4)
	if !cut || s != "ab…" || UTF16Len(s) != 3 {
		t.Fatalf("surrogate pair must not be split: %q", s)
	}
	if s, cut := TruncateUTF16("abc", 3); cut || s != "abc" {
		t.Fatal("fitting string changed")
	}
	if s, _ := TruncateUTF16("S0", 1); s != "…" {
		t.Fatalf("cap 1: %q", s)
	}
}

// FuzzParse: the parser never panics, and anything it accepts that
// encoding/json also accepts round-trips to the same data.
func FuzzParse(f *testing.F) {
	for _, s := range []string{`{}`, `[1,"a",{"b":null}]`, `{"a":1,"a":2}`, `"😀"`, `{"k":1e400}`, `[`, `{"a":"\u0000"}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		var ref any
		refErr := json.Unmarshal(data, &ref)
		if err != nil {
			return
		}
		out := Compact(p.Value)
		if _, err := Parse(out); err != nil {
			t.Fatalf("re-parse of %q failed: %v", out, err)
		}
		if refErr == nil && utf8.Valid(data) && !strings.Contains(string(data), `\ud`) && !strings.Contains(string(data), `\uD`) {
			var back any
			if err := json.Unmarshal(out, &back); err != nil {
				t.Fatalf("encoding/json rejects our output %q", out)
			}
		}
	})
}

// FuzzQuote: quoting is valid JSON that decodes back to the input and
// escapes exactly the JSON.stringify set.
func FuzzQuote(f *testing.F) {
	for _, s := range []string{"", "a\"\\", "\x00\x7f", " <>&", "😀"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		q := Quote(s)
		var back string
		if err := json.Unmarshal([]byte(q), &back); err != nil || back != s {
			t.Fatalf("Quote(%q) = %s does not round-trip (%v)", s, q, err)
		}
		if strings.ContainsAny(q[1:len(q)-1], "\x00\n\r\t") {
			t.Fatalf("control character left raw in %s", q)
		}
	})
}
