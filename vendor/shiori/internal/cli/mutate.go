package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/evidence"
	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
)

// Standalone mutation confirmation. The CLI acts under
// local operator authority: it prints the prepared intent and requires
// typing "yes" on a TTY, or --yes off a TTY. --yes is a flag only (never
// an environment variable or config) and never bypasses stale-hash, lock,
// journal or scope checks.

// Stdin and IsTerminal are the prompt source (replaceable in tests).
var (
	Stdin      io.Reader = os.Stdin
	IsTerminal           = func() bool { return isatty(os.Stdin.Fd()) }
)

var mutationCommands = map[string]bool{"create": true, "update": true, "patch": true, "reset": true, "checkpoint": true, "compact": true}

// stringsFlag collects a repeatable flag.
type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

// CLIAuthorizer prompts on the terminal or honours --yes.
type CLIAuthorizer struct {
	Yes bool
	TTY bool
	In  io.Reader
	Out io.Writer // intent display (stderr)
}

// Authorize prints the exact intent and asks for confirmation.
func (a *CLIAuthorizer) Authorize(ctx context.Context, req engine.AuthRequest) error {
	printIntent(a.Out, req)
	if a.Yes {
		return nil
	}
	if !a.TTY {
		return fmt.Errorf("%w (not a terminal; pass --yes to confirm this exact intent)", engine.ErrDenied)
	}
	fmt.Fprint(a.Out, "Type yes to apply this mutation: ")
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(a.In).ReadString('\n')
		line <- s
	}()
	select {
	case <-ctx.Done():
		return &storage.CancelledError{Stage: "authorization"}
	case s := <-line:
		if strings.TrimSpace(s) != "yes" {
			return engine.ErrDenied
		}
	}
	return nil
}

func printIntent(w io.Writer, req engine.AuthRequest) {
	in := req.Intent
	fmt.Fprintf(w, "Prepared %s (%s) for workplan %s\n", req.Tool, in.Operation, in.WorkplanID)
	fmt.Fprintf(w, "  root:   %s\n  intent: %s\n", in.Root, req.Digest)
	for _, t := range in.Targets {
		b, a := t.BeforeHash(), t.AfterHash()
		if b == "" {
			b = "absent"
		}
		if a == "" {
			a = "delete"
		}
		fmt.Fprintf(w, "  %-12s %s\n               before %s\n               after  %s\n", t.Kind, t.Rel, b, a)
	}
	r := req.Resources
	if in.History != nil {
		fmt.Fprintf(w, "  history: %s (append)\n", in.History.Rel)
	}
	fmt.Fprintf(w, "  journal: %s\n", r.Journal)
	for _, l := range in.Locks {
		fmt.Fprintf(w, "  lock:    %s\n", l.Rel)
	}
	fmt.Fprintf(w, "  staging: %d file(s); lock-protocol paths: %d; directories: %d\n", len(r.Staging), len(r.Lock), len(r.Dirs))
}

