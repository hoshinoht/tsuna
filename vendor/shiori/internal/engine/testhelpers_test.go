package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

func hostnameT(t *testing.T) string {
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func itoaT(i int) string              { return strconv.Itoa(i) }
func writeFileT(p, body string) error { return os.WriteFile(p, []byte(body), 0o600) }
func chtimesT(p string, tm time.Time) { os.Chtimes(p, tm, tm) }

// allowAll authorizes everything.
type allowAll struct{}

func (allowAll) Authorize(context.Context, AuthRequest) error { return nil }

// countingAuth records authorization requests and allows them.
type countingAuth struct{ n int }

func (c *countingAuth) Authorize(context.Context, AuthRequest) error { c.n++; return nil }

// frozen is the clock the tests write with.
var frozen = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func freezeClock(t testing.TB) {
	old := Clock
	Clock = func() time.Time { return frozen }
	t.Cleanup(func() { Clock = old })
}

// runMutation parses core input and prepares/executes one tool call.
func runMutation(ctx context.Context, e *Engine, tool string, in ojson.Value, auth Authorizer) (Output, error) {
	name := strings.TrimPrefix(tool, "workplan_")
	data, err := input.ParseMutationInput(name, in, input.SurfaceCore)
	if err != nil {
		return Output{}, err
	}
	p, err := e.Prepare(name, data)
	if err != nil {
		return Output{}, err
	}
	return e.Execute(ctx, p, auth, ExecOptions{})
}

// mustParse validates core input for a mutating tool.
func mustParse(t *testing.T, tool string, v ojson.Value) ojson.Value {
	t.Helper()
	d, err := input.ParseMutationInput(tool, v, input.SurfaceCore)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// relPath is the project-relative form of an absolute path under root.
func (e *Engine) relPath(p string) string {
	r, err := filepath.Rel(e.Root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// markdownGenerated reports whether the linked Markdown byte-equals the
// generated rendering of the normalized stored JSON (the reference's
// "generated" classification).
func (e *Engine) markdownGenerated(id string) (generated, present bool, err error) {
	s, err := e.load(id)
	if err != nil {
		return false, false, err
	}
	if !s.Markdown.Exists {
		return false, false, nil
	}
	gen, err := e.isGenerated(s.Plan, s.Markdown.Bytes)
	if err != nil {
		return false, true, err
	}
	return gen, true, nil
}

// checkAdvice restates an advice's counts from the raw plan bytes.
func checkAdvice(root, id, tool string, a ojson.Value) error {
	return testutil.CheckAdviceCounts(root, id, tool, a, advisor.DefaultRolloverKeep)
}

const (
	// advisorPointerPrefix starts a compaction archive pointer note.
	advisorPointerPrefix = "Compaction archive: "
	// wipedNeedle starts doctor's note on a wiped plan.
	wipedNeedle = "phases: Plan was wiped (workplan_reset mode=wipe)"
	// markerNeedle is in the missing step marker warning.
	markerNeedle = "has no step marker (<!-- workplan-step-id: <stepId> -->)"
)

// snapshotFiles maps relative path -> sha256 for every regular file.
func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for rel, st := range testutil.Fingerprint(t, root) {
		if st.Mode.IsRegular() {
			out[filepath.ToSlash(rel)] = st.SHA
		}
	}
	return out
}

// fileSHA is the hex SHA-256 of a file's bytes; ok is false when it
// cannot be read.
func fileSHA(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return testutil.SHA256Hex(string(data)), true
}
