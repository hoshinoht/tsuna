package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/mcp"
)

// runMCP runs `shiori mcp`: the workplan tools as a stdio MCP server.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shiori mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "project root (default: the client's single MCP root)")
	prefix := fs.String("tool-prefix", "", `prefix for tool names ("" suits a server named "workplan": workplan_resume, ...)`)
	approval := fs.String("write-approval", mcp.ApproveAuto, "auto | elicitation | client | deny")
	journal := fs.Int("journal-version", 2, "journal format of writes: 2 or 1")
	rebase := fs.Bool("rebase", false, "apply stale updates over non-conflicting newer writes unless a call sets rebase=false (X4)")
	advice := fs.String("compaction-advice", "", "compaction advisor thresholds: off, or key=value pairs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*journal != 1 && *journal != 2) {
		fmt.Fprintln(stderr, "shiori mcp: usage: shiori mcp [--root DIR] [--tool-prefix P] [--write-approval auto|elicitation|client|deny] [--journal-version 1|2] [--compaction-advice SPEC] [--rebase]")
		return 2
	}
	var th *advisor.Thresholds
	if *advice != "" {
		t, err := advisor.ParseThresholds(*advice)
		if err != nil {
			fmt.Fprintln(stderr, "shiori mcp:", err)
			return 2
		}
		th = t
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err := mcp.Serve(ctx, mcp.Options{In: stdin, Out: stdout, Err: stderr, Root: *root, ToolPrefix: *prefix,
		WriteApproval: *approval, Version: Version, Compaction: th, JournalVersion: *journal, Rebase: *rebase})
	if err != nil {
		fmt.Fprintln(stderr, "shiori mcp:", err)
		return 1
	}
	return 0
}
