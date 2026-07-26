package state

import (
	"fmt"
	"strings"
)

// IntegrityMode controls when object bodies are checked against their
// committed SHA-256 metadata. Every mode verifies all referenced bodies when
// the store opens. Strict mode additionally verifies each body read while the
// process is running.
type IntegrityMode string

const (
	IntegrityModeStartup IntegrityMode = "startup"
	IntegrityModeStrict  IntegrityMode = "strict"
)

type OpenOptions struct {
	IntegrityMode IntegrityMode
}

func ParseIntegrityMode(value string) (IntegrityMode, error) {
	mode := IntegrityMode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		mode = IntegrityModeStartup
	}
	switch mode {
	case IntegrityModeStartup, IntegrityModeStrict:
		return mode, nil
	default:
		return "", fmt.Errorf("integrity mode must be %q or %q", IntegrityModeStartup, IntegrityModeStrict)
	}
}
