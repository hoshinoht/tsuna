package lanes

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

func TestTrieOverlaps(t *testing.T) {
	tr := &Trie{}
	for _, c := range []struct{ path, lane string }{{"internal/api", "a"}, {"go.mod", "a"}, {"internal/apiv2", "b"}, {"internal/api/x.go", "a"}} {
		if cf := tr.Insert(c.path, c.lane); cf != nil {
			t.Fatalf("%s: unexpected conflict %+v", c.path, cf)
		}
	}
	cases := map[string]Conflict{
		"internal/api/handler.go": {Path: "internal/api/handler.go", By: "c", Lane: "a", With: "internal/api"},
		"internal":                {Path: "internal", By: "c", Lane: "a", With: "internal/api"},
		"go.mod":                  {Path: "go.mod", By: "c", Lane: "a", With: "go.mod"},
	}
	for p, want := range cases {
		cf := tr.Insert(p, "c")
		if cf == nil || *cf != want {
			t.Errorf("%s: %+v want %+v", p, cf, want)
		}
	}
	if !Covers([]string{"internal/api"}, "internal/api/x.go") || Covers([]string{"internal/api"}, "internal/apiv2/x.go") {
		t.Fatal("Covers")
	}
}

func TestTransitions(t *testing.T) {
	ok := [][2]string{{Claimed, Prepared}, {Prepared, Running}, {Running, Review}, {Review, Running}, {Review, Integrating}, {Integrating, Merged}, {Running, Abandoned}}
	for _, c := range ok {
		if !CanTransition(c[0], c[1]) {
			t.Errorf("%s -> %s refused", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{Claimed, Running}, {Merged, Running}, {Abandoned, Claimed}, {Running, Merged}} {
		if CanTransition(c[0], c[1]) {
			t.Errorf("%s -> %s allowed", c[0], c[1])
		}
	}
}

func TestMergeOrder(t *testing.T) {
	l := &Ledger{Lanes: []Lane{{ID: "c", State: Review}, {ID: "a", State: Running}, {ID: "old", State: Merged}, {ID: "b", State: Integrating}, {ID: "x", State: Running}, {ID: "y", State: Running}}}
	order, cycle := l.MergeOrder(map[string][]string{"c": {"b"}, "b": {"a", "old"}, "x": {"y"}, "y": {"x"}})
	if !reflect.DeepEqual(order, []string{"a", "b", "c"}) || !reflect.DeepEqual(cycle, []string{"x", "y"}) {
		t.Fatalf("order %v cycle %v", order, cycle)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	oid := strings.Repeat("a", 40)
	br := "lane/api"
	l := &Ledger{ID: "p", UpdatedAt: "2026-01-02T03:04:05Z", Lanes: []Lane{{
		ID: "api", State: Prepared, Steps: []model.StepRef{{PhaseID: "p1", StepID: "s1"}}, Claims: []string{"internal/api"},
		Baseline: Baseline{TreeOID: &oid, Head: &oid, Dirty: 2}, Checkout: &Checkout{Path: "/w/api", Branch: &br},
		History:   []Event{{State: Claimed, At: "2026-01-02T03:04:05Z", Source: "agent"}, {State: Prepared, At: "2026-01-02T03:04:05Z", Source: "cli"}},
		CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z",
	}, {ID: "shared", State: Claimed, Steps: []model.StepRef{{PhaseID: "p1", StepID: "s2"}}, CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z"}}}
	data := Encode(l)
	p, _ := ojson.Parse(data)
	got, issues := Decode(p.Value)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	if string(Encode(got)) != string(data) {
		t.Fatalf("round trip:\n%s", data)
	}
	bad := strings.Replace(string(data), `"laneId": "shared",
      "state": "claimed"`, `"laneId": "shared",
      "state": "done"`, 1)
	p, _ = ojson.Parse([]byte(bad))
	if _, issues := Decode(p.Value); len(issues) == 0 || !strings.Contains(issues[0], "lanes.1.state") {
		t.Fatalf("issues %v", issues)
	}
}
