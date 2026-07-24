package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/devy1540/fcp/internal/state"
)

func runSnapshot(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeCLIError(stderr, "snapshot", "operation_required", "operation must be list, save, load, or delete")
		return 2
	}
	operation := strings.ToLower(strings.TrimSpace(args[0]))
	args = args[1:]
	name := ""
	if operation != "list" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name = strings.TrimSpace(args[0])
		args = args[1:]
	}
	flags := newFlagSet("snapshot", stderr)
	endpoint := flags.String("endpoint", defaultEndpoint, "FCP HTTP endpoint")
	timeout := flags.Duration("timeout", 10*time.Second, "request timeout")
	_ = flags.Bool("json", true, "emit JSON")
	if !parseFlags(flags, args, stderr) {
		return 2
	}
	if operation != "list" && operation != "save" && operation != "load" && operation != "delete" {
		writeCLIError(stderr, "snapshot", "invalid_operation", "operation must be list, save, load, or delete")
		return 2
	}
	if operation != "list" && name == "" {
		writeCLIError(stderr, "snapshot", "name_required", "snapshot name is required")
		return 2
	}
	if *timeout <= 0 || *timeout > time.Minute {
		writeCLIError(stderr, "snapshot", "invalid_timeout", "timeout must be greater than 0 and at most 1m")
		return 2
	}
	client, err := newAPIClient(*endpoint, *timeout)
	if err != nil {
		writeCLIError(stderr, "snapshot", "invalid_endpoint", err.Error())
		return 2
	}

	result := map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "snapshot",
		"operation":     operation,
		"ok":            true,
	}
	if operation == "list" {
		var response struct {
			Snapshots []state.SnapshotInfo `json:"snapshots"`
			Warning   string               `json:"warning"`
		}
		if err := client.getJSON("/_fcp/snapshots", &response); err != nil {
			return writeSnapshotCLIError(stderr, operation, err)
		}
		result["snapshots"] = response.Snapshots
		result["warning"] = response.Warning
	} else {
		var response struct {
			Snapshot *state.SnapshotInfo `json:"snapshot,omitempty"`
			Deleted  string              `json:"deleted,omitempty"`
		}
		if err := client.postJSON("/_fcp/snapshots", map[string]string{"operation": operation, "name": name}, &response); err != nil {
			return writeSnapshotCLIError(stderr, operation, err)
		}
		result["name"] = name
		if response.Snapshot != nil {
			result["snapshot"] = response.Snapshot
		}
		if response.Deleted != "" {
			result["deleted"] = response.Deleted
		}
	}
	_ = writeJSON(stdout, result)
	return 0
}

func writeSnapshotCLIError(stderr io.Writer, operation string, err error) int {
	writeCLIError(stderr, "snapshot", "request_failed", fmt.Sprintf("%s snapshot: %v", operation, err))
	return 1
}
