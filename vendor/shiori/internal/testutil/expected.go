package testutil

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ExpectedFile is the consolidated expectations set: every oracle vector
// whose Go output intentionally differs from the reference.
const ExpectedFile = "expected/expectations.json"

// Expectation documents one vector's approved difference from the oracle.
type Expectation struct {
	Reason    string   `json:"reason"`
	Contracts string   `json:"contracts"`
	Compare   []string `json:"compare"`
	// Authorizations overrides the oracle's permission prompt count.
	Authorizations *int `json:"authorizations,omitempty"`
	// Message is the expected refusal text of a refusal comparator.
	Message string `json:"message,omitempty"`
	// OutputSha256 and RawOutputLength pin the Go output (root-normalized;
	// for a refusal, the error text) of an approved design change.
	OutputSha256    string `json:"outputSha256,omitempty"`
	RawOutputLength int    `json:"rawOutputLength,omitempty"`
}

// Has reports whether the entry lists comparator c.
func (x Expectation) Has(c string) bool {
	for _, y := range x.Compare {
		if y == c {
			return true
		}
	}
	return false
}

// Pinned reports whether the entry pins the Go output.
func (x Expectation) Pinned() bool { return x.OutputSha256 != "" }

type expectedDoc struct {
	Note    string                 `json:"note"`
	Vectors map[string]Expectation `json:"vectors"`
}

var (
	expectedOnce sync.Once
	expectedDocV expectedDoc
	expectedErr  error
	pinMu        sync.Mutex
	pinUpdates   = map[string][2]any{}
)

// UpdatePins reports whether SHIORI_EXPECTED_UPDATE=1 asks the tests to
// re-record the pinned hashes of listed vectors (review the diff).
func UpdatePins() bool { return os.Getenv("SHIORI_EXPECTED_UPDATE") == "1" }

// Expected returns the consolidated expectations, keyed by vector id.
func Expected(t testing.TB) map[string]Expectation {
	t.Helper()
	expectedOnce.Do(func() {
		var data []byte
		data, expectedErr = os.ReadFile(Testdata(ExpectedFile))
		if expectedErr == nil {
			d := json.NewDecoder(bytes.NewReader(data))
			d.DisallowUnknownFields()
			expectedErr = d.Decode(&expectedDocV)
		}
	})
	if expectedErr != nil {
		t.Fatalf("reading %s: %v", ExpectedFile, expectedErr)
	}
	return expectedDocV.Vectors
}

// CheckPin compares a listed vector's output with its pin, or records a
// new pin in update mode. checkLen also compares the UTF-16 length.
func CheckPin(t testing.TB, id string, x Expectation, normalizedSha string, rawLen int, checkLen bool) {
	t.Helper()
	if UpdatePins() {
		pinMu.Lock()
		pinUpdates[id] = [2]any{normalizedSha, rawLen}
		pinMu.Unlock()
		return
	}
	if !x.Pinned() {
		t.Fatalf("%s: output differs on purpose but the entry in %s has no pin", id, ExpectedFile)
	}
	if normalizedSha != x.OutputSha256 {
		t.Fatalf("%s: pinned output changed: sha %s want %s (SHIORI_EXPECTED_UPDATE=1 to re-pin after review)", id, normalizedSha, x.OutputSha256)
	}
	if checkLen && rawLen != x.RawOutputLength {
		t.Fatalf("%s: raw length %d want %d", id, rawLen, x.RawOutputLength)
	}
}

// WritePins rewrites the pins recorded in update mode for the vectors
// with one of the given prefixes; other entries stay as they are.
func WritePins(t testing.TB, prefixes ...string) {
	t.Helper()
	if !UpdatePins() {
		return
	}
	var doc struct {
		Note    string                     `json:"note"`
		Vectors map[string]json.RawMessage `json:"vectors"`
	}
	data, err := os.ReadFile(Testdata(ExpectedFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	pinMu.Lock()
	defer pinMu.Unlock()
	n := 0
	for id, p := range pinUpdates {
		owned := false
		for _, pre := range prefixes {
			owned = owned || strings.HasPrefix(id, pre)
		}
		raw, ok := doc.Vectors[id]
		if !owned || !ok {
			continue
		}
		var x Expectation
		if err := json.Unmarshal(raw, &x); err != nil {
			t.Fatal(err)
		}
		x.OutputSha256, x.RawOutputLength = p[0].(string), p[1].(int)
		b, _ := json.Marshal(x)
		doc.Vectors[id] = b
		n++
	}
	ids := make([]string, 0, len(doc.Vectors))
	for id := range doc.Vectors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out bytes.Buffer
	out.WriteString("{\n  \"note\": ")
	note, _ := json.Marshal(doc.Note)
	out.Write(note)
	out.WriteString(",\n  \"vectors\": {")
	for i, id := range ids {
		if i > 0 {
			out.WriteString(",")
		}
		key, _ := json.Marshal(id)
		var ind bytes.Buffer
		json.Indent(&ind, doc.Vectors[id], "    ", "  ")
		out.WriteString("\n    ")
		out.Write(key)
		out.WriteString(": ")
		out.Write(ind.Bytes())
	}
	out.WriteString("\n  }\n}\n")
	if err := os.WriteFile(Testdata(ExpectedFile), out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("re-pinned %d vectors in %s", n, ExpectedFile)
}
