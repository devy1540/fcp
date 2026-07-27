package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devy1540/fcp/internal/compatibility"
	"github.com/devy1540/fcp/internal/reliability"
)

func TestCLIUsageAndValidationErrors(t *testing.T) {
	var stdout bytes.Buffer
	if exitCode := Run(nil, &stdout, &bytes.Buffer{}); exitCode != 0 || !strings.Contains(stdout.String(), "FCP AI-friendly commands") {
		t.Fatalf("usage exit=%d output=%q", exitCode, stdout.String())
	}
	stdout.Reset()
	if exitCode := Run([]string{"help"}, &stdout, &bytes.Buffer{}); exitCode != 0 || !strings.Contains(stdout.String(), "fcp exec") {
		t.Fatalf("help exit=%d output=%q", exitCode, stdout.String())
	}

	tests := []struct {
		name string
		args []string
		code string
	}{
		{name: "doctor timeout", args: []string{"doctor", "--timeout", "0"}, code: "invalid_timeout"},
		{name: "doctor endpoint", args: []string{"doctor", "--endpoint", "ftp://example.test"}, code: "invalid_endpoint"},
		{name: "status timeout", args: []string{"status", "--timeout", "-1s"}, code: "invalid_timeout"},
		{name: "status endpoint", args: []string{"status", "--endpoint", "example.test"}, code: "invalid_endpoint"},
		{name: "resources subcommand", args: []string{"resources"}, code: "missing_subcommand"},
		{name: "resources service", args: []string{"resources", "list"}, code: "service_required"},
		{name: "resources page", args: []string{"resources", "list", "--service", "s3", "--limit", "101"}, code: "invalid_page"},
		{name: "resources provider", args: []string{"resources", "list", "--service", "s3", "--provider", "azure"}, code: "invalid_provider"},
		{name: "resources timeout", args: []string{"resources", "list", "--service", "s3", "--timeout", "31s"}, code: "invalid_timeout"},
		{name: "verify timeout", args: []string{"verify", "--timeout", "0"}, code: "invalid_timeout"},
		{name: "verify endpoint", args: []string{"verify", "--endpoint", "unix:///tmp/fcp.sock"}, code: "invalid_endpoint"},
		{name: "reliability timeout", args: []string{"reliability", "--timeout", "31s"}, code: "invalid_timeout"},
		{name: "reliability minimum", args: []string{"reliability", "--minimum", "-1"}, code: "invalid_minimum"},
		{name: "env format", args: []string{"env", "--format", "yaml"}, code: "invalid_format"},
		{name: "env provider", args: []string{"env", "azure"}, code: "unknown_provider"},
		{name: "snapshot operation", args: []string{"snapshot"}, code: "operation_required"},
		{name: "snapshot invalid operation", args: []string{"snapshot", "copy", "name"}, code: "invalid_operation"},
		{name: "snapshot name", args: []string{"snapshot", "save"}, code: "name_required"},
		{name: "snapshot timeout", args: []string{"snapshot", "list", "--timeout", "0"}, code: "invalid_timeout"},
		{name: "snapshot endpoint", args: []string{"snapshot", "list", "--endpoint", "not-a-url"}, code: "invalid_endpoint"},
		{name: "skill subcommand", args: []string{"skill"}, code: "missing_subcommand"},
		{name: "exec command", args: []string{"exec"}, code: "command_required"},
		{name: "exec profile", args: []string{"exec", "--profile", "production", "--", "true"}, code: "invalid_profile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runCLI(t, test.args...)
			if result.exitCode != 2 || !strings.Contains(result.stderr, `"code": "`+test.code+`"`) {
				t.Fatalf("args=%v exit=%d stdout=%s stderr=%s", test.args, result.exitCode, result.stdout, result.stderr)
			}
		})
	}
}

