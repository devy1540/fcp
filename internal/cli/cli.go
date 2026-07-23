package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const schemaVersion = "fcp.cli/v1"

// IsCommand reports whether args select the AI-friendly CLI rather than the
// backwards-compatible server flags.
func IsCommand(args []string) bool {
	return len(args) > 0 && !strings.HasPrefix(args[0], "-")
}

// Run executes a machine-readable FCP CLI command and returns its exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stdout)
		return 0
	}
	switch args[0] {
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "env":
		return runEnv(args[1:], stdout, stderr)
	case "resources":
		return runResources(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "skill":
		return runSkill(args[1:], stdout, stderr)
	case "help":
		writeUsage(stdout)
		return 0
	default:
		writeCLIError(stderr, args[0], "unknown_command", "unknown FCP command")
		return 2
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, `FCP AI-friendly commands:
  fcp doctor [--endpoint URL] [--gcp-endpoint HOST:PORT] --json
  fcp status [--endpoint URL] --json
  fcp env [podo-backend|podo-notification|podo-app|all] [--format json|shell]
  fcp resources list --service ID [--provider AWS|GCP] [--query TEXT] --json
  fcp verify [--service ID] [--strict] --json
  fcp skill install [--target DIRECTORY]

Run fcp without a subcommand to start the local emulator.`)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {}
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string, stderr io.Writer) bool {
	if err := flags.Parse(args); err != nil {
		writeCLIError(stderr, flags.Name(), "invalid_arguments", err.Error())
		return false
	}
	if flags.NArg() != 0 {
		writeCLIError(stderr, flags.Name(), "unexpected_arguments", strings.Join(flags.Args(), " "))
		return false
	}
	return true
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeCLIError(output io.Writer, command, code, message string) {
	_ = writeJSON(output, map[string]any{
		"schemaVersion": schemaVersion,
		"command":       command,
		"ok":            false,
		"error": map[string]string{
			"code": code, "message": message,
		},
	})
}

type apiClient struct {
	endpoint string
	http     *http.Client
}

func newAPIClient(endpoint string, timeout time.Duration) (*apiClient, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("endpoint must be an HTTP or HTTPS URL")
	}
	return &apiClient{endpoint: endpoint, http: &http.Client{Timeout: timeout}}, nil
}

func (c *apiClient) getJSON(path string, target any) error {
	request, err := http.NewRequest(http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body)
		message := firstNonEmpty(body.Message, body.Error, response.Status)
		return fmt.Errorf("GET %s: %s", path, message)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown error"
}

func isConnectionError(err error) bool {
	var urlError *url.Error
	return errors.As(err, &urlError)
}

type dashboardResponse struct {
	Project     string             `json:"project"`
	GeneratedAt time.Time          `json:"generatedAt"`
	Summary     dashboardSummary   `json:"summary"`
	Services    []dashboardService `json:"services"`
	Page        *dashboardPage     `json:"page,omitempty"`
}

type dashboardSummary struct {
	ServiceCount          int `json:"serviceCount"`
	AWSServiceCount       int `json:"awsServiceCount"`
	GCPServiceCount       int `json:"gcpServiceCount"`
	SDKVerifiedCount      int `json:"sdkVerifiedCount"`
	ContractVerifiedCount int `json:"contractVerifiedCount"`
	ResourceCount         int `json:"resourceCount"`
	MessageCount          int `json:"messageCount"`
}

type dashboardService struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Provider      string                `json:"provider"`
	Description   string                `json:"description"`
	Status        string                `json:"status"`
	Verification  dashboardVerification `json:"verification"`
	ResourceCount int                   `json:"resourceCount"`
	Resources     []dashboardResource   `json:"resources,omitempty"`
}

type dashboardVerification struct {
	Level       string                           `json:"level"`
	Label       string                           `json:"label"`
	Evidence    string                           `json:"evidence"`
	Source      string                           `json:"source"`
	Operations  []dashboardOperationVerification `json:"operations,omitempty"`
	Limitations []string                         `json:"limitations,omitempty"`
}

type dashboardOperationVerification struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Scope  string `json:"scope"`
}

type dashboardResource struct {
	Name       string               `json:"name"`
	Kind       string               `json:"kind"`
	Status     string               `json:"status"`
	CreatedAt  time.Time            `json:"createdAt,omitzero"`
	UpdatedAt  time.Time            `json:"updatedAt,omitzero"`
	Attributes []dashboardAttribute `json:"attributes,omitempty"`
}

type dashboardAttribute struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type dashboardPage struct {
	Service     string `json:"service"`
	Query       string `json:"query,omitempty"`
	Total       int    `json:"total"`
	Offset      int    `json:"offset"`
	Limit       int    `json:"limit"`
	HasPrevious bool   `json:"hasPrevious"`
	HasNext     bool   `json:"hasNext"`
}

func dashboardServiceByID(services []dashboardService, id string) (dashboardService, bool) {
	for _, service := range services {
		if service.ID == id {
			return service, true
		}
	}
	return dashboardService{}, false
}
