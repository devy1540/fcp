package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devy1540/fcp/internal/profile"
	"github.com/devy1540/fcp/internal/server"
	"github.com/devy1540/fcp/internal/state"
)

func TestDoctorStatusResourcesAndVerify(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.SeedDemo(store, "fcp-local"); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.NewWithOptions(store, server.Options{ProjectID: "fcp-local"}))
	defer httpServer.Close()
	gcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gcpListener.Close()

	doctor := runCLI(t, "doctor", "--endpoint", httpServer.URL, "--gcp-endpoint", gcpListener.Addr().String(), "--json")
	if doctor.exitCode != 0 || !doctor.output.OK || doctor.output.Command != "doctor" {
		t.Fatalf("doctor exit=%d output=%+v stderr=%s", doctor.exitCode, doctor.output, doctor.stderr)
	}

	status := runCLI(t, "status", "--endpoint", httpServer.URL, "--json")
	if status.exitCode != 0 || !status.output.OK || !strings.Contains(status.stdout, `"serviceCount": 13`) {
		t.Fatalf("status exit=%d stdout=%s stderr=%s", status.exitCode, status.stdout, status.stderr)
	}

	resources := runCLI(t, "resources", "list", "--endpoint", httpServer.URL, "--service", "pubsub", "--provider", "gcp", "--limit", "1", "--json")
	if resources.exitCode != 0 || !resources.output.OK || !strings.Contains(resources.stdout, `"service": "pubsub"`) || !strings.Contains(resources.stdout, `"limit": 1`) {
		t.Fatalf("resources exit=%d stdout=%s stderr=%s", resources.exitCode, resources.stdout, resources.stderr)
	}

	verify := runCLI(t, "verify", "--endpoint", httpServer.URL, "--service", "gcs", "--json")
	if verify.exitCode != 0 || !verify.output.OK || !strings.Contains(verify.stdout, `"scope": "runtime-and-declared-compatibility"`) || !strings.Contains(verify.stdout, `"PARTIAL"`) {
		t.Fatalf("verify exit=%d stdout=%s stderr=%s", verify.exitCode, verify.stdout, verify.stderr)
	}

	strict := runCLI(t, "verify", "--endpoint", httpServer.URL, "--service", "gcs", "--strict", "--json")
	if strict.exitCode != 1 || strict.output.OK {
		t.Fatalf("strict verify exit=%d stdout=%s stderr=%s", strict.exitCode, strict.stdout, strict.stderr)
	}
}

func TestDoctorReportsOfflineWithoutPanicking(t *testing.T) {
	offline := runCLI(t, "doctor", "--endpoint", "http://127.0.0.1:1", "--gcp-endpoint", "127.0.0.1:1", "--timeout", "100ms")
	if offline.exitCode != 1 || offline.output.OK || !strings.Contains(offline.stdout, `"http-health"`) {
		t.Fatalf("offline doctor exit=%d stdout=%s stderr=%s", offline.exitCode, offline.stdout, offline.stderr)
	}
}

func TestEnvJSONAndShell(t *testing.T) {
	jsonResult := runCLI(t, "env", "gcp", "--format", "json", "--endpoint", "http://127.0.0.1:9999")
	if jsonResult.exitCode != 0 || !jsonResult.output.OK || !strings.Contains(jsonResult.stdout, `"GOOGLE_GEMINI_BASE_URL": "http://127.0.0.1:9999"`) || strings.Contains(jsonResult.stdout, "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("gcp env exit=%d stdout=%s stderr=%s", jsonResult.exitCode, jsonResult.stdout, jsonResult.stderr)
	}

	shellResult := runCLI(t, "env", "aws", "--format", "shell")
	if shellResult.exitCode != 0 || !strings.Contains(shellResult.stdout, "export AWS_ENDPOINT_URL='http://127.0.0.1:4566'") || strings.Contains(shellResult.stdout, "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("aws env exit=%d stdout=%s stderr=%s", shellResult.exitCode, shellResult.stdout, shellResult.stderr)
	}
}

func TestSkillInstallIsSafeAndComplete(t *testing.T) {
	target := t.TempDir()
	installed := runCLI(t, "skill", "install", "--target", target, "--json")
	if installed.exitCode != 0 || !installed.output.OK {
		t.Fatalf("skill install exit=%d stdout=%s stderr=%s", installed.exitCode, installed.stdout, installed.stderr)
	}
	for _, relative := range []string{"SKILL.md", filepath.Join("agents", "openai.yaml")} {
		path := filepath.Join(target, fcpSkillName, relative)
		body, err := os.ReadFile(path)
		if err != nil || len(body) == 0 {
			t.Fatalf("installed skill file %s: bytes=%d err=%v", path, len(body), err)
		}
	}

	again := runCLI(t, "skill", "install", "--target", target)
	if again.exitCode != 1 || !strings.Contains(again.stderr, `"code": "already_exists"`) {
		t.Fatalf("second install exit=%d stdout=%s stderr=%s", again.exitCode, again.stdout, again.stderr)
	}
}

