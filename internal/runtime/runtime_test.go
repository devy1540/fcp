package runtime

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devy1540/fcp/internal/state"
)

func TestStartServesHTTPAndGCPOnDynamicPorts(t *testing.T) {
	root := t.TempDir()
	credentials := filepath.Join(root, "credentials.json")
	fcpRuntime, err := Start(Config{
		Listen:         "127.0.0.1:0",
		GCPListen:      "127.0.0.1:0",
		DataDir:        filepath.Join(root, "data"),
		Profile:        "demo",
		ProjectID:      "fcp-local",
		CredentialsOut: credentials,
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := fcpRuntime.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}()

	response, err := http.Get(fcpRuntime.HTTPEndpoint() + "/_fcp/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", response.StatusCode)
	}
	connection, err := net.DialTimeout("tcp", fcpRuntime.GCPAddress(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	info, err := os.Stat(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode=%o", info.Mode().Perm())
	}
}

func TestStartRejectsUnknownProfile(t *testing.T) {
	_, err := Start(Config{DataDir: t.TempDir(), Profile: "unknown", Logger: log.New(io.Discard, "", 0)})
	if err == nil {
		t.Fatal("expected unknown profile error")
	}
}

func TestStartRejectsInvalidIntegrityMode(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	_, err := Start(Config{
		DataDir:       dataDir,
		IntegrityMode: "invalid",
		Logger:        log.New(io.Discard, "", 0),
	})
	if err == nil {
		t.Fatal("expected invalid integrity mode error")
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid integrity mode created data directory: %v", statErr)
	}
}

func TestStartRejectsConcurrentRuntimeForDataDir(t *testing.T) {
	dataDir := t.TempDir()
	first, err := Start(Config{
		Listen:    "127.0.0.1:0",
		GCPListen: "127.0.0.1:0",
		DataDir:   dataDir,
		Logger:    log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Start(Config{
		Listen:    "127.0.0.1:0",
		GCPListen: "127.0.0.1:0",
		DataDir:   dataDir,
		Logger:    log.New(io.Discard, "", 0),
	}); !errors.Is(err, state.ErrDataDirLocked) {
		t.Fatalf("second runtime error=%v want=%v", err, state.ErrDataDirLocked)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, err := Start(Config{
		Listen:    "127.0.0.1:0",
		GCPListen: "127.0.0.1:0",
		DataDir:   dataDir,
		Logger:    log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("data directory lock was not released: %v", err)
	}
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