func TestCLIRequestAndResponseFailures(t *testing.T) {
	assessment := reliability.Assess(compatibility.Services())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_fcp/dashboard":
			switch r.URL.Query().Get("view") {
			case "reliability":
				_ = json.NewEncoder(w).Encode(dashboardResponse{Project: "fcp-local", Reliability: assessment})
			case "service":
				_ = json.NewEncoder(w).Encode(dashboardResponse{
					Project: "fcp-local",
					Services: []dashboardService{{
						ID:       "s3",
						Provider: "AWS",
						Status:   "READY",
						Verification: dashboardVerification{Operations: []dashboardOperationVerification{{
							Name: "operation", Status: "UNKNOWN",
						}}},
					}},
					Page: &dashboardPage{Service: "s3", Limit: 1},
				})
			default:
				http.Error(w, `{"message":"dashboard unavailable"}`, http.StatusServiceUnavailable)
			}
		case "/message":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"broken request"}`))
		case "/error":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"missing resource"}`))
		case "/status":
			http.Error(w, "failure", http.StatusInternalServerError)
		case "/invalid":
			_, _ = w.Write([]byte("{"))
		case "/no-content":
			w.WriteHeader(http.StatusNoContent)
		case "/echo":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	notReady := runCLI(t, "reliability", "--endpoint", httpServer.URL)
	if notReady.exitCode != 1 || !strings.Contains(notReady.stdout, `"runtimeReady": false`) || !strings.Contains(notReady.stdout, `"ok": false`) {
		t.Fatalf("reliability accepted an unready runtime: exit=%d stdout=%s stderr=%s", notReady.exitCode, notReady.stdout, notReady.stderr)
	}
	mismatch := runCLI(t, "resources", "list", "--endpoint", httpServer.URL, "--service", "s3", "--provider", "gcp")
	if mismatch.exitCode != 2 || !strings.Contains(mismatch.stderr, `"code": "provider_mismatch"`) {
		t.Fatalf("provider mismatch exit=%d stdout=%s stderr=%s", mismatch.exitCode, mismatch.stdout, mismatch.stderr)
	}
	verify := runCLI(t, "verify", "--endpoint", httpServer.URL, "--service", "s3")
	if verify.exitCode != 1 || !strings.Contains(verify.stdout, `"ok": false`) {
		t.Fatalf("unknown verification status exit=%d stdout=%s stderr=%s", verify.exitCode, verify.stdout, verify.stderr)
	}
	status := runCLI(t, "status", "--endpoint", httpServer.URL)
	if status.exitCode != 1 || !strings.Contains(status.stderr, `"code": "request_failed"`) {
		t.Fatalf("status error exit=%d stdout=%s stderr=%s", status.exitCode, status.stdout, status.stderr)
	}

	client, err := newAPIClient(httpServer.URL+"/", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/message", want: "broken request"},
		{path: "/error", want: "missing resource"},
		{path: "/status", want: "500 Internal Server Error"},
		{path: "/invalid", want: "decode /invalid"},
	} {
		var target map[string]any
		if err := client.getJSON(test.path, &target); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("GET %s err=%v want=%q", test.path, err, test.want)
		}
	}
	if err := client.postJSON("/no-content", map[string]string{"name": "test"}, nil); err != nil {
		t.Fatalf("no-content POST failed: %v", err)
	}
	var echoed map[string]any
	if err := client.postJSON("/echo", map[string]string{"name": "test"}, &echoed); err != nil || echoed["ok"] != true {
		t.Fatalf("echo POST response=%v err=%v", echoed, err)
	}
	if err := client.postJSON("/echo", make(chan int), &echoed); err == nil {
		t.Fatal("unsupported JSON body was accepted")
	}
}

func TestCLIExecutionAndSnapshotFailureContracts(t *testing.T) {
	missingSnapshot := runCLI(t, "exec", "--snapshot", "missing", "--data-dir", t.TempDir(), "--", "true")
	if missingSnapshot.exitCode != 1 || !strings.Contains(missingSnapshot.stderr, `"code": "snapshot_failed"`) {
		t.Fatalf("missing snapshot exit=%d stdout=%s stderr=%s", missingSnapshot.exitCode, missingSnapshot.stdout, missingSnapshot.stderr)
	}
	missingCommand := "fcp-command-that-does-not-exist-" + strings.ReplaceAll(t.Name(), "/", "-")
	notStarted := runCLI(t, "exec", "--", missingCommand)
	if notStarted.exitCode != 1 || !strings.Contains(notStarted.stderr, `"code": "command_start_failed"`) {
		t.Fatalf("missing command exit=%d stdout=%s stderr=%s", notStarted.exitCode, notStarted.stdout, notStarted.stderr)
	}

	offline := runCLI(t, "snapshot", "list", "--endpoint", "http://127.0.0.1:1", "--timeout", "100ms")
	if offline.exitCode != 1 || !strings.Contains(offline.stderr, `"code": "request_failed"`) || !strings.Contains(offline.stderr, "list snapshot") {
		t.Fatalf("offline snapshot exit=%d stdout=%s stderr=%s", offline.exitCode, offline.stdout, offline.stderr)
	}
}

func TestCLIHelpersPreserveSafetyContracts(t *testing.T) {
	if firstNonEmpty("", "  ", "value") != "value" || firstNonEmpty("", " ") != "unknown error" {
		t.Fatal("firstNonEmpty did not normalize blank values")
	}
	if _, err := newAPIClient("ftp://example.test", time.Second); err == nil {
		t.Fatal("non-HTTP endpoint was accepted")
	}
	client, err := newAPIClient("http://127.0.0.1:1", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var target map[string]any
	if err := client.getJSON("/", &target); err == nil || !isConnectionError(err) {
		t.Fatalf("connection failure classification err=%v", err)
	}

	environment := mergedEnvironment(
		[]string{"B=old", "A=one", "invalid"},
		map[string]string{"B": "new", "C": "three"},
	)
	if strings.Join(environment, ",") != "A=one,B=new,C=three" {
		t.Fatalf("unexpected merged environment: %v", environment)
	}
	if commandExitCode(nil) != 0 || commandExitCode(errors.New("failure")) != 1 {
		t.Fatal("unexpected generic command exit mapping")
	}
	if warnings := endpointWarnings("http://127.0.0.1:4566"); len(warnings) != 0 {
		t.Fatalf("loopback endpoint warning: %v", warnings)
	}
	if warnings := endpointWarnings("http://192.0.2.10:4566"); len(warnings) != 1 {
		t.Fatalf("remote endpoint warning: %v", warnings)
	}
	if warnings := endpointWarnings("://"); warnings != nil {
		t.Fatalf("invalid endpoint warnings: %v", warnings)
	}

	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	base, err := skillInstallBase("")
	if err != nil || base != filepath.Join(codexHome, "skills") {
		t.Fatalf("skill base=%q err=%v", base, err)
	}
	explicit, err := skillInstallBase(filepath.Join(codexHome, "custom"))
	if err != nil || !filepath.IsAbs(explicit) {
		t.Fatalf("explicit skill base=%q err=%v", explicit, err)
	}

	if quoted := shellQuote("a'b"); quoted != `'a'"'"'b'` {
		t.Fatalf("unexpected shell quote: %s", quoted)
	}
	if mode := infoMode(nil); mode != os.FileMode(0) {
		t.Fatalf("nil info mode=%v", mode)
	}
}
