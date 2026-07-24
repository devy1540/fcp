package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devy1540/fcp/internal/cli"
	fcpruntime "github.com/devy1540/fcp/internal/runtime"
)

var version = "dev"

func main() {
	if cli.IsCommand(os.Args[1:]) {
		os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
	}

	listen := flag.String("listen", "127.0.0.1:4566", "HTTP listen address")
	gcpListen := flag.String("gcp-listen", "127.0.0.1:8085", "GCP gRPC listen address")
	legacyPubSubListen := flag.String("pubsub-listen", "", "deprecated alias for --gcp-listen")
	dataDir := flag.String("data-dir", ".fcp", "persistent data directory")
	profileName := flag.String("profile", "", "optional seed profile (supported: demo)")
	projectID := flag.String("project", "fcp-local", "project ID used by the seed profile")
	metadataServiceAccount := flag.String("metadata-service-account", "", "service account email returned by the fake GCP metadata server")
	credentialsOut := flag.String("credentials-out", "", "write local profile service-account credentials to this path")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *legacyPubSubListen != "" {
		*gcpListen = *legacyPubSubListen
	}
	fcpRuntime, err := fcpruntime.Start(fcpruntime.Config{
		Listen:                 *listen,
		GCPListen:              *gcpListen,
		DataDir:                *dataDir,
		Profile:                *profileName,
		ProjectID:              *projectID,
		MetadataServiceAccount: *metadataServiceAccount,
		CredentialsOut:         *credentialsOut,
		Version:                version,
		Logger:                 log.Default(),
	})
	if err != nil {
		log.Fatal(err)
	}

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exitCode := 0
	select {
	case <-signalContext.Done():
	case runtimeErr := <-fcpRuntime.Errors():
		log.Printf("server stopped: %v", runtimeErr)
		exitCode = 1
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fcpRuntime.Close(shutdownContext); err != nil {
		log.Printf("shutdown FCP: %v", err)
		exitCode = 1
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
