// Package cli implements the standalone `shiori` command. It runs the same
// engine in-process (no child, no protocol) under local operator
// authority. Reads never prompt or write; mutations (mutate.go) print the
// prepared intent and require confirmation before committing it.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Version is the CLI version (set with -ldflags at build time).
var Version = "0.0.0-stage-d"

const usage = `shiori — workplan core (stage D)

Usage:
  shiori <command> [flags] [id]

Read commands (never prompt, never write):
  list                       List plans and classified sidecars
  read <id>                  Read a plan (normalized JSON, selection, Markdown)
  inspect <id>               Page stable phase/step ids and Markdown markers
  validate <id>              Validate structure, links, dependencies, recovery state
  resume <id>                Bounded continuation packet (UTF-16 budget)
  doctor [id]                Read-only diagnostics: roots, sidecars, locks, journals
  compact <id> --reason R    Compaction preview (read-only without --apply)
  history <id>               The plan's change log [--since HASH --limit N] (spec 06 X4)
  report <id>                Status report in Markdown (--json): progress, open work, evidence and
                             the commits it verified [--commits N], findings, lanes, recent activity
  portfolio                  Plans and the plans they wait on, from planLinks (spec 06 X5)

Mutating commands (print the prepared intent, then require confirmation):
  create <id>                --goal G [--title --kind --status --plan-file --markdown-file F
                             --append-note N --overwrite --replace-markdown
                             --template feature|bugfix|migration]
  update <id>                [--title --goal --status --plan-file --markdown-file F
                             --append-note N --replace-markdown --rebase] | --recovery resume|rollback
  patch <id>                 --patch-file F [--validate]
  reset <id>                 [--mode draft|markdown-only|wipe --replace-markdown]
                             draft: statuses to draft, checkpoint removed, content kept;
                             wipe [--preserve-notes]: preview (read-only), then apply with
                             --preview-token T --confirm WIPE_PLAN_CONTENT (archives first)
  checkpoint <id>            --summary S --next-action A [--phase --step --blocker
                             --guardrail --reference --validation --append-validation V]
                             replaces the whole checkpoint; --merge keeps every omitted
                             field (then --summary/--next-action are optional)
  compact <id> --apply       --reason R [--archive-phase ID --archive-note I --archive-finding I
                             | --rollover [--keep-notes N] [--pin-note I]...]
                             --preview-token T --confirm ARCHIVE_SELECTED_HISTORY
  evidence <id>              --phase P --step S --expected-hash H [--scope PATH]... [--summary S] [--lane L]
                             (-- COMMAND [ARGS...] | --command C --exit-code N [--output-file F])
                             records evidence for a step (spec 06 X2); with -- COMMAND it runs
                             the command in the root (or the lane's checkout) and records its
                             exit code and output digest
  verify <id>                Re-run the commands recorded with evidence -- COMMAND for stale or
                             failing steps and record the results [--all --step P/S --dry-run]
  mcp [--root DIR]           The workplan tools as an MCP server on stdin/stdout (register it as
                             "workplan" so tools appear as workplan_resume, ...); writes are
                             approved through MCP elicitation or the client's tool approval
                             (--write-approval auto|elicitation|client|deny)
  serve --stdio              Native adapter protocol on stdin/stdout (JSON lines;
                             prepare -> host authorization -> commit; idle exit)
  version                    Print the version

Common flags:
  --root DIR                 Project root (default: current directory)
  --json                     Machine output: the tool result object only, on stdout
  --input JSON               Raw core-surface tool input object (flags override its fields)

Mutation flags:
  --expected-hash H          stateHash from the latest read (required for existing-state
                             writes, including recovery and compaction apply)
  --legacy-unhashed          Allow an existing-state write without --expected-hash
                             (still rechecked under the lock)
  --journal-version 1|2      Journal format (default 2: images by reference; 1: inline,
                             readable by the reference plugin). Also on serve.
  --yes                      Confirm the printed intent (required off a terminal;
                             never read from the environment)

Read command flags:
  read:     --phase ID --step ID --no-markdown --markdown --notes
  inspect:  --phase ID --limit N --cursor TOKEN
  resume:   --max-chars N --limit N --cursor TOKEN --phase ID --step ID
  doctor:   --limit N
  resume, doctor: --compaction-advice off|key=N,... (D.4 advisor thresholds:
            min-savings-kib, notes, terminal-percent, plan-kib, keep-notes)

Exit status: 0 success; 1 operation error, refusal, or (validate) an invalid
plan; 2 usage error.
`

