package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/gitview"
	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// runEvidence records one evidence record through workplan_update. With
// "-- COMMAND ARGS..." it runs the command in the project root, binds the
// record to the tree from before the run and records its real exit code
// and output digest (source cli-run); otherwise --command/--exit-code are
// the operator's assertion (source cli).
func runEvidence(args []string, stdout, stderr io.Writer) int {
	var argv []string
	for i, a := range args {
		if a == "--" {
			args, argv = args[:i], args[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("shiori evidence", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root")
	jsonOut := fs.Bool("json", false, "machine output")
	yes := fs.Bool("yes", false, "confirm the printed intent without a prompt")
	expected := fs.String("expected-hash", "", "stateHash from the latest read")
	phase := fs.String("phase", "", "phase id")
	step := fs.String("step", "", "step id")
	command := fs.String("command", "", "the command that was run (without -- COMMAND)")
	exitCode := fs.Int("exit-code", 0, "its exit code (without -- COMMAND)")
	outputFile := fs.String("output-file", "", "file holding its output (only the sha256 is stored)")
	summary := fs.String("summary", "", "one-line result summary")
	lane := fs.String("lane", "", "lane the evidence is for (with -- COMMAND it runs in the lane checkout)")
	var scope stringsFlag
	fs.Var(&scope, "scope", "project-relative path the evidence covers (repeatable)")
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
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "shiori evidence: "+format+"\n", a...)
		return 2
	}
	switch {
	case len(positional) != 1:
		return usageErr("expected one plan id")
	case *phase == "" || *step == "":
		return usageErr("--phase and --step are required")
	case *expected == "":
		return usageErr("--expected-hash is required (the stateHash from the latest read)")
	case len(argv) > 0 && (set["command"] || set["exit-code"] || set["output-file"]):
		return usageErr("--command, --exit-code and --output-file describe a command that was already run; omit them with -- COMMAND")
	case len(argv) == 0 && (!set["command"] || !set["exit-code"]):
		return usageErr("pass -- COMMAND [ARGS...] to run it, or --command and --exit-code for one that was already run")
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
	e.Source = history.SourceCLI
	rec := ojson.NewObject(8).
		Set("phaseId", ojson.StringValue(*phase)).
		Set("stepId", ojson.StringValue(*step))
	if len(argv) == 0 {
		e.EvidenceSource = evidence.SourceCLI
		rec.Set("command", ojson.StringValue(*command)).Set("exitCode", ojson.IntValue(int64(*exitCode)))
		if set["output-file"] {
			data, err := os.ReadFile(*outputFile)
			if err != nil {
				return usageErr("%v", err)
			}
			sum := sha256.Sum256(data)
			rec.Set("outputDigest", ojson.StringValue(hex.EncodeToString(sum[:])))
		}
	} else {
		e.EvidenceSource = evidence.SourceCLIRun
		dir := e.Root
		if set["lane"] {
			if dir, err = e.LaneCheckout(positional[0], *lane); err != nil {
				return fail(stdout, stderr, *jsonOut, err)
			}
		}
		var rels []string
		for _, p := range scope {
			rel, err := snapshot.NormalizeSpecFile(e.Root, strings.TrimSuffix(strings.TrimSpace(p), "/"))
			if err != nil {
				return usageErr("--scope %s: %v", p, err)
			}
			rels = append(rels, rel)
		}
		// The tree the command runs against, before it can change it.
		tree, err := gitview.Snapshot(context.Background(), dir, snapshot.WorkplanDir, rels)
		if err != nil && !errors.Is(err, gitview.ErrNoGit) {
			return fail(stdout, stderr, *jsonOut, err)
		}
		e.EvidenceTree = tree
		code, digest, err := runRecorded(dir, argv, stderr)
		if err != nil {
			return fail(stdout, stderr, *jsonOut, err)
		}
		fmt.Fprintf(stderr, "shiori evidence: %s exited %d\n", argv[0], code)
		rec.Set("command", ojson.StringValue(shellJoin(argv))).
			Set("exitCode", ojson.IntValue(int64(code))).
			Set("outputDigest", ojson.StringValue(digest))
	}
	if set["summary"] {
		rec.Set("summary", ojson.StringValue(*summary))
	}
	if len(scope) > 0 {
		rec.Set("scope", ojson.StringsValue(scope))
	}
	if set["lane"] {
		rec.Set("lane", ojson.StringValue(*lane))
	}
	toolIn := ojson.NewObject(3).
		Set("id", ojson.StringValue(positional[0])).
		Set("expectedHash", ojson.StringValue(*expected)).
		Set("recordEvidence", ojson.ArrayValue([]ojson.Value{rec.Value()})).Value()
	return execMutation(e, "update", toolIn, *yes, *jsonOut, stdout, stderr)
}

// runRecorded runs argv in dir, streaming its combined output to w, and
// returns its exit code and the sha256 of that output.
func runRecorded(dir string, argv []string, w io.Writer) (int, string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	h := sha256.New()
	out := io.MultiWriter(h, w)
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
	default:
		return 0, "", err
	}
	return cmd.ProcessState.ExitCode(), hex.EncodeToString(h.Sum(nil)), nil
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// shellJoin renders argv as a POSIX shell command line.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if shellSafe.MatchString(a) {
			q[i] = a
		} else {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}
