package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hjyoon/fcp/internal/profile"
	"github.com/hjyoon/fcp/internal/server"
	"github.com/hjyoon/fcp/internal/state"
)

func TestDoctorStatusResourcesAndVerify(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.SeedPodo(store, "podo-local"); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.NewWithOptions(store, server.Options{ProjectID: "podo-local"}))
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
	jsonResult := runCLI(t, "env", "podo-backend", "--format", "json", "--endpoint", "http://127.0.0.1:9999")
	if jsonResult.exitCode != 0 || !jsonResult.output.OK || !strings.Contains(jsonResult.stdout, `"GOOGLE_GEMINI_BASE_URL": "http://127.0.0.1:9999"`) || strings.Contains(jsonResult.stdout, "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("backend env exit=%d stdout=%s stderr=%s", jsonResult.exitCode, jsonResult.stdout, jsonResult.stderr)
	}

	shellResult := runCLI(t, "env", "podo-notification", "--format", "shell")
	if shellResult.exitCode != 0 || !strings.Contains(shellResult.stdout, "export AWS_ENDPOINT_URL='http://127.0.0.1:4566'") || !strings.Contains(shellResult.stdout, "export PODO_QUEUE_PROVIDER='pubsub'") {
		t.Fatalf("notification env exit=%d stdout=%s stderr=%s", shellResult.exitCode, shellResult.stdout, shellResult.stderr)
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

func TestUsageErrorsUseJSONAndExitTwo(t *testing.T) {
	result := runCLI(t, "resources", "list", "--provider", "azure")
	if result.exitCode != 2 || !strings.Contains(result.stderr, `"code": "service_required"`) {
		t.Fatalf("usage error exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
}

func TestUnknownCommandIsDetectedBeforeServerStartup(t *testing.T) {
	if !IsCommand([]string{"unknown"}) || IsCommand([]string{"--profile", "podo"}) || IsCommand(nil) {
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
