package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/gitview"
	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// runVerify re-runs the recorded commands of steps whose evidence is
// stale or failing and records the results. Only commands the operator
// ran through `shiori evidence -- COMMAND` are repeated, after the
// operator confirms the exact list.
func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shiori verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root")
	jsonOut := fs.Bool("json", false, "machine output")
	yes := fs.Bool("yes", false, "run the listed commands without a prompt")
	all := fs.Bool("all", false, "every step with recorded commands, not only stale or failing ones")
	dryRun := fs.Bool("dry-run", false, "list the commands only")
	var steps stringsFlag
	fs.Var(&steps, "step", "PHASE/STEP to verify (repeatable)")
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
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "shiori verify: usage: shiori verify <id> [--all] [--step PHASE/STEP]... [--dry-run] [--yes] [--json] [--root DIR]")
		return 2
	}
	id := positional[0]
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
	e.Source = history.SourceCLI
	runs, skipped, err := e.EvidenceReruns(id, *all || len(steps) > 0)
	if err != nil {
		return fail(stdout, stderr, *jsonOut, err)
	}
	if len(steps) > 0 {
		want := map[string]bool{}
		for _, s := range steps {
			want[s] = true
		}
		var kept []engine.Rerun
		for _, r := range runs {
			if want[r.PhaseID+"/"+r.StepID] {
				kept = append(kept, r)
			}
		}
		runs = kept
	}
	for _, s := range skipped {
		fmt.Fprintln(stderr, "shiori verify: not re-run (only commands run through shiori evidence -- COMMAND are):", s)
	}
	if len(runs) == 0 {
		if *jsonOut {
			fmt.Fprintln(stdout, string(ojson.Pretty(ojson.NewObject(1).Set("results", ojson.ArrayValue([]ojson.Value{})).Value())))
		} else {
			fmt.Fprintln(stdout, "Nothing to re-run.")
		}
		return 0
	}
	fmt.Fprintf(stderr, "Commands to re-run in %s:\n", e.Root)
	for _, r := range runs {
		where := ""
		if r.Lane != nil {
			where = " (in lane " + *r.Lane + ")"
		}
		fmt.Fprintf(stderr, "  %s/%s [%s]%s: %s\n", r.PhaseID, r.StepID, r.State, where, r.Command)
	}
	if *dryRun {
		return 0
	}
	if !*yes {
		if !IsTerminal() {
			fmt.Fprintln(stderr, "shiori verify: not a terminal; pass --yes to run exactly these commands")
			return 1
		}
		fmt.Fprint(stderr, "Type yes to run these commands and record their results: ")
		line, _ := bufio.NewReader(Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			return fail(stdout, stderr, *jsonOut, engine.ErrDenied)
		}
	}
	var results []ojson.Value
	failed := false
	for _, r := range runs {
		res, err := rerun(e, id, r, stderr)
		if err != nil {
			return fail(stdout, stderr, *jsonOut, err)
		}
		if c, _ := res.Get("exitCode"); c.NumberLiteral() != "0" {
			failed = true
		}
		results = append(results, res)
		if !*jsonOut {
			c, _ := res.Get("exitCode")
			fmt.Fprintf(stdout, "%s/%s: %s → exit %s (was %s)\n", r.PhaseID, r.StepID, r.Command, c.NumberLiteral(), r.State)
		}
	}
	if *jsonOut {
		fmt.Fprintln(stdout, string(ojson.Pretty(ojson.NewObject(1).Set("results", ojson.ArrayValue(results)).Value())))
	}
	if failed {
		return 1
	}
	return 0
}

// rerun runs one recorded command against the current tree and records
// the result through workplan_update.recordEvidence.
func rerun(e *engine.Engine, id string, r engine.Rerun, stderr io.Writer) (ojson.Value, error) {
	argv, err := shellSplit(r.Command)
	if err != nil {
		return ojson.Value{}, fmt.Errorf("%s/%s: cannot re-run %q: %v", r.PhaseID, r.StepID, r.Command, err)
	}
	dir := e.Root
	if r.Lane != nil {
		if dir, err = e.LaneCheckout(id, *r.Lane); err != nil {
			return ojson.Value{}, err
		}
	}
	tree, err := gitview.Snapshot(context.Background(), dir, snapshot.WorkplanDir, r.Scope)
	if err != nil && !errors.Is(err, gitview.ErrNoGit) {
		return ojson.Value{}, err
	}
	e.EvidenceTree, e.EvidenceSource = tree, evidence.SourceCLIRun
	code, digest, err := runRecorded(dir, argv, stderr)
	if err != nil {
		return ojson.Value{}, err
	}
	hash, err := e.CurrentStateHash(id)
	if err != nil {
		return ojson.Value{}, err
	}
	rec := ojson.NewObject(6).
		Set("phaseId", ojson.StringValue(r.PhaseID)).
		Set("stepId", ojson.StringValue(r.StepID)).
		Set("command", ojson.StringValue(r.Command)).
		Set("exitCode", ojson.IntValue(int64(code))).
		Set("outputDigest", ojson.StringValue(digest))
	if len(r.Scope) > 0 {
		rec.Set("scope", ojson.StringsValue(r.Scope))
	}
	if r.Lane != nil {
		rec.Set("lane", ojson.StringValue(*r.Lane))
	}
	in := ojson.NewObject(3).
		Set("id", ojson.StringValue(id)).
		Set("expectedHash", ojson.StringValue(hash)).
		Set("recordEvidence", ojson.ArrayValue([]ojson.Value{rec.Value()})).Value()
	data, err := input.ParseMutationInput("update", in, input.SurfaceCore)
	if err != nil {
		return ojson.Value{}, err
	}
	prep, err := e.Prepare("update", data)
	if err != nil {
		return ojson.Value{}, err
	}
	// The operator confirmed the command list; the intent is still shown.
	if _, err := e.Execute(context.Background(), prep, &CLIAuthorizer{Yes: true, Out: stderr}, engine.ExecOptions{}); err != nil {
		return ojson.Value{}, err
	}
	return ojson.NewObject(5).
		Set("phaseId", ojson.StringValue(r.PhaseID)).
		Set("stepId", ojson.StringValue(r.StepID)).
		Set("command", ojson.StringValue(r.Command)).
		Set("previousState", ojson.StringValue(r.State)).
		Set("exitCode", ojson.IntValue(int64(code))).Value(), nil
}

// shellSplit inverts shellJoin: words separated by spaces, each plain or
// single-quoted (an embedded quote is closed, escaped and reopened).
// Anything else is refused.
func shellSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted:
			if c == '\'' {
				quoted = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(s) && s[i+1] == '\'':
			cur.WriteByte('\'')
			i++
			inWord = true
		case c == ' ':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case shellSafe.MatchString(string(c)):
			cur.WriteByte(c)
			inWord = true
		default:
			return nil, fmt.Errorf("unexpected %q (not a command shiori recorded)", c)
		}
	}
	if quoted {
		return nil, errors.New("unterminated quote")
	}
	if inWord {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		return nil, errors.New("empty command")
	}
	return out, nil
}
