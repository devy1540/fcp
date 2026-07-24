package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	fcpruntime "github.com/devy1540/fcp/internal/runtime"
	"github.com/devy1540/fcp/internal/state"
)

func runExec(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("exec", stderr)
	snapshotName := flags.String("snapshot", "", "snapshot to materialize")
	dataDir := flags.String("data-dir", ".fcp", "source FCP data directory")
	profileName := flags.String("profile", "", "optional seed profile (supported: demo)")
	projectID := flags.String("project", "fcp-local", "GCP project ID")
	if err := flags.Parse(args); err != nil {
		writeCLIError(stderr, "exec", "invalid_arguments", err.Error())
		return 2
	}
	commandArgs := flags.Args()
	if len(commandArgs) == 0 {
		writeCLIError(stderr, "exec", "command_required", "place a command after --")
		return 2
	}
	if *profileName != "" && *profileName != "demo" {
		writeCLIError(stderr, "exec", "invalid_profile", "profile must be demo or empty")
		return 2
	}

	temporaryRoot, err := os.MkdirTemp("", "fcp-exec-*")
	if err != nil {
		writeCLIError(stderr, "exec", "temporary_directory_failed", err.Error())
		return 1
	}
	defer os.RemoveAll(temporaryRoot)
	if err := os.Chmod(temporaryRoot, 0o700); err != nil {
		writeCLIError(stderr, "exec", "temporary_directory_failed", err.Error())
		return 1
	}
	isolatedDataDir := filepath.Join(temporaryRoot, "data")
	if err := os.Mkdir(isolatedDataDir, 0o700); err != nil {
		writeCLIError(stderr, "exec", "temporary_directory_failed", err.Error())
		return 1
	}
	if *snapshotName != "" {
		if _, err := state.MaterializeSnapshot(*dataDir, *snapshotName, isolatedDataDir); err != nil {
			writeCLIError(stderr, "exec", "snapshot_failed", err.Error())
			return 1
		}
	}

	credentialsPath := ""
	if *profileName == "demo" {
		credentialsPath = filepath.Join(temporaryRoot, "fcp-local-credentials.json")
	}
	fcpRuntime, err := fcpruntime.Start(fcpruntime.Config{
		Listen:         "127.0.0.1:0",
		GCPListen:      "127.0.0.1:0",
		DataDir:        isolatedDataDir,
		Profile:        *profileName,
		ProjectID:      *projectID,
		CredentialsOut: credentialsPath,
		Logger:         log.New(stderr, "fcp exec: ", log.LstdFlags),
	})
	if err != nil {
		writeCLIError(stderr, "exec", "startup_failed", err.Error())
		return 1
	}
	defer closeRuntime(fcpRuntime, stderr)

	variables, _ := providerEnvironment("all", fcpRuntime.HTTPEndpoint(), fcpRuntime.GCPAddress(), *projectID, credentialsPath)
	variables["FCP_DATA_DIR"] = isolatedDataDir
	command := exec.Command(commandArgs[0], commandArgs[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = mergedEnvironment(os.Environ(), variables)
	if err := command.Start(); err != nil {
		writeCLIError(stderr, "exec", "command_start_failed", err.Error())
		return 1
	}

	waited := make(chan error, 1)
	go func() {
		waited <- command.Wait()
	}()
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case commandErr := <-waited:
		return commandExitCode(commandErr)
	case runtimeErr := <-fcpRuntime.Errors():
		_ = command.Process.Kill()
		<-waited
		writeCLIError(stderr, "exec", "runtime_failed", runtimeErr.Error())
		return 1
	case <-signalContext.Done():
		_ = command.Process.Signal(os.Interrupt)
		select {
		case commandErr := <-waited:
			return commandExitCode(commandErr)
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-waited
			return 130
		}
	}
}

func mergedEnvironment(current []string, overrides map[string]string) []string {
	environment := make(map[string]string, len(current)+len(overrides))
	for _, entry := range current {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	for key, value := range overrides {
		environment[key] = value
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if exitCode := exitError.ExitCode(); exitCode >= 0 {
			return exitCode
		}
	}
	return 1
}

func closeRuntime(fcpRuntime *fcpruntime.Runtime, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fcpRuntime.Close(ctx); err != nil {
		fmt.Fprintf(stderr, "fcp exec: shutdown: %v\n", err)
	}
}
