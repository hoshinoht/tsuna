package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hoshinoht/gofetch-mcp/internal/config"
	"github.com/hoshinoht/gofetch-mcp/internal/mcpserver"
	"github.com/hoshinoht/gofetch-mcp/internal/search"
)

const version = "0.2.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	exaKeyFile := flag.String("exa-key-file", "", "file holding the Exa API key (overrides "+config.EnvExaKeyFile+"; \"none\" disables key files)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gofetch %s\n", version)
		return
	}

	// stdout carries the MCP protocol; notices go to stderr.
	log.SetOutput(os.Stderr)
	cfg := config.Load(*exaKeyFile, os.Getenv)
	for _, n := range cfg.Notices {
		log.Printf("gofetch: %s", n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := mcpserver.New(version, mcpserver.Options{Search: search.Config{ExaAPIKey: cfg.ExaAPIKey}})
	if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("gofetch: %v", err)
	}
}
