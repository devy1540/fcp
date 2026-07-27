package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/devy1540/fcp/internal/reliability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	defaultEndpoint    = "http://127.0.0.1:4566"
	defaultGCPEndpoint = "127.0.0.1:8085"
)

var requiredGCPGRPCServices = []string{
	"google.pubsub.v1.Publisher",
	"google.pubsub.v1.Subscriber",
	"google.firestore.v1.Firestore",
	"google.cloud.secretmanager.v1.SecretManagerService",
	"google.cloud.kms.v1.KeyManagementService",
	"google.iam.credentials.v1.IAMCredentials",
}

type checkResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Latency int64  `json:"latencyMs"`
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("doctor", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	gcpEndpoint := flags.String("gcp-endpoint", defaultGCPEndpoint, "FCP GCP gRPC endpoint")
	timeout := flags.Duration("timeout", 2*time.Second, "per-check timeout")
	_ = flags.Bool("json", true, "emit JSON")
	if !parseFlags(flags, args, stderr) {
		return 2
	}
	if *timeout <= 0 || *timeout > 30*time.Second {
		writeCLIError(stderr, "doctor", "invalid_timeout", "timeout must be greater than 0 and at most 30s")
		return 2
	}
	client, err := newAPIClient(*endpoint, *timeout)
	if err != nil {
		writeCLIError(stderr, "doctor", "invalid_endpoint", err.Error())
		return 2
	}

	warnings := endpointWarnings(*endpoint)
	checks := make([]checkResult, 0, 3)
	var health struct {
		Status string `json:"status"`
	}
	started := time.Now()
	err = client.getJSON("/_fcp/health", &health)
	healthOK := err == nil && health.Status == "ok"
	checks = append(checks, checkResult{Name: "http-health", OK: healthOK, Detail: checkDetail(err, health.Status), Latency: time.Since(started).Milliseconds()})

	var dashboard dashboardResponse
	started = time.Now()
	err = client.getJSON("/_fcp/dashboard?view=summary", &dashboard)
	dashboardOK := err == nil && dashboard.Summary.ServiceCount > 0
	checks = append(checks, checkResult{Name: "dashboard-summary", OK: dashboardOK, Detail: checkDetail(err, fmt.Sprintf("%d services", dashboard.Summary.ServiceCount)), Latency: time.Since(started).Milliseconds()})

	started = time.Now()
	connection, grpcErr := grpc.NewClient(*gcpEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	grpcStatus := ""
	if grpcErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		healthClient := grpc_health_v1.NewHealthClient(connection)
		for _, service := range requiredGCPGRPCServices {
			health, healthErr := healthClient.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: service})
			if healthErr != nil {
				grpcErr = fmt.Errorf("%s: %w", service, healthErr)
				break
			}
			if health.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
				grpcErr = fmt.Errorf("%s: gRPC health status is %s", service, health.GetStatus())
				break
			}
		}
		cancel()
		_ = connection.Close()
		if grpcErr == nil {
			grpcStatus = fmt.Sprintf("%d FCP services SERVING", len(requiredGCPGRPCServices))
		}
	}
	checks = append(checks, checkResult{Name: "gcp-grpc-health", OK: grpcErr == nil, Detail: checkDetail(grpcErr, grpcStatus), Latency: time.Since(started).Milliseconds()})

	ok := true
	for _, check := range checks {
		ok = ok && check.OK
	}
	result := map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "doctor",
		"ok":            ok,
		"endpoint":      client.endpoint,
		"gcpEndpoint":   *gcpEndpoint,
		"project":       dashboard.Project,
		"checks":        checks,
		"warnings":      warnings,
	}
	if dashboardOK {
		result["summary"] = dashboard.Summary
	}
	_ = writeJSON(stdout, result)
	if !ok {
		return 1
	}
	return 0
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("status", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	timeout := flags.Duration("timeout", 2*time.Second, "request timeout")
	_ = flags.Bool("json", true, "emit JSON")
	if !parseFlags(flags, args, stderr) {
		return 2
	}
	if !validRequestTimeout("status", *timeout, stderr) {
		return 2
	}
	client, err := newAPIClient(*endpoint, *timeout)
	if err != nil {
		writeCLIError(stderr, "status", "invalid_endpoint", err.Error())
		return 2
	}
	var dashboard dashboardResponse
	if err := client.getJSON("/_fcp/dashboard?view=summary", &dashboard); err != nil {
		writeCLIError(stderr, "status", "request_failed", err.Error())
		return 1
	}
	_ = writeJSON(stdout, map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "status",
		"ok":            true,
		"endpoint":      client.endpoint,
		"project":       dashboard.Project,
		"generatedAt":   dashboard.GeneratedAt,
		"summary":       dashboard.Summary,
		"services":      dashboard.Services,
	})
	return 0
}

