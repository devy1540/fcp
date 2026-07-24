package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/devy1540/fcp/internal/profile"
	"github.com/devy1540/fcp/internal/server"
	"github.com/devy1540/fcp/internal/state"
	"google.golang.org/grpc"
)

type Config struct {
	Listen                 string
	GCPListen              string
	DataDir                string
	Profile                string
	ProjectID              string
	MetadataServiceAccount string
	CredentialsOut         string
	Version                string
	Logger                 *log.Logger
}

type Runtime struct {
	httpListener net.Listener
	gcpListener  net.Listener
	httpServer   *http.Server
	gcpServer    *grpc.Server
	errors       chan error
	logger       *log.Logger
}

func Start(config Config) (*Runtime, error) {
	config = withDefaults(config)
	store, err := state.Open(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	if err := seedProfile(store, config); err != nil {
		return nil, err
	}

	gcpListener, err := net.Listen("tcp", config.GCPListen)
	if err != nil {
		return nil, fmt.Errorf("listen for GCP APIs: %w", err)
	}
	httpListener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		_ = gcpListener.Close()
		return nil, fmt.Errorf("listen for HTTP APIs: %w", err)
	}

	fcpRuntime := &Runtime{
		httpListener: httpListener,
		gcpListener:  gcpListener,
		httpServer: &http.Server{
			Handler: server.NewWithOptions(store, server.Options{
				ProjectID:           config.ProjectID,
				ServiceAccountEmail: config.MetadataServiceAccount,
			}),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		gcpServer: server.NewGCPGRPCServer(store),
		errors:    make(chan error, 2),
		logger:    config.Logger,
	}

	fcpRuntime.logger.Printf("FCP GCP gRPC APIs listening on %s", fcpRuntime.GCPAddress())
	fcpRuntime.logger.Printf("FCP %s listening on %s (data: %s)", config.Version, fcpRuntime.HTTPEndpoint(), config.DataDir)
	go func() {
		if serveErr := fcpRuntime.gcpServer.Serve(gcpListener); serveErr != nil {
			fcpRuntime.reportError(fmt.Errorf("serve GCP APIs: %w", serveErr))
		}
	}()
	go func() {
		if serveErr := fcpRuntime.httpServer.Serve(httpListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			fcpRuntime.reportError(fmt.Errorf("serve HTTP APIs: %w", serveErr))
		}
	}()
	return fcpRuntime, nil
}

func (r *Runtime) HTTPEndpoint() string {
	return "http://" + r.httpListener.Addr().String()
}

func (r *Runtime) GCPAddress() string {
	return r.gcpListener.Addr().String()
}

func (r *Runtime) Errors() <-chan error {
	return r.errors
}

func (r *Runtime) Close(ctx context.Context) error {
	httpErr := r.httpServer.Shutdown(ctx)
	stopped := make(chan struct{})
	go func() {
		r.gcpServer.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		r.gcpServer.Stop()
	}
	_ = r.httpListener.Close()
	_ = r.gcpListener.Close()
	return httpErr
}

func (r *Runtime) reportError(err error) {
	select {
	case r.errors <- err:
	default:
		r.logger.Printf("%v", err)
	}
}

func withDefaults(config Config) Config {
	if config.Listen == "" {
		config.Listen = "127.0.0.1:4566"
	}
	if config.GCPListen == "" {
		config.GCPListen = "127.0.0.1:8085"
	}
	if config.DataDir == "" {
		config.DataDir = ".fcp"
	}
	if config.ProjectID == "" {
		config.ProjectID = "fcp-local"
	}
	if config.Version == "" {
		config.Version = "dev"
	}
	if config.Logger == nil {
		config.Logger = log.Default()
	}
	return config
}

func seedProfile(store *state.Store, config Config) error {
	if config.Profile == "" {
		return nil
	}
	if config.Profile != "demo" {
		return fmt.Errorf("unknown profile %q", config.Profile)
	}
	summary, err := profile.SeedDemo(store, config.ProjectID)
	if err != nil {
		return fmt.Errorf("seed demo profile: %w", err)
	}
	if config.CredentialsOut != "" {
		if err := profile.WriteDemoCredentials(store, config.ProjectID, config.CredentialsOut); err != nil {
			return fmt.Errorf("write FCP credentials: %w", err)
		}
		config.Logger.Printf("wrote local FCP credentials to %s", config.CredentialsOut)
	}
	config.Logger.Printf(
		"seeded demo profile project=%s dynamo_tables=%d queues=%d buckets=%d topics=%d subscriptions=%d secrets=%d kms_keys=%d iam_accounts=%d",
		summary.Project, summary.DynamoTables, summary.Queues, summary.Buckets, summary.Topics,
		summary.Subscriptions, summary.Secrets, summary.KMSKeys, summary.IAMAccounts,
	)
	return nil
}
