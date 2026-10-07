package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/protocol"
)

// runServe runs `shiori serve --stdio`: one private JSON-lines protocol
// connection on stdin/stdout for a native host adapter.
// There is no network listener and no daemon; the process exits on stdin
// EOF, SIGTERM/SIGINT, a fatal frame error, or after the idle timeout.
func runServe(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shiori serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stdio := fs.Bool("stdio", false, "serve the protocol on stdin/stdout (required)")
	idle := fs.Duration("idle-timeout", protocol.DefaultIdleTimeout, "exit after this long with no request and no prepared intent")
	maxFrame := fs.Int("max-frame-bytes", protocol.DefaultMaxFrameBytes, "request frame limit in bytes")
	advice := fs.String("compaction-advice", "", "compaction advisor thresholds: off, or key=value pairs (D.4)")
	journal := fs.Int("journal-version", 2, "journal format of writes: 2 (by reference) or 1 (inline, reference-compatible)")
	rebase := fs.Bool("rebase", false, "apply stale updates over non-conflicting newer writes unless a call sets rebase=false (X4)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*stdio || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "shiori serve: usage: shiori serve --stdio [--idle-timeout 10m] [--max-frame-bytes N] [--compaction-advice SPEC] [--journal-version 1|2] [--rebase]")
		return 2
	}
	if *journal != 1 && *journal != 2 {
		fmt.Fprintln(stderr, "shiori serve: --journal-version must be 1 or 2")
		return 2
	}
	if *idle <= 0 || *idle > 24*time.Hour {
		fmt.Fprintln(stderr, "shiori serve: --idle-timeout must be between 1ns and 24h")
		return 2
	}
	if *maxFrame < 1024 || *maxFrame > protocol.DefaultMaxFrameBytes*4 {
		fmt.Fprintln(stderr, "shiori serve: --max-frame-bytes must be between 1024 and 64 MiB")
		return 2
	}
	var th *advisor.Thresholds
	if *advice != "" {
		t, err := advisor.ParseThresholds(*advice)
		if err != nil {
			fmt.Fprintln(stderr, "shiori serve:", err)
			return 2
		}
		th = t
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := protocol.Serve(ctx, protocol.Options{
		Compaction:     th,
		JournalVersion: *journal,
		Rebase:         *rebase,
		In:             stdin, Out: stdout, Err: stderr,
		CoreVersion:   Version,
		IdleTimeout:   *idle,
		MaxFrameBytes: *maxFrame,
	})
	switch {
	case err == nil, errors.Is(err, protocol.ErrIdle):
		return 0
	default:
		return 1
	}
}