func runResources(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		writeCLIError(stderr, "resources", "missing_subcommand", "expected: resources list")
		return 2
	}
	flags := newFlagSet("resources list", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	serviceID := flags.String("service", "", "service ID")
	provider := flags.String("provider", "", "AWS or GCP")
	query := flags.String("query", "", "resource search query")
	limit := flags.Int("limit", 25, "page size")
	offset := flags.Int("offset", 0, "page offset")
	timeout := flags.Duration("timeout", 2*time.Second, "request timeout")
	_ = flags.Bool("json", true, "emit JSON")
	if !parseFlags(flags, args[1:], stderr) {
		return 2
	}
	if !validRequestTimeout("resources", *timeout, stderr) {
		return 2
	}
	*serviceID = strings.ToLower(strings.TrimSpace(*serviceID))
	if *serviceID == "" {
		writeCLIError(stderr, "resources", "service_required", "--service is required")
		return 2
	}
	if *limit < 1 || *limit > 100 || *offset < 0 {
		writeCLIError(stderr, "resources", "invalid_page", "limit must be 1..100 and offset must be non-negative")
		return 2
	}
	if *provider != "" {
		*provider = strings.ToUpper(strings.TrimSpace(*provider))
		if *provider != "AWS" && *provider != "GCP" {
			writeCLIError(stderr, "resources", "invalid_provider", "provider must be AWS or GCP")
			return 2
		}
	}
	client, err := newAPIClient(*endpoint, *timeout)
	if err != nil {
		writeCLIError(stderr, "resources", "invalid_endpoint", err.Error())
		return 2
	}
	parameters := url.Values{}
	parameters.Set("view", "service")
	parameters.Set("service", *serviceID)
	parameters.Set("limit", strconv.Itoa(*limit))
	parameters.Set("offset", strconv.Itoa(*offset))
	if strings.TrimSpace(*query) != "" {
		parameters.Set("q", strings.TrimSpace(*query))
	}
	var dashboard dashboardResponse
	if err := client.getJSON("/_fcp/dashboard?"+parameters.Encode(), &dashboard); err != nil {
		writeCLIError(stderr, "resources", "request_failed", err.Error())
		return 1
	}
	service, found := dashboardServiceByID(dashboard.Services, *serviceID)
	if !found || dashboard.Page == nil {
		writeCLIError(stderr, "resources", "invalid_response", "service page is missing from the response")
		return 1
	}
	if *provider != "" && service.Provider != *provider {
		writeCLIError(stderr, "resources", "provider_mismatch", fmt.Sprintf("service %s belongs to %s", service.ID, service.Provider))
		return 2
	}
	_ = writeJSON(stdout, map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "resources.list",
		"ok":            true,
		"endpoint":      client.endpoint,
		"project":       dashboard.Project,
		"provider":      service.Provider,
		"service":       service.ID,
		"page":          dashboard.Page,
		"resources":     service.Resources,
	})
	return 0
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("verify", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	serviceID := flags.String("service", "", "service ID")
	strict := flags.Bool("strict", false, "fail when any selected operation is PARTIAL")
	timeout := flags.Duration("timeout", 3*time.Second, "request timeout")
	_ = flags.Bool("json", true, "emit JSON")
	if !parseFlags(flags, args, stderr) {
		return 2
	}
	if !validRequestTimeout("verify", *timeout, stderr) {
		return 2
	}
	*serviceID = strings.ToLower(strings.TrimSpace(*serviceID))
	client, err := newAPIClient(*endpoint, *timeout)
	if err != nil {
		writeCLIError(stderr, "verify", "invalid_endpoint", err.Error())
		return 2
	}

	serviceIDs := []string{}
	if *serviceID != "" {
		serviceIDs = append(serviceIDs, *serviceID)
	} else {
		var summary dashboardResponse
		if err := client.getJSON("/_fcp/dashboard?view=summary", &summary); err != nil {
			writeCLIError(stderr, "verify", "request_failed", err.Error())
			return 1
		}
		for _, service := range summary.Services {
			serviceIDs = append(serviceIDs, service.ID)
		}
	}

	verified := make([]dashboardService, 0, len(serviceIDs))
	fullCount, partialCount := 0, 0
	ok := true
	project := ""
	for _, id := range serviceIDs {
		parameters := url.Values{"view": {"service"}, "service": {id}, "limit": {"1"}}
		var dashboard dashboardResponse
		if err := client.getJSON("/_fcp/dashboard?"+parameters.Encode(), &dashboard); err != nil {
			writeCLIError(stderr, "verify", "request_failed", err.Error())
			return 1
		}
		project = dashboard.Project
		service, found := dashboardServiceByID(dashboard.Services, id)
		if !found || service.Status != "READY" || len(service.Verification.Operations) == 0 {
			ok = false
		}
		for _, operation := range service.Verification.Operations {
			switch operation.Status {
			case "FULL":
				fullCount++
			case "PARTIAL":
				partialCount++
			default:
				ok = false
			}
		}
		verified = append(verified, service)
	}
	if *strict && partialCount > 0 {
		ok = false
	}
	_ = writeJSON(stdout, map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "verify",
		"ok":            ok,
		"endpoint":      client.endpoint,
		"project":       project,
		"strict":        *strict,
		"scope":         "runtime-and-declared-compatibility",
		"note":          "This verifies the running FCP contract and declared regression evidence; it does not execute SDK suites or prove cloud parity.",
		"counts": map[string]int{
			"services": len(verified), "full": fullCount, "partial": partialCount,
		},
		"services": verified,
	})
	if !ok {
		return 1
	}
	return 0
}

