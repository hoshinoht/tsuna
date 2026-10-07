package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// readAll runs every read operation for a plan and returns the texts.
func readAll(e *Engine, id string) []string {
	var out []string
	add := func(v ojson.Value, err error) {
		if err != nil {
			out = append(out, "ERR "+err.Error())
			return
		}
		out = append(out, string(ojson.Pretty(v)))
	}
	add(e.Read(input.ReadInput{ID: id}))
	add(e.Inspect(input.InspectInput{ID: id}))
	add(e.Validate(input.ValidateInput{ID: id}))
	v, text, err := e.Resume(input.ResumeInput{ID: id})
	_ = v
	if err != nil {
		text = "ERR " + err.Error()
	}
	out = append(out, text)
	add(e.Doctor(input.DoctorInput{ID: &id}))
	add(e.List(input.ListInput{}))
	return out
}

// TestCacheParityOnFixtures: a warm cache returns exactly what an uncached
// engine returns, for every read of every fixture plan.
func TestCacheParityOnFixtures(t *testing.T) {
	fixtures, _ := os.ReadDir(testutil.Testdata("fixtures"))
	n := 0
	for _, f := range fixtures {
		if !f.IsDir() {
			continue
		}
		root := testutil.NewRoot(t, f.Name())
		plain, err := New(root.Path)
		if err != nil {
			t.Fatal(err)
		}
		cached, _ := New(root.Path)
		cached.Cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
		cached.Cache.RacyWindow = 0
		ents, _ := os.ReadDir(filepath.Join(root.Path, ".opencode/workplan"))
		plans := 0
		for _, en := range ents {
			name := en.Name()
			if !strings.HasSuffix(name, ".json") || strings.Count(name, ".") != 1 {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			want := readAll(plain, id)
			for pass := 0; pass < 2; pass++ {
				got := readAll(cached, id)
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("%s/%s op %d pass %d differs with the cache:\n%s", f.Name(), id, i, pass, testutil.FirstDiff(got[i], want[i]))
					}
				}
			}
			n++
			plans++
		}
		if cached.Cache.Hits == 0 && plans > 0 {
			t.Fatalf("%s: warm passes never hit the cache", f.Name())
		}
	}
	if n < 10 {
		t.Fatalf("only %d plans checked", n)
	}
}

// TestCacheSeesEdits: every kind of change after a warm read is visible,
// including a same-size edit within the racy window.
func TestCacheSeesEdits(t *testing.T) {
	freezeClock(t)
	root := testutil.NewRoot(t, "full-valid")
	e, _ := New(root.Path)
	e.Cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
	hash := func() string {
		s, err := e.load("full-plan")
		if err != nil {
			t.Fatal(err)
		}
		return s.StateHash
	}
	fresh := func() string {
		s, err := snapshot.Load(root.Path, "full-plan", e.Limits)
		if err != nil {
			t.Fatal(err)
		}
		return s.StateHash
	}
	dir := filepath.Join(root.Path, ".opencode/workplan")
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{"full-plan.json", "full-plan.md", "full-plan.checkpoint.json"} {
		os.Chtimes(filepath.Join(dir, name), old, old)
	}
	if hash() != fresh() || hash() != fresh() {
		t.Fatal("warm hash differs")
	}
	if e.Cache.Hits == 0 {
		t.Fatal("no stat hit on unchanged, old files")
	}
	edits := []struct {
		name string
		edit func(p string)
	}{
		{"full-plan.md", func(p string) { b, _ := os.ReadFile(p); os.WriteFile(p, append(b, '\n'), 0o644) }},
		{"full-plan.checkpoint.json", func(p string) { os.Remove(p) }},
		{"full-plan.json", func(p string) {
			// Same size, mtime set back to the cached one: the inode
			// change time still differs.
			b, _ := os.ReadFile(p)
			st, _ := os.Stat(p)
			i := strings.Index(string(b), `"goal": "`) + len(`"goal": "`)
			if b[i] == 'a' {
				b[i] = 'b'
			} else {
				b[i] = 'a'
			}
			os.WriteFile(p, b, 0o644)
			os.Chtimes(p, st.ModTime(), st.ModTime())
		}},
	}
	for _, ed := range edits {
		ed.edit(filepath.Join(dir, ed.name))
		if h, f := hash(), fresh(); h != f {
			t.Fatalf("after editing %s the cached hash is stale", ed.name)
		}
	}
	// A file filled inside the racy window is never trusted on stat alone:
	// with only the Markdown new, each load hits one file fewer than once
	// the Markdown is old.
	p := filepath.Join(dir, "full-plan.md")
	os.WriteFile(p, []byte("racy\n"), 0o644)
	hits := func() int64 { before := e.Cache.Hits; hash(); return e.Cache.Hits - before }
	hits()
	racy := hits()
	os.Chtimes(p, old, old)
	hits() // refill with an old mtime
	if settled := hits(); settled != racy+1 {
		t.Fatalf("stat hits per load: %d while racy, %d after; want one more", racy, settled)
	}
}

func TestCacheEvictsWithinBudget(t *testing.T) {
	root := testutil.NewRoot(t, "large-paging")
	e, _ := New(root.Path)
	e.Cache = snapshot.NewCache(1 << 10)
	e.Cache.RacyWindow = 0
	for i := 0; i < 3; i++ {
		if _, err := e.load("big-plan"); err != nil {
			t.Fatal(err)
		}
	}
	if e.Cache.PlanHits != 0 {
		t.Fatal("a plan larger than the budget stayed cached")
	}
}

// TestCacheRememberedRenderingIsExact: after cached writes the next write
// skips the old-plan render, and (verifyPostHashes) every remembered
// classification equals a fresh one.
func TestCacheRememberedRenderingIsExact(t *testing.T) {
	freezeClock(t)
	for _, fx := range []string{"full-valid", "minimal-valid", "handwritten-md"} {
		root := testutil.NewRoot(t, fx)
		e, _ := New(root.Path)
		e.Cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
		ents, _ := os.ReadDir(filepath.Join(root.Path, ".opencode/workplan"))
		for _, en := range ents {
			name := en.Name()
			if !strings.HasSuffix(name, ".json") || strings.Count(name, ".") != 1 {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			for i := 0; i < 3; i++ {
				s, err := e.load(id)
				if err != nil {
					break
				}
				in := mustJSON(t, `{"id":"`+id+`","expectedHash":"`+s.StateHash+`","appendNotes":["n`+itoaT(i)+`"]}`)
				if _, err := runMutation(context.Background(), e, "update", in, allowAll{}); err != nil {
					t.Fatalf("%s/%s: %v", fx, id, err)
				}
				if _, err := e.Validate(input.ValidateInput{ID: id}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
