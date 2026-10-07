package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// runPortfolio prints the cross-plan view built from plan links.
func runPortfolio(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shiori portfolio", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root")
	jsonOut := fs.Bool("json", false, "machine output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "shiori portfolio: usage: shiori portfolio [--json] [--root DIR]")
		return 2
	}
	rootDir := *root
	if rootDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fail(stdout, stderr, *jsonOut, err)
		}
		rootDir = wd
	}
	e, err := engine.New(rootDir)
	if err != nil {
		return fail(stdout, stderr, *jsonOut, err)
	}
	v, ok, err := e.Portfolio()
	if err != nil {
		return fail(stdout, stderr, *jsonOut, err)
	}
	if *jsonOut {
		if !ok {
			v = ojson.NewObject(1).Set("plans", ojson.ArrayValue([]ojson.Value{})).Value()
		}
		fmt.Fprintln(stdout, string(ojson.Pretty(v)))
		return 0
	}
	if !ok {
		fmt.Fprintln(stdout, "No plan records links to other plans (workplan_update planLinks).")
		return 0
	}
	plans, _ := v.Get("plans")
	for _, p := range plans.Elems() {
		get := func(k string) ojson.Value { x, _ := p.Get(k); return x }
		line := fmt.Sprintf("%-28s %-11s %s/%s", get("id").Str(), get("status").Str(), get("stepsCompleted").NumberLiteral(), get("steps").NumberLiteral())
		var w []string
		for _, x := range get("waitingOn").Elems() {
			w = append(w, x.Str())
		}
		if len(w) > 0 {
			line += "  waiting on " + strings.Join(w, ", ")
		}
		fmt.Fprintln(stdout, line)
	}
	if c, ok := v.Get("cycle"); ok {
		var parts []string
		for _, x := range c.Elems() {
			parts = append(parts, x.Str())
		}
		fmt.Fprintln(stdout, "cycle:", strings.Join(parts, " → "))
	}
	is, _ := v.Get("issues")
	for _, x := range is.Elems() {
		fmt.Fprintln(stdout, "issue:", x.Str())
	}
	return 0
}
