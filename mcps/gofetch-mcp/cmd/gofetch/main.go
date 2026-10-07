package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hoshinoht/gofetch-mcp/internal/mcpserver"
)

const version = "0.1.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gofetch %s\n", version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := mcpserver.New(version)
	if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("gofetch: %v", err)
	}
}
