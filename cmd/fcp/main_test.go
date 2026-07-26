package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devy1540/fcp/internal/state"
)

func TestRunVersionAndCLIHelp(t *testing.T) {
	for _, test := range []struct {
		name       string
		args       []string
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"--version"}, wantStdout: version},
		{name: "CLI help", args: []string{"help"}, wantStdout: "FCP AI-friendly commands"},
		{name: "server help", args: []string{"--help"}, wantStderr: "Usage of fcp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := run(context.Background(), test.args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), test.wantStdout) {
				t.Fatalf("stdout=%q want substring %q", stdout.String(), test.wantStdout)
			}
			if !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr=%q want substring %q", stderr.String(), test.wantStderr)
			}
		})
	}
}

func TestRunRejectsInvalidConfigurationBeforeCreatingData(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"--integrity-mode", "invalid", "--data-dir", dataDir}, &stdout, &stderr)
	if exitCode != 2 || !strings.Contains(stderr.String(), "integrity mode must be") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid configuration created data: %v", err)
	}
}

func TestRunStartsAndGracefullyStopsServer(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	credentials := filepath.Join(root, "credentials.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	exitCode := run(ctx, []string{
		"--listen", "127.0.0.1:0",
		"--gcp-listen", "127.0.0.1:0",
		"--data-dir", dataDir,
		"--profile", "demo",
		"--project", "test-project",
		"--credentials-out", credentials,
		"--integrity-mode", "strict",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	info, err := os.Stat(credentials)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials info=%v err=%v", info, err)
	}
	store, err := state.OpenWithOptions(dataDir, state.OpenOptions{IntegrityMode: state.IntegrityModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if tables := store.ListDynamoTables(); len(tables) == 0 {
		t.Fatal("demo profile did not persist resources")
	}
}

func TestRunReportsStartupFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"--listen", "127.0.0.1:0",
		"--gcp-listen", "127.0.0.1:0",
		"--data-dir", t.TempDir(),
		"--profile", "unknown",
	}, &stdout, &stderr)
	if exitCode != 1 || !strings.Contains(stderr.String(), `unknown profile "unknown"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
}