// Run executes the CLI and returns the process exit status.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdout, "shiori", Version)
		return 0
	case "serve":
		return runServe(rest, os.Stdin, stdout, stderr)
	case "evidence":
		return runEvidence(rest, stdout, stderr)
	case "mcp":
		return runMCP(rest, os.Stdin, stdout, stderr)
	case "history":
		return runHistory(rest, stdout, stderr)
	case "verify":
		return runVerify(rest, stdout, stderr)
	case "report":
		return runReport(rest, stdout, stderr)
	case "portfolio":
		return runPortfolio(rest, stdout, stderr)
	case "list", "read", "inspect", "validate", "resume", "doctor":
	default:
		if mutationCommands[cmd] {
			return runMutationCommand(cmd, rest, stdout, stderr)
		}
		fmt.Fprintf(stderr, "shiori: unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	fs := flag.NewFlagSet("shiori "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	rawInput := fs.String("input", "", "raw tool input JSON")
	phase := fs.String("phase", "", "phase id")
	step := fs.String("step", "", "step id")
	noMD := fs.Bool("no-markdown", false, "omit linked Markdown")
	withMD := fs.Bool("markdown", false, "include linked Markdown in a filtered read")
	withNotes := fs.Bool("notes", false, "include findings and notes in a filtered read")
	limit := fs.Int("limit", 0, "page size")
	cursor := fs.String("cursor", "", "cursor from the previous page")
	maxChars := fs.Int("max-chars", 0, "resume budget in UTF-16 code units")
	advice := fs.String("compaction-advice", "", "compaction advisor thresholds: off, or key=value pairs (D.4)")
	var positional []string
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// Build the core-surface tool input object.
	var toolIn ojson.Value
	if *rawInput != "" {
		p, err := ojson.Parse([]byte(*rawInput))
		if err != nil {
			fmt.Fprintf(stderr, "shiori: --input is not valid JSON: %v\n", err)
			return 2
		}
		toolIn = p.Value
	} else {
		b := ojson.NewObject(8)
		if len(positional) > 1 {
			fmt.Fprintf(stderr, "shiori %s: expected at most one id, got %d arguments\n", cmd, len(positional))
			return 2
		}
		if len(positional) == 1 {
			if cmd == "list" {
				fmt.Fprintln(stderr, "shiori list: takes no id")
				return 2
			}
			b.Set("id", ojson.StringValue(positional[0]))
		}
		str := func(flagName, key string, v string) {
			if set[flagName] {
				b.Set(key, ojson.StringValue(v))
			}
		}
		num := func(flagName, key string, v int) {
			if set[flagName] {
				b.Set(key, ojson.IntValue(int64(v)))
			}
		}
		switch cmd {
		case "read":
			str("phase", "phaseId", *phase)
			str("step", "stepId", *step)
			if *noMD && *withMD {
				fmt.Fprintln(stderr, "shiori read: --markdown and --no-markdown are mutually exclusive")
				return 2
			}
			if *noMD {
				b.Set("includeMarkdown", ojson.BoolValue(false))
			}
			if *withMD {
				b.Set("includeMarkdown", ojson.BoolValue(true))
			}
			if *withNotes {
				b.Set("includeNotes", ojson.BoolValue(true))
			}
		case "inspect":
			str("phase", "phaseId", *phase)
			num("limit", "limit", *limit)
			str("cursor", "cursor", *cursor)
		case "resume":
			num("max-chars", "maxChars", *maxChars)
			num("limit", "limit", *limit)
			str("cursor", "cursor", *cursor)
			str("phase", "phaseId", *phase)
			str("step", "stepId", *step)
		case "doctor":
			num("limit", "limit", *limit)
		}
		for name := range set {
			if !flagAllowed(cmd, name) {
				fmt.Fprintf(stderr, "shiori %s: flag --%s does not apply\n", cmd, name)
				return 2
			}
		}
		toolIn = b.Value()
	}

	// The root is trusted operator context (flag, or workspaceRoot in the
	// core-surface input), never model input on the native surface.
	rootDir := *root
	if wr, ok := toolIn.Get("workspaceRoot"); ok && wr.Kind() == ojson.String && rootDir == "" {
		rootDir = wr.Str()
	}
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
	if set["compaction-advice"] {
		th, err := advisor.ParseThresholds(*advice)
		if err != nil {
			fmt.Fprintln(stderr, "shiori "+cmd+":", err)
			return 2
		}
		e.Compaction = th
	}

	res, text, err := dispatch(e, cmd, toolIn)
	if err != nil {
		return fail(stdout, stderr, *jsonOut, err)
	}
	if *jsonOut {
		if text == "" {
			text = string(ojson.Pretty(res))
		}
		fmt.Fprintln(stdout, text)
	} else {
		human(stdout, cmd, res)
	}
	if cmd == "validate" {
		if v, ok := res.Get("valid"); ok && !v.Bool() {
			return 1
		}
	}
	return 0
}

