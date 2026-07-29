package task

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	AgentHandoffFilename = "AGENT_HANDOFF.md"
	maxAgentHandoffBytes = 256 << 10
)

var replacementTokenPrefix = "RE" + "PLACE_"
var unresolvedHandoffPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b` + replacementTokenPrefix + `[A-Z0-9_]*\b`),
	regexp.MustCompile(`(?i)\b(?:TODO|TBD):\s*fill\b`),
	regexp.MustCompile(`\{\{\s*(?:PLACEHOLDER|` + replacementTokenPrefix + `[A-Z0-9_]*)\s*\}\}`),
	regexp.MustCompile(`\$\{\s*` + replacementTokenPrefix + `[A-Z0-9_]*\s*\}`),
	regexp.MustCompile(`<(?:TASK_ID|PROJECT_ID|PATCH_ID|` + replacementTokenPrefix + `[A-Z0-9_]*)>`),
}
var requiredHandoffHeadings = []string{"# AGENT_HANDOFF", "## TASK_IDENTITY", "## AUTHORITY", "## AGENT_ROLE", "## PROHIBITED_ACTIONS", "## PATCH_APPLICATION", "## REQUIRED_RUNTIME_GATES", "## REPAIR_POLICY", "## EVIDENCE_AND_COMMITS", "## TERMINAL_OUTPUT_PROTOCOL", "## RESPONSE_CONTRACT"}

type RuntimeHandoffContext struct {
	TaskID, ProjectID, Worktree, PackRoot, OwnerRequest, ResultPath, CompleteTaskPath, ResultBranch, BaseRevision, EvidenceDir string
	PatchApplied, PatchRepair                                                                                                  bool
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
	for _, p := range unresolvedHandoffPatterns {
		if match := p.FindString(text); match != "" {
			return nil, fmt.Errorf("%s contains unresolved placeholder %q", AgentHandoffFilename, match)
		}
	}
	for label, value := range map[string]string{"patch_id": manifest.PatchID, "repository": manifest.Target.Repository, "base_revision": manifest.Target.BaseRevision, "workflow_repository": manifest.Workflow.Repository, "workflow_version": manifest.Workflow.Version, "workflow_commit": manifest.Workflow.Commit, "workflow_document": manifest.Workflow.Document, "target_branch": manifest.Target.Branch} {
		if value == "" || !strings.Contains(text, value) {
			return nil, fmt.Errorf("%s does not identify manifest %s %q", AgentHandoffFilename, label, value)
		}
	}
	return []byte(text), nil
}
func validateHandoffHeadings(text string) error {
	seen := map[string]bool{}
	s := bufio.NewScanner(strings.NewReader(text))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		for _, h := range requiredHandoffHeadings {
			if line == h {
				seen[h] = true
			}
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	for _, h := range requiredHandoffHeadings {
		if !seen[h] {
			return fmt.Errorf("%s is missing required heading %q", AgentHandoffFilename, h)
		}
	}
	return nil
}
func WriteRuntimeHandoff(path string, source []byte, c RuntimeHandoffContext) error {
	state := "applied and exact scope verified"
	if c.PatchRepair {
		state = "automatic application failed; manual repair is allowed only inside manifest scope"
	} else if !c.PatchApplied {
		state = "no implementation payload was detected"
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

- Strict JSON result: %s
- Mandatory finalizer command: %s

The generated paths and command above are authoritative. Interactive session text does not complete the task. Write only the JSON result and invoke the exact command for succeeded, needs_gpt_revision, or failed.
`, c.TaskID, c.ProjectID, c.Worktree, c.ResultBranch, c.BaseRevision, c.PackRoot, c.OwnerRequest, state, c.EvidenceDir, c.ResultPath, c.CompleteTaskPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