func TestSnapshotCommands(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("baseline"); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.New(store))
	defer httpServer.Close()

	saved := runCLI(t, "snapshot", "save", "clean", "--endpoint", httpServer.URL)
	if saved.exitCode != 0 || !saved.output.OK || !strings.Contains(saved.stdout, `"containsSensitiveData": true`) {
		t.Fatalf("save exit=%d stdout=%s stderr=%s", saved.exitCode, saved.stdout, saved.stderr)
	}
	listed := runCLI(t, "snapshot", "list", "--endpoint", httpServer.URL)
	if listed.exitCode != 0 || !strings.Contains(listed.stdout, `"name": "clean"`) || strings.Contains(listed.stdout, `"Buckets"`) {
		t.Fatalf("list exit=%d stdout=%s stderr=%s", listed.exitCode, listed.stdout, listed.stderr)
	}
	loaded := runCLI(t, "snapshot", "load", "clean", "--endpoint", httpServer.URL)
	if loaded.exitCode != 0 || !loaded.output.OK {
		t.Fatalf("load exit=%d stdout=%s stderr=%s", loaded.exitCode, loaded.stdout, loaded.stderr)
	}
	deleted := runCLI(t, "snapshot", "delete", "clean", "--endpoint", httpServer.URL)
	if deleted.exitCode != 0 || !strings.Contains(deleted.stdout, `"deleted": "clean"`) {
		t.Fatalf("delete exit=%d stdout=%s stderr=%s", deleted.exitCode, deleted.stdout, deleted.stderr)
	}
}

func TestExecStartsIsolatedRuntimeAndPropagatesExitCode(t *testing.T) {
	sourceData := t.TempDir()
	store, err := state.Open(sourceData)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("snapshot-bucket"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSnapshot("baseline"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GO_WANT_FCP_EXEC_HELPER", "1")
	success := runCLI(t,
		"exec", "--snapshot", "baseline", "--data-dir", sourceData, "--profile", "demo", "--",
		os.Args[0], "-test.run=TestExecHelperProcess", "--", "verify",
	)
	if success.exitCode != 0 {
		t.Fatalf("exec exit=%d stdout=%s stderr=%s", success.exitCode, success.stdout, success.stderr)
	}
	failed := runCLI(t,
		"exec", "--",
		os.Args[0], "-test.run=TestExecHelperProcess", "--", "exit7",
	)
	if failed.exitCode != 7 {
		t.Fatalf("exec did not propagate exit code: %d stderr=%s", failed.exitCode, failed.stderr)
	}
}

func TestExecHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FCP_EXEC_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "exit7" {
		os.Exit(7)
	}
	endpoint := os.Getenv("FCP_HTTP_ENDPOINT")
	response, err := http.Get(endpoint + "/_fcp/dashboard?service=s3")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "snapshot-bucket") {
		t.Fatalf("snapshot unavailable status=%d body=%s", response.StatusCode, body)
	}
	credentials := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	info, err := os.Stat(credentials)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials path=%q mode=%v err=%v", credentials, infoMode(info), err)
	}
	os.Exit(0)
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func TestUsageErrorsUseJSONAndExitTwo(t *testing.T) {
	result := runCLI(t, "resources", "list", "--provider", "azure")
	if result.exitCode != 2 || !strings.Contains(result.stderr, `"code": "service_required"`) {
		t.Fatalf("usage error exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
}

func TestUnknownCommandIsDetectedBeforeServerStartup(t *testing.T) {
	if !IsCommand([]string{"unknown"}) || IsCommand([]string{"--profile", "demo"}) || IsCommand(nil) {
		t.Fatal("command detection does not preserve server flags")
	}
	result := runCLI(t, "unknown")
	if result.exitCode != 2 || !strings.Contains(result.stderr, `"code": "unknown_command"`) {
		t.Fatalf("unknown command exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
}

type cliTestResult struct {
	exitCode int
	stdout   string
	stderr   string
	output   struct {
		OK      bool   `json:"ok"`
		Command string `json:"command"`
	}
}

func runCLI(t *testing.T, args ...string) cliTestResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	result := cliTestResult{exitCode: Run(args, &stdout, &stderr), stdout: stdout.String(), stderr: stderr.String()}
	if result.stdout != "" {
		if err := json.Unmarshal(stdout.Bytes(), &result.output); err != nil && !strings.HasPrefix(result.stdout, "export ") {
			t.Fatalf("stdout is not JSON: %v\n%s", err, result.stdout)
		}
	}
	return result
}