func flagAllowed(cmd, name string) bool {
	switch name {
	case "root", "json", "input":
		return true
	case "compaction-advice": // trusted operator flag
		return cmd == "resume" || cmd == "doctor"
	}
	allowed := map[string][]string{
		"read":    {"phase", "step", "no-markdown", "markdown", "notes"},
		"inspect": {"phase", "limit", "cursor"},
		"resume":  {"max-chars", "limit", "cursor", "phase", "step"},
		"doctor":  {"limit"},
	}
	for _, a := range allowed[cmd] {
		if a == name {
			return true
		}
	}
	return false
}

// dispatch validates input on the core surface and runs the operation.
// text is set when the operation defines its own serialization (resume).
func dispatch(e *engine.Engine, cmd string, raw ojson.Value) (ojson.Value, string, error) {
	s := input.SurfaceCore
	switch cmd {
	case "list":
		in, err := input.ParseListInput(raw, s)
		if err != nil {
			return ojson.Value{}, "", err
		}
		v, err := e.List(in)
		return v, "", err
	case "read":
		in, err := input.ParseReadInput(raw, s)
		if err != nil {
			return ojson.Value{}, "", err
		}
		v, err := e.Read(in)
		return v, "", err
	case "inspect":
		in, err := input.ParseInspectInput(raw, s)
		if err != nil {
			return ojson.Value{}, "", err
		}
		v, err := e.Inspect(in)
		return v, "", err
	case "validate":
		in, err := input.ParseValidateInput(raw, s)
		if err != nil {
			return ojson.Value{}, "", err
		}
		v, err := e.Validate(in)
		return v, "", err
	case "resume":
		in, err := input.ParseResumeInput(raw, s)
		if err != nil {
			return ojson.Value{}, "", err
		}
		return e.Resume(in)
	default:
		in, err := input.ParseDoctorInput(raw, s)
		if err != nil {
			return ojson.Value{}, "", err
		}
		v, err := e.Doctor(in)
		return v, "", err
	}
}

// ErrorClass maps an error to a protocol error class
// (protocol-envelope-v1 errorClass); see engine.ErrorClass.
func ErrorClass(err error) string { return engine.ErrorClass(err) }

func fail(stdout, stderr io.Writer, jsonOut bool, err error) int {
	if !jsonOut {
		fmt.Fprintln(stderr, "shiori:", err)
		return 1
	}
	b := ojson.NewObject(3).
		Set("class", ojson.StringValue(ErrorClass(err))).
		Set("message", ojson.StringValue(err.Error()))
	var issues []model.Issue
	var ie *input.InputError
	var de *model.DecodeError
	var gate interface{ StructuredIssues() []model.Issue } // plan and step status gates
	switch {
	case errors.As(err, &ie):
		issues = ie.Issues
	case errors.As(err, &de):
		issues = de.Issues
	case errors.As(err, &gate):
		issues = gate.StructuredIssues()
	}
	if len(issues) > 0 {
		out := make([]ojson.Value, len(issues))
		for i, is := range issues {
			p := is.PathString()
			if p == "" {
				p = "$"
			}
			out[i] = ojson.NewObject(2).Set("path", ojson.StringValue(p)).Set("message", ojson.StringValue(is.Message)).Value()
		}
		b.Set("issues", ojson.ArrayValue(out))
	}
	v := ojson.NewObject(2).Set("ok", ojson.BoolValue(false)).Set("error", b.Value()).Value()
	fmt.Fprintln(stdout, string(ojson.Pretty(v)))
	return 1
}

// itoa is a small helper for human output.
func itoa(v ojson.Value) string {
	if f, ok := v.Float(); ok {
		return strconv.FormatInt(int64(f), 10)
	}
	return v.Str()
}
