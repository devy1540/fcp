package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/hjyoon/fcp/internal/cli"
	"github.com/hjyoon/fcp/internal/profile"
	"github.com/hjyoon/fcp/internal/server"
	"github.com/hjyoon/fcp/internal/state"
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

	store, err := state.Open(*dataDir)
	if err != nil {
		log.Fatalf("open state: %v", err)
	}
	if *profileName != "" {
		switch *profileName {
		case "demo":
			summary, err := profile.SeedDemo(store, *projectID)
			if err != nil {
				log.Fatalf("seed demo profile: %v", err)
			}
			if *credentialsOut != "" {
				if err := profile.WriteDemoCredentials(store, *projectID, *credentialsOut); err != nil {
					log.Fatalf("write FCP credentials: %v", err)
				}
				log.Printf("wrote local FCP credentials to %s", *credentialsOut)
			}
			log.Printf("seeded demo profile project=%s dynamo_tables=%d queues=%d buckets=%d topics=%d subscriptions=%d secrets=%d kms_keys=%d iam_accounts=%d", summary.Project, summary.DynamoTables, summary.Queues, summary.Buckets, summary.Topics, summary.Subscriptions, summary.Secrets, summary.KMSKeys, summary.IAMAccounts)
		default:
			log.Fatalf("unknown profile %q", *profileName)
		}
	}

	handler := server.NewWithOptions(store, server.Options{
		ProjectID:           *projectID,
		ServiceAccountEmail: *metadataServiceAccount,
	})
	if *legacyPubSubListen != "" {
		*gcpListen = *legacyPubSubListen
	}
	gcpListener, err := net.Listen("tcp", *gcpListen)
	if err != nil {
		log.Fatalf("listen for GCP APIs: %v", err)
	}
	gcpServer := server.NewGCPGRPCServer(store)
	go func() {
		log.Printf("FCP GCP gRPC APIs listening on %s", *gcpListen)
		if err := gcpServer.Serve(gcpListener); err != nil {
			log.Printf("GCP gRPC server stopped: %v", err)
		}
	}()
	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("FCP %s listening on http://%s (data: %s)", version, *listen, *dataDir)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
