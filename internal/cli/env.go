package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

func runEnv(args []string, stdout, stderr io.Writer) int {
	provider := "all"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		provider = strings.ToLower(strings.TrimSpace(args[0]))
		args = args[1:]
	}
	flags := newFlagSet("env", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	gcpEndpoint := flags.String("gcp-endpoint", defaultGCPEndpoint, "FCP GCP gRPC endpoint")
	project := flags.String("project", "fcp-local", "GCP project ID")
	credentials := flags.String("credentials", ".fcp/fcp-local-credentials.json", "local service account credentials path")
	format := flags.String("format", "json", "json or shell")
	_ = flags.Bool("json", false, "emit JSON")
	if !parseFlags(flags, args, stderr) {
		return 2
	}
	if *format != "json" && *format != "shell" {
		writeCLIError(stderr, "env", "invalid_format", "format must be json or shell")
		return 2
	}
	variables, ok := providerEnvironment(provider, strings.TrimRight(*endpoint, "/"), *gcpEndpoint, *project, *credentials)
	if !ok {
		writeCLIError(stderr, "env", "unknown_provider", "provider must be aws, gcp, or all")
		return 2
	}
	if *format == "shell" {
		keys := make([]string, 0, len(variables))
		for key := range variables {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(stdout, "export %s=%s\n", key, shellQuote(variables[key]))
		}
		return 0
	}
	_ = writeJSON(stdout, map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "env",
		"ok":            true,
		"provider":      provider,
		"variables":     variables,
	})
	return 0
}

func providerEnvironment(provider, endpoint, gcpEndpoint, project, credentials string) (map[string]string, bool) {
	if provider != "all" && provider != "aws" && provider != "gcp" {
		return nil, false
	}
	common := map[string]string{
		"FCP_HTTP_ENDPOINT": endpoint,
		"FCP_GCP_ENDPOINT":  gcpEndpoint,
	}
	aws := map[string]string{
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "test",
		"AWS_REGION":            "us-east-1",
		"AWS_ENDPOINT_URL":      endpoint,
	}
	gcp := map[string]string{
		"GCP_PROJECT_ID":                 project,
		"GOOGLE_CLOUD_PROJECT":           project,
		"GOOGLE_APPLICATION_CREDENTIALS": credentials,
		"STORAGE_EMULATOR_HOST":          endpoint,
		"PUBSUB_EMULATOR_HOST":           gcpEndpoint,
		"FIRESTORE_EMULATOR_HOST":        gcpEndpoint,
		"GCP_METADATA_BASE_URL":          endpoint + "/computeMetadata/v1",
		"GOOGLE_GEMINI_BASE_URL":         endpoint,
		"GOOGLE_API_KEY":                 "fcp-local",
		"GOOGLE_AI_API_KEY":              "fcp-local",
		"SPRING_AI_GOOGLE_GENAI_API_KEY": "fcp-local",
	}
	result := map[string]string{}
	mergeEnvironment(result, common)
	switch provider {
	case "aws":
		mergeEnvironment(result, aws)
	case "gcp":
		mergeEnvironment(result, gcp)
	case "all":
		mergeEnvironment(result, aws)
		mergeEnvironment(result, gcp)
	}
	return result, true
}

func mergeEnvironment(target, source map[string]string) {
	for key, value := range source {
		target[key] = value
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
