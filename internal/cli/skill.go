package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	fcpSkills "github.com/devy1540/fcp/skills"
)

const fcpSkillName = "fcp-local-cloud"

func runSkill(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "install" {
		writeCLIError(stderr, "skill", "missing_subcommand", "expected: skill install")
		return 2
	}
	flags := newFlagSet("skill install", stderr)
	target := flags.String("target", "", "skills directory; defaults to the Codex skills directory")
	_ = flags.Bool("json", true, "emit JSON")
	if !parseFlags(flags, args[1:], stderr) {
		return 2
	}
	base, err := skillInstallBase(*target)
	if err != nil {
		writeCLIError(stderr, "skill.install", "target_failed", err.Error())
		return 1
	}
	destination := filepath.Join(base, fcpSkillName)
	if _, err := os.Stat(destination); err == nil {
		writeCLIError(stderr, "skill.install", "already_exists", fmt.Sprintf("skill already exists at %s", destination))
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		writeCLIError(stderr, "skill.install", "target_failed", err.Error())
		return 1
	}
	installed, err := installEmbeddedSkill(destination)
	if err != nil {
		writeCLIError(stderr, "skill.install", "install_failed", err.Error())
		return 1
	}
	_ = writeJSON(stdout, map[string]any{
		"schemaVersion": schemaVersion,
		"command":       "skill.install",
		"ok":            true,
		"skill":         fcpSkillName,
		"path":          destination,
		"files":         installed,
	})
	return 0
}

func skillInstallBase(target string) (string, error) {
	if target != "" {
		return filepath.Abs(target)
	}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		return filepath.Join(codexHome, "skills"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".codex", "skills"), nil
}

func installEmbeddedSkill(destination string) ([]string, error) {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return nil, fmt.Errorf("create skill directory: %w", err)
	}
	installed := []string{}
	err := fs.WalkDir(fcpSkills.FCP, fcpSkillName, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(fcpSkillName, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := fcpSkills.FCP.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return err
		}
		installed = append(installed, relative)
		return nil
	})
	if err != nil {
		return installed, fmt.Errorf("copy embedded skill: %w", err)
	}
	return installed, nil
}
