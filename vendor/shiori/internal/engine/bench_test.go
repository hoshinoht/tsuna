package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// Fixture identities from docs/baseline.md (extracted file sha256).
var perfFixtures = map[string][2]string{
	"perf-100k": {"cd06bc5924c87ee1c20c95c7537c9851e1218f6d1c27608e4fe9b60c7a27a565", "f11fd877ebd38fb939619b7f663fcebb5cafd12ac508bb5072f421479ed49a35"},
	"perf-1m":   {"2561e5ef5d51549b500e135cf80a4135164b6ffcd3d0d64bdf27ff919ffff25a", "df0e796e1a42fa01f3e3cd42dbcf03f41000a33452cdd79ea28ddd30d1784b0a"},
	"perf-10m":  {"75b17b3339d7be5c6a671369eebc8e200d777fcdc6947fb09275cbf310354e36", "0d09581aade5ea64c4fba6d6c46e5c256cdfc874639e0496403bf842f6bcff22"},
}

var perfSizes = []string{"perf-100k", "perf-1m", "perf-10m"}

var perfOnce sync.Map

// perfRoot materialises a perf fixture under the temp dir (read-only use)
// and verifies the fixture identity.
func perfRoot(tb testing.TB, size string) string {
	tb.Helper()
	dir := filepath.Join(os.TempDir(), "shiori-perf-"+runtime.Version(), size)
	if _, done := perfOnce.Load(size); !done {
		os.RemoveAll(dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatal(err)
		}
		if size == "perf-100k" {
			if err := testutil.CopyTree(testutil.Testdata("perf", size), dir); err != nil {
				tb.Fatal(err)
			}
		} else if err := untar(testutil.Testdata("perf", size+".tar.gz"), filepath.Dir(dir)); err != nil {
			tb.Fatal(err)
		}
		for i, name := range []string{"perf-plan.json", "perf-plan.md"} {
			data, err := os.ReadFile(filepath.Join(dir, ".opencode", "workplan", name))
			if err != nil {
				tb.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != perfFixtures[size][i] {
				tb.Fatalf("%s/%s does not match the baseline fixture identity", size, name)
			}
		}
		perfOnce.Store(size, true)
	}
	return dir
}

func untar(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe tar entry %q", h.Name)
		}
		target := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

// perfOp runs one operation with the baseline inputs and returns the
// output text (its UTF-16 length is the "output chars" column).
func perfOp(e *Engine, op string) (string, error) {
	switch op {
	case "read":
		v, err := e.Read(input.ReadInput{ID: "perf-plan"})
		if err != nil {
			return "", err
		}
		return string(ojson.Pretty(v)), nil
	case "inspect":
		v, err := e.Inspect(input.InspectInput{ID: "perf-plan"})
		if err != nil {
			return "", err
		}
		return string(ojson.Pretty(v)), nil
	case "resume":
		_, text, err := e.Resume(input.ResumeInput{ID: "perf-plan"})
		return text, err
	case "validate":
		v, err := e.Validate(input.ValidateInput{ID: "perf-plan"})
		if err != nil {
			return "", err
		}
		return string(ojson.Pretty(v)), nil
	}
	return "", fmt.Errorf("unknown op %s", op)
}

var perfOps = []string{"read", "inspect", "resume", "validate"}

// BenchmarkOps: go test -bench Ops -benchmem ./internal/engine
// (includes output serialization, like the reference's returned text).
func BenchmarkOps(b *testing.B) {
	for _, size := range perfSizes {
		root := perfRoot(b, size)
		e, err := New(root)
		if err != nil {
			b.Fatal(err)
		}
		for _, op := range perfOps {
			b.Run(size+"/"+op, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := perfOp(e, op); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkWrites: go test -bench Writes -benchmem ./internal/engine
// (prepare + authorize + commit on a private copy of each fixture).
func BenchmarkWrites(b *testing.B) {
	writes := map[string]func(i int) string{
		"append-note": func(i int) string { return `{"id":"perf-plan","appendNotes":["bench note ` + strconv.Itoa(i) + `"]}` },
		"step-status": func(i int) string {
			st := []string{"in_progress", "blocked"}[i%2]
			return `{"id":"perf-plan","updateSteps":[{"phaseId":"phase-1","stepId":"step-1","status":"` + st + `"}]}`
		},
	}
	for _, size := range perfSizes {
		for _, name := range []string{"append-note", "step-status"} {
			b.Run(size+"/"+name, func(b *testing.B) {
				dir := filepath.Join(b.TempDir(), "root")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					b.Fatal(err)
				}
				if err := testutil.CopyTree(perfRoot(b, size), dir); err != nil {
					b.Fatal(err)
				}
				e, err := New(dir)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					p, err := ojson.Parse([]byte(writes[name](i)))
					if err != nil {
						b.Fatal(err)
					}
					data, err := input.ParseMutationInput("update", p.Value, input.SurfaceCore)
					if err != nil {
						b.Fatal(err)
					}
					prep, err := e.Prepare("update", data)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := e.Execute(context.Background(), prep, allowAll{}, ExecOptions{}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkOpsCached is BenchmarkOps on a serve-like engine whose cache is
// warm (the fixture files are old, so stat hits are trusted).
func BenchmarkOpsCached(b *testing.B) {
	for _, size := range perfSizes {
		e, err := New(perfRoot(b, size))
		if err != nil {
			b.Fatal(err)
		}
		e.Cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
		for _, op := range perfOps {
			b.Run(size+"/"+op, func(b *testing.B) {
				if _, err := perfOp(e, op); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := perfOp(e, op); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkAgentLoop: resume then append a note, as an agent does, on a
// private copy, without and with the serve cache.
func BenchmarkAgentLoop(b *testing.B) {
	for _, size := range perfSizes {
		for _, cached := range []bool{false, true} {
			b.Run(fmt.Sprintf("size=%s/cache=%v", size, cached), func(b *testing.B) {
				dir := filepath.Join(b.TempDir(), "root")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					b.Fatal(err)
				}
				if err := testutil.CopyTree(perfRoot(b, size), dir); err != nil {
					b.Fatal(err)
				}
				e, err := New(dir)
				if err != nil {
					b.Fatal(err)
				}
				if cached {
					e.Cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
				}
				b.ReportAllocs()
				// One untimed round warms the cache (steady state).
				for i := -1; i < b.N; i++ {
					if i == 0 {
						b.ResetTimer()
					}
					if _, err := perfOp(e, "resume"); err != nil {
						b.Fatal(err)
					}
					p, _ := ojson.Parse([]byte(`{"id":"perf-plan","appendNotes":["loop ` + strconv.Itoa(i) + `"]}`))
					data, err := input.ParseMutationInput("update", p.Value, input.SurfaceCore)
					if err != nil {
						b.Fatal(err)
					}
					prep, err := e.Prepare("update", data)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := e.Execute(context.Background(), prep, allowAll{}, ExecOptions{}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// TestColdCall is exec'd by TestBaselineMatrix as a fresh process: it
// times the first call only (the "cold first call" column).
func TestColdCall(t *testing.T) {
	op, root := os.Getenv("SHIORI_COLD_OP"), os.Getenv("SHIORI_COLD_ROOT")
	if op == "" {
		t.Skip("exec'd by TestBaselineMatrix")
	}
	start := time.Now()
	e, err := New(root)
	if err == nil {
		_, err = perfOp(e, op)
	}
	el := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("COLD_NS=%d\n", el.Nanoseconds())
}

type stat struct {
	Median float64 `json:"medianMs"`
	P95    float64 `json:"p95Ms"`
}

func summarize(ms []float64) stat {
	s := append([]float64(nil), ms...)
	sort.Float64s(s)
	median := s[len(s)/2]
	if len(s)%2 == 0 {
		median = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	rank := int(0.95*float64(len(s))+0.999999) - 1 // nearest rank
	if rank < 0 {
		rank = 0
	}
	if rank >= len(s) {
		rank = len(s) - 1
	}
	return stat{median, s[rank]}
}

var rssRE = regexp.MustCompile(`(\d+)\s+maximum resident set size`)

// TestBaselineMatrix measures the docs/baseline.md matrix for Go when
// SHIORI_BASELINE=<output.json> is set. Same method as the reference:
// warm = 2 unrecorded + 100/30/10 recorded in-process calls; cold = fresh
// processes (15, or 5 for 10 MiB), in-process first-call time, whole
// process wall time and peak RSS from /usr/bin/time -l.
func TestBaselineMatrix(t *testing.T) {
	out := os.Getenv("SHIORI_BASELINE")
	if out == "" {
		t.Skip("set SHIORI_BASELINE=<results.json>")
	}
	tmp := t.TempDir()
	testBin := filepath.Join(tmp, "engine.test")
	cliBin := filepath.Join(tmp, "shiori")
	for _, c := range [][]string{
		{"test", "-c", "-o", testBin, "."},
		{"build", "-trimpath", "-o", cliBin, "../../cmd/shiori"},
	} {
		cmd := exec.Command("go", c...)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", c, err, b)
		}
	}
	type row struct {
		Size          string  `json:"size"`
		Op            string  `json:"op"`
		Warm          stat    `json:"warm"`
		ColdFirstCall stat    `json:"coldFirstCall"`
		ColdTestProc  stat    `json:"coldTestProcessWall"`
		ColdCLI       stat    `json:"coldCliProcessWall"`
		RSSMedianMiB  float64 `json:"peakRssCliMedianMiB"`
		RSSMaxMiB     float64 `json:"peakRssCliMaxMiB"`
		OutputChars   int     `json:"outputChars"`
	}
	var rows []row
	for _, size := range perfSizes {
		root := perfRoot(t, size)
		warmN, coldN := 100, 15
		switch size {
		case "perf-1m":
			warmN = 30
		case "perf-10m":
			warmN, coldN = 10, 5
		}
		e, err := New(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range perfOps {
			r := row{Size: size, Op: op}
			var warm []float64
			for i := 0; i < warmN+2; i++ {
				start := time.Now()
				text, err := perfOp(e, op)
				el := time.Since(start)
				if err != nil {
					t.Fatal(err)
				}
				if i >= 2 {
					warm = append(warm, float64(el.Nanoseconds())/1e6)
				}
				r.OutputChars = ojson.UTF16Len(text)
			}
			r.Warm = summarize(warm)
			var first, procWall, cliWall, rss []float64
			for i := 0; i < coldN; i++ {
				cmd := exec.Command(testBin, "-test.run", "^TestColdCall$")
				cmd.Env = append(os.Environ(), "SHIORI_COLD_OP="+op, "SHIORI_COLD_ROOT="+root)
				start := time.Now()
				b, err := cmd.Output()
				procWall = append(procWall, float64(time.Since(start).Nanoseconds())/1e6)
				if err != nil {
					t.Fatalf("cold call: %v %s", err, b)
				}
				m := regexp.MustCompile(`COLD_NS=(\d+)`).FindSubmatch(b)
				ns, _ := strconv.ParseFloat(string(m[1]), 64)
				first = append(first, ns/1e6)

				args := []string{"-l", cliBin, op, "perf-plan", "--root", root, "--json"}
				cmd = exec.Command("/usr/bin/time", args...)
				var stderr bytes.Buffer
				cmd.Stdout = io.Discard
				cmd.Stderr = &stderr
				start = time.Now()
				if err := cmd.Run(); err != nil {
					t.Fatalf("cli: %v %s", err, stderr.String())
				}
				cliWall = append(cliWall, float64(time.Since(start).Nanoseconds())/1e6)
				if mm := rssRE.FindStringSubmatch(stderr.String()); mm != nil {
					v, _ := strconv.ParseFloat(mm[1], 64)
					rss = append(rss, v/(1<<20))
				}
			}
			r.ColdFirstCall = summarize(first)
			r.ColdTestProc = summarize(procWall)
			r.ColdCLI = summarize(cliWall)
			if len(rss) > 0 {
				r.RSSMedianMiB = summarize(rss).Median
				sort.Float64s(rss)
				r.RSSMaxMiB = rss[len(rss)-1]
			}
			rows = append(rows, r)
			t.Logf("%-9s %-8s warm %.2f/%.2f ms  cold first %.2f/%.2f  cli wall %.1f/%.1f  rss %.0f (%.0f) MiB  out %d",
				size, op, r.Warm.Median, r.Warm.P95, r.ColdFirstCall.Median, r.ColdFirstCall.P95,
				r.ColdCLI.Median, r.ColdCLI.P95, r.RSSMedianMiB, r.RSSMaxMiB, r.OutputChars)
		}
	}
	doc := map[string]any{
		"go":          runtime.Version(),
		"goos":        runtime.GOOS,
		"goarch":      runtime.GOARCH,
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"rows":        rows,
	}
	data, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
