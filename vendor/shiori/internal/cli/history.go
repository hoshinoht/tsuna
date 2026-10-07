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

// runHistory prints a plan's change log (spec 06 X4).
func runHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shiori history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root")
	jsonOut := fs.Bool("json", false, "machine output")
	since := fs.String("since", "", "only writes after this stateHash")
	limit := fs.Int("limit", 20, "newest entries shown (0: all readable)")
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != 1 || *limit < 0 {
		fmt.Fprintln(stderr, "shiori history: usage: shiori history <id> [--since HASH] [--limit N] [--json] [--root DIR]")
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
	v, err := e.History(positional[0], *since, *limit)
	if err != nil {
		return fail(stdout, stderr, *jsonOut, err)
	}
	if *jsonOut {
		fmt.Fprintln(stdout, string(ojson.Pretty(v)))
		return 0
	}
	entries, _ := v.Get("entries")
	if len(entries.Elems()) == 0 {
		fmt.Fprintln(stdout, "No logged writes.")
	}
	if n, _ := v.Get("omitted"); n.NumberLiteral() != "0" {
		fmt.Fprintf(stdout, "(%s older entries not shown; --limit 0 shows all)\n", n.NumberLiteral())
	}
	for _, en := range entries.Elems() {
		get := func(k string) string { x, _ := en.Get(k); return x.Str() }
		seq, _ := en.Get("seq")
		var parts []string
		ch, _ := en.Get("changes")
		for _, c := range ch.Elems() {
			path, _ := c.Get("path")
			op, _ := c.Get("op")
			s := path.Str() + " " + op.Str()
			if to, ok := c.Get("to"); ok {
				from, _ := c.Get("from")
				s = path.Str() + " " + from.Str() + "→" + to.Str()
			}
			if n, ok := c.Get("count"); ok {
				s += " " + n.NumberLiteral()
			}
			parts = append(parts, s)
		}
		fmt.Fprintf(stdout, "#%s %s %-5s %-12s %s\n", seq.NumberLiteral(), get("at"), get("source"), get("op"), strings.Join(parts, "; "))
	}
	if is, _ := v.Get("issues"); len(is.Elems()) > 0 {
		for _, i := range is.Elems() {
			fmt.Fprintln(stdout, "issue:", i.Str())
		}
	}
	if cur, ok := v.Get("currentStateHash"); ok && len(entries.Elems()) > 0 {
		last := entries.Elems()[len(entries.Elems())-1]
		after, _ := last.Get("after")
		if sh, ok := after.Get("stateHash"); !ok || sh.Str() != cur.Str() {
			fmt.Fprintln(stdout, "The plan changed outside Shiori after the last logged write.")
		}
	}
	return 0
}
