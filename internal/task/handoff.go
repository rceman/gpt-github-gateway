package task

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	AgentHandoffFilename  = "AGENT_HANDOFF.md"
	AgentResponseFilename = "AGENT_RESPONSE.md"
	maxAgentHandoffBytes  = 256 << 10
)

var requiredHandoffHeadings = []string{
	"# AGENT_HANDOFF",
	"## TASK_IDENTITY",
	"## AUTHORITY",
	"## AGENT_ROLE",
	"## PROHIBITED_ACTIONS",
	"## PATCH_APPLICATION",
	"## REQUIRED_RUNTIME_GATES",
	"## REPAIR_POLICY",
	"## EVIDENCE_AND_COMMITS",
	"## RESPONSE_CONTRACT",
}

type RuntimeHandoffContext struct {
	TaskID       string
	ProjectID    string
	Worktree     string
	PackRoot     string
	OwnerRequest string
	ResponsePath string
	ResultPath   string
	ResultBranch string
	BaseRevision string
	EvidenceDir  string
	PatchApplied bool
	PatchRepair  bool
}

func LoadAgentHandoff(packRoot string, manifest Manifest) ([]byte, error) {
	path := filepath.Join(packRoot, AgentHandoffFilename)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("missing required %s: %w", AgentHandoffFilename, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a regular non-symlink file", AgentHandoffFilename)
	}
	if info.Size() == 0 || info.Size() > maxAgentHandoffBytes {
		return nil, fmt.Errorf("%s must be between 1 byte and %d bytes", AgentHandoffFilename, maxAgentHandoffBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", AgentHandoffFilename, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%s contains a NUL byte", AgentHandoffFilename)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if err := validateHandoffHeadings(text); err != nil {
		return nil, err
	}
	for _, marker := range []string{"REPLACE_", "TODO: fill", "TBD: fill"} {
		if strings.Contains(text, marker) {
			return nil, fmt.Errorf("%s contains unresolved placeholder %q", AgentHandoffFilename, marker)
		}
	}
	if strings.ContainsAny(text, "<>") {
		return nil, fmt.Errorf("%s contains angle-bracket placeholder syntax", AgentHandoffFilename)
	}
	for label, value := range map[string]string{
		"patch_id":            manifest.PatchID,
		"repository":          manifest.Target.Repository,
		"base_revision":       manifest.Target.BaseRevision,
		"workflow_repository": manifest.Workflow.Repository,
		"workflow_version":    manifest.Workflow.Version,
		"workflow_commit":     manifest.Workflow.Commit,
		"workflow_document":   manifest.Workflow.Document,
		"target_branch":       manifest.Target.Branch,
	} {
		if value == "" || !strings.Contains(text, value) {
			return nil, fmt.Errorf("%s does not identify manifest %s %q", AgentHandoffFilename, label, value)
		}
	}
	return []byte(text), nil
}

func validateHandoffHeadings(text string) error {
	seen := make(map[string]bool, len(requiredHandoffHeadings))
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		for _, heading := range requiredHandoffHeadings {
			if line == heading {
				seen[heading] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", AgentHandoffFilename, err)
	}
	for _, heading := range requiredHandoffHeadings {
		if !seen[heading] {
			return fmt.Errorf("%s is missing required heading %q", AgentHandoffFilename, heading)
		}
	}
	return nil
}

func WriteRuntimeHandoff(path string, source []byte, context RuntimeHandoffContext) error {
	patchState := "applied and exact scope verified"
	if context.PatchRepair {
		patchState = "automatic application failed; manual repair is allowed only inside manifest scope"
	} else if !context.PatchApplied {
		patchState = "no implementation payload was detected"
	}
	content := strings.TrimRight(string(source), "\n") + fmt.Sprintf(`

## GATEWAY_RUNTIME_CONTEXT

- Task ID: %s
- Project ID: %s
- Worktree: %s
- Result branch: %s
- Base revision: %s
- Patch pack: %s
- Owner request: %s
- Patch state: %s
- Evidence directory: %s

## GATEWAY_REQUIRED_OUTPUTS

- Human response: %s
- Machine result: %s

The gateway-generated paths above are authoritative for this execution. Do not write result files elsewhere.
`, context.TaskID, context.ProjectID, context.Worktree, context.ResultBranch, context.BaseRevision, context.PackRoot, context.OwnerRequest, patchState, context.EvidenceDir, context.ResponsePath, context.ResultPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