func runMutationCommand(cmd string, rest []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shiori "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root")
	jsonOut := fs.Bool("json", false, "machine output")
	rawInput := fs.String("input", "", "raw core-surface tool input JSON")
	yes := fs.Bool("yes", false, "confirm the printed intent without a prompt")
	expected := fs.String("expected-hash", "", "stateHash from the latest read")
	legacy := fs.Bool("legacy-unhashed", false, "allow an existing-state write without --expected-hash (still rechecked under the lock)")
	journal := fs.Int("journal-version", 2, "journal format: 2 (by reference) or 1 (inline, reference-compatible)")
	// Field flags (the --input object may carry any field instead).
	title := fs.String("title", "", "")
	goal := fs.String("goal", "", "")
	kind := fs.String("kind", "", "")
	status := fs.String("status", "", "")
	planFile := fs.String("plan-file", "", "")
	mdFile := fs.String("markdown-file", "", "file with explicit linked Markdown")
	overwrite := fs.Bool("overwrite", false, "")
	replaceMD := fs.Bool("replace-markdown", false, "")
	template := fs.String("template", "", "create: start from a plan template (feature, bugfix, migration)")
	rebase := fs.Bool("rebase", false, "update: apply over newer non-conflicting writes when --expected-hash is stale (X4)")
	var notes, blockers, guardrails, refs, validations, appendValidations, phases, noteIdx, findingIdx, pinNotes stringsFlag
	fs.Var(&notes, "append-note", "")
	fs.Var(&blockers, "blocker", "")
	fs.Var(&guardrails, "guardrail", "")
	fs.Var(&refs, "reference", "")
	fs.Var(&validations, "validation", "")
	fs.Var(&phases, "archive-phase", "")
	fs.Var(&noteIdx, "archive-note", "")
	fs.Var(&findingIdx, "archive-finding", "")
	rollover := fs.Bool("rollover", false, "select notes by rollover (D.4)")
	keepNotes := fs.Int("keep-notes", 0, "rollover: newest notes that stay (default 20)")
	fs.Var(&pinNotes, "pin-note", "rollover: note index to keep")
	recovery := fs.String("recovery", "", "resume|rollback")
	patchFile := fs.String("patch-file", "", "")
	validate := fs.Bool("validate", false, "")
	mode := fs.String("mode", "", "")
	preserveNotes := fs.Bool("preserve-notes", false, "")
	summary := fs.String("summary", "", "")
	merge := fs.Bool("merge", false, "checkpoint: keep the stored checkpoint's omitted fields (D.4.3)")
	fs.Var(&appendValidations, "append-validation", "checkpoint: line appended to recentValidation (D.4.3)")
	nextAction := fs.String("next-action", "", "")
	phase := fs.String("phase", "", "")
	step := fs.String("step", "", "")
	reason := fs.String("reason", "", "")
	apply := fs.Bool("apply", false, "")
	token := fs.String("preview-token", "", "")
	confirm := fs.String("confirm", "", "")
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
	allowed := map[string][]string{
		"create":     {"title", "goal", "kind", "status", "plan-file", "markdown-file", "overwrite", "replace-markdown", "append-note", "template"},
		"update":     {"title", "goal", "status", "plan-file", "markdown-file", "replace-markdown", "append-note", "recovery", "rebase"},
		"patch":      {"patch-file", "validate"},
		"reset":      {"mode", "preserve-notes", "replace-markdown", "preview-token", "confirm"},
		"checkpoint": {"summary", "next-action", "phase", "step", "blocker", "guardrail", "reference", "validation", "merge", "append-validation"},
		"compact":    {"reason", "archive-phase", "archive-note", "archive-finding", "apply", "preview-token", "confirm", "rollover", "keep-notes", "pin-note"},
	}
	for name := range set {
		switch name {
		case "root", "json", "input", "yes", "expected-hash", "legacy-unhashed", "journal-version":
			continue
		}
		ok := false
		for _, a := range allowed[cmd] {
			ok = ok || a == name
		}
		if !ok {
			fmt.Fprintf(stderr, "shiori %s: flag --%s does not apply\n", cmd, name)
			return 2
		}
	}
	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "shiori %s: "+format+"\n", append([]any{cmd}, a...)...)
		return 2
	}

	// Build the core-surface input object.
	var members []ojson.Member
	if *rawInput != "" {
		p, err := ojson.Parse([]byte(*rawInput))
		if err != nil || p.Value.Kind() != ojson.Object {
			return usageErr("--input must be a JSON object")
		}
		members = append(members, p.Value.Members()...)
	}
	put := func(k string, v ojson.Value) {
		for i := range members {
			if members[i].Key == k {
				members[i].Value = v
				return
			}
		}
		members = append(members, ojson.Member{Key: k, Value: v})
	}
	if len(positional) > 1 {
		return usageErr("expected at most one id, got %d arguments", len(positional))
	}
	if len(positional) == 1 {
		put("id", ojson.StringValue(positional[0]))
	}
	strFlag := func(flagName, key string, v string) {
		if set[flagName] {
			put(key, ojson.StringValue(v))
		}
	}
	listFlag := func(flagName, key string, vs []string) {
		if set[flagName] {
			put(key, ojson.StringsValue(vs))
		}
	}
	boolFlag := func(flagName, key string, v bool) {
		if set[flagName] {
			put(key, ojson.BoolValue(v))
		}
	}
	intsFlag := func(flagName, key string, vs []string) error {
		if !set[flagName] {
			return nil
		}
		out := make([]ojson.Value, len(vs))
		for i, s := range vs {
			n, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("--%s expects integers", flagName)
			}
			out[i] = ojson.IntValue(int64(n))
		}
		put(key, ojson.ArrayValue(out))
		return nil
	}
	strFlag("title", "title", *title)
	strFlag("goal", "goal", *goal)
	strFlag("kind", "kind", *kind)
	strFlag("status", "status", *status)
	strFlag("plan-file", "planFile", *planFile)
	if set["markdown-file"] {
		data, err := os.ReadFile(*mdFile)
		if err != nil {
			return usageErr("%v", err)
		}
		put("planMarkdown", ojson.StringValue(string(data)))
	}
	boolFlag("overwrite", "overwrite", *overwrite)
	boolFlag("replace-markdown", "replaceMarkdown", *replaceMD)
	boolFlag("rebase", "rebase", *rebase)
	if set["template"] {
		phases, ok := templatePhases(*template)
		if !ok {
			return usageErr("unknown template %q (templates: %s)", *template, templateNames())
		}
		for _, m := range members {
			if m.Key == "phases" {
				return usageErr("--template and phases in --input are exclusive")
			}
		}
		put("phases", phases)
		if !set["kind"] {
			put("kind", ojson.StringValue(*template))
		}
	}
	noteKey := "appendNotes"
	if cmd == "create" {
		noteKey = "notes"
	}
	listFlag("append-note", noteKey, notes)
	strFlag("recovery", "recovery", *recovery)
	if set["patch-file"] {
		data, err := os.ReadFile(*patchFile)
		if err != nil {
			return usageErr("%v", err)
		}
		put("patchText", ojson.StringValue(string(data)))
	}
	boolFlag("validate", "validate", *validate)
	strFlag("mode", "mode", *mode)
	boolFlag("preserve-notes", "preserveNotes", *preserveNotes)
	strFlag("summary", "summary", *summary)
	strFlag("next-action", "nextAction", *nextAction)
	strFlag("phase", "phaseId", *phase)
	strFlag("step", "stepId", *step)
	listFlag("blocker", "blockers", blockers)
	listFlag("guardrail", "guardrails", guardrails)
	listFlag("reference", "references", refs)
	listFlag("validation", "recentValidation", validations)
	boolFlag("merge", "merge", *merge)
	listFlag("append-validation", "appendValidation", appendValidations)
	strFlag("reason", "archiveReason", *reason)
	listFlag("archive-phase", "completedPhaseIds", phases)
	if err := intsFlag("archive-note", "noteIndexes", noteIdx); err != nil {
		return usageErr("%v", err)
	}
	if err := intsFlag("archive-finding", "resolvedFindingIndexes", findingIdx); err != nil {
		return usageErr("%v", err)
	}
	// The note rollover selector.
	if (set["keep-notes"] || set["pin-note"]) && !*rollover {
		return usageErr("--keep-notes and --pin-note need --rollover")
	}
	if *rollover {
		rb := ojson.NewObject(2)
		if set["keep-notes"] {
			rb.Set("keepLatest", ojson.IntValue(int64(*keepNotes)))
		}
		if set["pin-note"] {
			out := make([]ojson.Value, len(pinNotes))
			for i, s := range pinNotes {
				n, err := strconv.Atoi(s)
				if err != nil {
					return usageErr("--pin-note expects integers")
				}
				out[i] = ojson.IntValue(int64(n))
			}
			rb.Set("pinNoteIndexes", ojson.ArrayValue(out))
		}
		put("noteRollover", rb.Value())
	}
	if *apply {
		put("mode", ojson.StringValue("apply"))
	}
	strFlag("preview-token", "previewToken", *token)
	strFlag("confirm", "confirmation", *confirm)
	if set["expected-hash"] {
		for _, m := range members {
			if m.Key == "expectedHash" && m.Value.Str() != *expected {
				return usageErr("--expected-hash conflicts with expectedHash in --input")
			}
		}
		put("expectedHash", ojson.StringValue(*expected))
	}
	toolIn := ojson.ObjectValue(members)
	_, hasHash := toolIn.Get("expectedHash")
	existing := cmd != "create" && cmd != "compact"
	if cmd == "create" {
		if ov, ok := toolIn.Get("overwrite"); ok && ov.Bool() {
			existing = true
		}
	}
	if cmd == "compact" {
		if m, ok := toolIn.Get("mode"); ok && m.Str() == "apply" {
			existing = true
		}
	}
	if existing && !hasHash && !*legacy {
		return usageErr("existing-state writes require --expected-hash (the stateHash from the latest read) or --legacy-unhashed")
	}

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
	if *journal != 1 && *journal != 2 {
		return usageErr("--journal-version must be 1 or 2")
	}
	e.JournalVersion = *journal
	e.EvidenceSource = evidence.SourceCLI
	e.Source = history.SourceCLI
	return execMutation(e, cmd, toolIn, *yes, *jsonOut, stdout, stderr)
}

// execMutation parses, prepares, authorizes and commits one mutation.
func execMutation(e *engine.Engine, cmd string, toolIn ojson.Value, yes, jsonOut bool, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	data, err := input.ParseMutationInput(cmd, toolIn, input.SurfaceCore)
	if err != nil {
		return fail(stdout, stderr, jsonOut, err)
	}
	prep, err := e.Prepare(cmd, data)
	if err != nil {
		return fail(stdout, stderr, jsonOut, err)
	}
	auth := &CLIAuthorizer{Yes: yes, TTY: IsTerminal(), In: Stdin, Out: stderr}
	out, err := e.Execute(ctx, prep, auth, engine.ExecOptions{})
	if err != nil {
		return fail(stdout, stderr, jsonOut, err)
	}
	if out.Text != "" {
		if jsonOut {
			fmt.Fprintln(stdout, string(ojson.Pretty(ojson.NewObject(2).Set("output", ojson.StringValue(out.Text)).Set("metadata", out.Metadata).Value())))
		} else {
			fmt.Fprintln(stdout, out.Text)
		}
		return 0
	}
	fmt.Fprintln(stdout, out.String())
	return 0
}