func runReliability(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("reliability", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	minimum := flags.Int("minimum", reliability.RecommendedMinimum, "minimum accepted score")
	timeout := flags.Duration("timeout", 3*time.Second, "request timeout")
	_ = flags.Bool("json", true, "emit JSON")
	if !parseFlags(flags, args, stderr) {
		return 2
	}
	if !validRequestTimeout("reliability", *timeout, stderr) {
		return 2
	}
	if *minimum < 0 || *minimum > 100 {
		writeCLIError(stderr, "reliability", "invalid_minimum", "minimum must be between 0 and 100")
		return 2
	}
	client, err := newAPIClient(*endpoint, *timeout)
	if err != nil {
		writeCLIError(stderr, "reliability", "invalid_endpoint", err.Error())
		return 2
	}
	var dashboard dashboardResponse
	if err := client.getJSON("/_fcp/dashboard?view=reliability", &dashboard); err != nil {
		writeCLIError(stderr, "reliability", "request_failed", err.Error())
		return 1
	}
	if err := reliability.Validate(dashboard.Reliability); err != nil {
		writeCLIError(stderr, "reliability", "invalid_response", err.Error())
		return 1
	}
	runtimeReady := dashboard.Summary.ServiceCount > 0
	ok := runtimeReady && dashboard.Reliability.Score >= *minimum && len(dashboard.Reliability.AppliedCaps) == 0
	_ = writeJSON(stdout, map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "reliability",
		"ok":            ok,
		"endpoint":      client.endpoint,
		"project":       dashboard.Project,
		"minimum":       *minimum,
		"runtimeReady":  runtimeReady,
		"assessment":    dashboard.Reliability,
		"note":          "The score measures versioned local-emulator evidence, not the latest CI run or full AWS/GCP parity.",
	})
	if !ok {
		return 1
	}
	return 0
}

func validRequestTimeout(command string, timeout time.Duration, stderr io.Writer) bool {
	if timeout <= 0 || timeout > 30*time.Second {
		writeCLIError(stderr, command, "invalid_timeout", "timeout must be greater than 0 and at most 30s")
		return false
	}
	return true
}

func checkDetail(err error, success string) string {
	if err != nil {
		return err.Error()
	}
	return success
}

func endpointWarnings(endpoint string) []string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return []string{"FCP does not validate AWS credentials or SigV4; bind and access it only through a trusted local network."}
	}
	return nil
}
