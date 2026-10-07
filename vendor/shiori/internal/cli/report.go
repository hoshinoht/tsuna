package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// runReport prints a plan's status report (Markdown, or --json).
func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shiori report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root")
	jsonOut := fs.Bool("json", false, "machine output")
	commits := fs.Int("commits", 50, "commits from HEAD searched for the content evidence tested (0: none)")
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
	if len(positional) != 1 || *commits < 0 {
		fmt.Fprintln(stderr, "shiori report: usage: shiori report <id> [--commits N] [--json] [--root DIR]")
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
	v, md, err := e.Report(context.Background(), positional[0], *commits)
	if err != nil {
		return fail(stdout, stderr, *jsonOut, err)
	}
	if *jsonOut {
		fmt.Fprintln(stdout, string(ojson.Pretty(v)))
	} else {
		fmt.Fprint(stdout, md)
	}
	return 0
}
