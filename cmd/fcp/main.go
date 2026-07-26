package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devy1540/fcp/internal/cli"
	fcpruntime "github.com/devy1540/fcp/internal/runtime"
	"github.com/devy1540/fcp/internal/state"
)

var version = "dev"

func main() {
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(signalContext, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if cli.IsCommand(args) {
		return cli.Run(args, stdout, stderr)
	}
	flags := flag.NewFlagSet("fcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:4566", "HTTP listen address")
	gcpListen := flags.String("gcp-listen", "127.0.0.1:8085", "GCP gRPC listen address")
	legacyPubSubListen := flags.String("pubsub-listen", "", "deprecated alias for --gcp-listen")
	dataDir := flags.String("data-dir", ".fcp", "persistent data directory")
	profileName := flags.String("profile", "", "optional seed profile (supported: demo)")
	projectID := flags.String("project", "fcp-local", "project ID used by the seed profile")
	metadataServiceAccount := flags.String("metadata-service-account", "", "service account email returned by the fake GCP metadata server")
	credentialsOut := flags.String("credentials-out", "", "write local profile service-account credentials to this path")
	integrityModeFlag := flags.String("integrity-mode", string(state.IntegrityModeStartup), "object integrity mode: startup or strict")
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}

	if *legacyPubSubListen != "" {
		*gcpListen = *legacyPubSubListen
	}
	integrityMode, err := state.ParseIntegrityMode(*integrityModeFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags)
	fcpRuntime, err := fcpruntime.Start(fcpruntime.Config{
		Listen:                 *listen,
		GCPListen:              *gcpListen,
		DataDir:                *dataDir,
		Profile:                *profileName,
		ProjectID:              *projectID,
		MetadataServiceAccount: *metadataServiceAccount,
		CredentialsOut:         *credentialsOut,
		IntegrityMode:          integrityMode,
		Version:                version,
		Logger:                 logger,
	})
	if err != nil {
		logger.Printf("start FCP: %v", err)
		return 1
	}

	exitCode := 0
	select {
	case <-ctx.Done():
	case runtimeErr := <-fcpRuntime.Errors():
		logger.Printf("server stopped: %v", runtimeErr)
		exitCode = 1
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fcpRuntime.Close(shutdownContext); err != nil {
		logger.Printf("shutdown FCP: %v", err)
		exitCode = 1
	}
	return exitCode
}
