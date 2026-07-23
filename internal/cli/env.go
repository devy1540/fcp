package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

func runEnv(args []string, stdout, stderr io.Writer) int {
	service := "all"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		service = strings.ToLower(strings.TrimSpace(args[0]))
		args = args[1:]
	}
	flags := newFlagSet("env", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	gcpEndpoint := flags.String("gcp-endpoint", defaultGCPEndpoint, "FCP GCP gRPC endpoint")
	project := flags.String("project", "podo-local", "GCP project ID")
	credentials := flags.String("credentials", ".fcp/podo-local-credentials.json", "local service account credentials path")
	format := flags.String("format", "json", "json or shell")
	_ = flags.Bool("json", false, "emit JSON")
	if !parseFlags(flags, args, stderr) {
		return 2
	}
	if *format != "json" && *format != "shell" {
		writeCLIError(stderr, "env", "invalid_format", "format must be json or shell")
		return 2
	}
	variables, ok := podoEnvironment(service, strings.TrimRight(*endpoint, "/"), *gcpEndpoint, *project, *credentials)
	if !ok {
		writeCLIError(stderr, "env", "unknown_service", "service must be podo-backend, podo-notification, podo-app, or all")
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
		"service":       service,
		"variables":     variables,
	})
	return 0
}

func podoEnvironment(service, endpoint, gcpEndpoint, project, credentials string) (map[string]string, bool) {
	if service != "all" && service != "podo-backend" && service != "podo-notification" && service != "podo-app" {
		return nil, false
	}
	common := map[string]string{
		"FCP_HTTP_ENDPOINT":              endpoint,
		"FCP_GCP_ENDPOINT":               gcpEndpoint,
		"GCP_PROJECT_ID":                 project,
		"GOOGLE_CLOUD_PROJECT":           project,
		"GOOGLE_APPLICATION_CREDENTIALS": credentials,
		"STORAGE_EMULATOR_HOST":          endpoint,
		"PUBSUB_EMULATOR_HOST":           gcpEndpoint,
		"FIRESTORE_EMULATOR_HOST":        gcpEndpoint,
	}
	backend := map[string]string{
		"GCP_METADATA_BASE_URL":            endpoint + "/computeMetadata/v1",
		"AUTH_SYSTEM_IDENTITY_JWK_SET_URI": endpoint + "/oauth2/v3/certs",
		"GOOGLE_GEMINI_BASE_URL":           endpoint,
		"GOOGLE_API_KEY":                   "fcp-local",
		"GOOGLE_AI_API_KEY":                "fcp-local",
		"SPRING_AI_GOOGLE_GENAI_API_KEY":   "fcp-local",
	}
	notification := map[string]string{
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": "test",
		"AWS_REGION":            "ap-northeast-2",
		"AWS_ENDPOINT_URL":      endpoint,
		"PODO_STORE_PROVIDER":   "dynamo",
		"PODO_QUEUE_PROVIDER":   "pubsub",
	}
	app := map[string]string{
		"GCS_TUTOR_PROFILE_BUCKET":  "podo-tutor-profile",
		"GCS_ASSETS_PRIVATE_BUCKET": "podo-assets-private",
		"GCS_I18N_BUCKET":           "podo-assets",
	}
	result := map[string]string{}
	mergeEnvironment(result, common)
	switch service {
	case "podo-backend":
		mergeEnvironment(result, backend)
	case "podo-notification":
		mergeEnvironment(result, notification)
	case "podo-app":
		mergeEnvironment(result, app)
	case "all":
		mergeEnvironment(result, backend)
		mergeEnvironment(result, notification)
		mergeEnvironment(result, app)
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
