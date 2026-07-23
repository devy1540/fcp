package skills

import "embed"

// FCP contains the distributable Codex skill bundled with the CLI.
//
//go:embed fcp-local-cloud/SKILL.md fcp-local-cloud/agents/openai.yaml
var FCP embed.FS
