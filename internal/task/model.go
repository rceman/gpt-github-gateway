package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/config"
	"github.com/rceman/gpt-github-gateway/internal/gitx"
)

const (
	WorkflowRepository = "https://github.com/rceman/gpt-review-planner"
	WorkflowVersion    = "v1.2.0"
	WorkflowCommit     = "07ab94b358e8634fa0e547acaa0cf6e237ebbc2e"
	WorkflowDocument   = "GPT_REVIEW_PLANNER.md"
)

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type Envelope struct {
	SchemaVersion    int    `json:"schema_version"`
	TaskID           string `json:"task_id"`
	GatewayID        string `json:"gateway_id"`
	ProjectID        string `json:"project_id"`
	Operation        string `json:"operation"`
	SubmittedAt      string `json:"submitted_at"`
	RequestPath      string `json:"request_path"`
	PatchPackPath    string `json:"patch_pack_path"`
	ResultBranch     string `json:"result_branch"`
	ApprovalRequired bool   `json:"approval_required"`
}

type Manifest struct {
	SchemaVersion                int           `json:"schema_version"`
	PatchID                      string        `json:"patch_id"`
	Title                        string        `json:"title"`
	Description                  string        `json:"description"`
	CreatedAt                    string        `json:"created_at"`
	PatchTimestamp               string        `json:"patch_timestamp"`
	PatchSlug                    string        `json:"patch_slug"`
	BaselineRelease              string        `json:"baseline_release"`
	EvidenceDirectory            string        `json:"evidence_directory"`
	Workflow                     WorkflowPin   `json:"workflow"`
	Target                       Target        `json:"target"`
	FilesCreated                 []string      `json:"files_created"`
	FilesModified                []string      `json:"files_modified"`
	FilesDeleted                 []string      `json:"files_deleted"`
	Requirements                 []Requirement `json:"requirements"`
	Gates                        []Gate        `json:"gates"`
	GPTStaticChecksPerformed     []string      `json:"gpt_static_checks_performed"`
	GPTRuntimeChecksNotPerformed []string      `json:"gpt_runtime_checks_not_performed"`
	KnownIntegrationRisks        []string      `json:"known_integration_risks"`
	ForbiddenDeviations          []string      `json:"forbidden_deviations"`
}

type WorkflowPin struct {
	Repository string `json:"repository"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Document   string `json:"document"`
}

type Target struct {
	Repository   string `json:"repository"`
	Branch       string `json:"branch"`
	BaseRevision string `json:"base_revision"`
}

type Requirement struct {
	ID         string   `json:"id"`
	Summary    string   `json:"summary"`
	Acceptance []string `json:"acceptance"`
	AllowNA    bool     `json:"allow_na,omitempty"`
}

type Gate struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Command  string `json:"command,omitempty"`
	Workflow string `json:"workflow,omitempty"`
	Head     string `json:"head,omitempty"`
}

type AgentResult struct {
	SchemaVersion        int              `json:"schema_version"`
	TaskID               string           `json:"task_id"`
	Status               string           `json:"status"`
	ImplementationCommit string           `json:"implementation_commit,omitempty"`
	EvidenceCommit       string           `json:"evidence_commit,omitempty"`
	Gates                []AgentGate      `json:"gates"`
	Deviations           []AgentDeviation `json:"deviations"`
	Summary              string           `json:"summary"`
}

type AgentGate struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Exit    int    `json:"exit"`
	Summary string `json:"summary"`
}

type AgentDeviation struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Summary         string   `json:"summary"`
	Workaround      string   `json:"workaround"`
	ScopeChanged    bool     `json:"scope_changed"`
	BehaviorChanged bool     `json:"behavior_changed"`
	Requirements    []string `json:"requirements"`
}

type Status struct {
	SchemaVersion int       `json:"schema_version"`
	TaskID        string    `json:"task_id"`
	GatewayID     string    `json:"gateway_id"`
	ProjectID     string    `json:"project_id"`
	State         string    `json:"state"`
	UpdatedAt     time.Time `json:"updated_at"`
	Message       string    `json:"message,omitempty"`
	Worktree      string    `json:"worktree,omitempty"`
	ResultBranch  string    `json:"result_branch,omitempty"`
}

type Approval struct {
	SchemaVersion int       `json:"schema_version"`
	TaskID        string    `json:"task_id"`
	Decision      string    `json:"decision"`
	Reason        string    `json:"reason,omitempty"`
	DecidedAt     time.Time `json:"decided_at"`
}

func LoadEnvelope(path string) (Envelope, error) {
	var value Envelope
	if err := decodeStrict(path, &value); err != nil {
		return Envelope{}, err
	}
	if err := value.Validate(); err != nil {
		return Envelope{}, err
	}
	return value, nil
}

func LoadManifest(path string) (Manifest, error) {
	var value Manifest
	if err := decodeStrict(path, &value); err != nil {
		return Manifest{}, err
	}
	if err := value.Validate(); err != nil {
		return Manifest{}, err
	}
	return value, nil
}

func LoadAgentResult(path string) (AgentResult, error) {
	var value AgentResult
	if err := decodeStrict(path, &value); err != nil {
		return AgentResult{}, err
	}
	if err := value.Validate(); err != nil {
		return AgentResult{}, err
	}
	return value, nil
}

func decodeStrict(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (e Envelope) Validate() error {
	if e.SchemaVersion != 1 {
		return fmt.Errorf("unsupported task schema_version %d", e.SchemaVersion)
	}
	if !config.ValidSlug(e.TaskID) || !config.ValidSlug(e.GatewayID) || !config.ValidSlug(e.ProjectID) {
		return fmt.Errorf("task, gateway, and project IDs must be safe slugs")
	}
	if e.Operation != "apply_patch_pack" {
		return fmt.Errorf("unsupported operation %q", e.Operation)
	}
	if _, err := time.Parse(time.RFC3339, e.SubmittedAt); err != nil {
		return fmt.Errorf("submitted_at must be RFC3339: %w", err)
	}
	for label, value := range map[string]string{"request_path": e.RequestPath, "patch_pack_path": e.PatchPackPath} {
		if err := ValidateRelativePath(value); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	if !strings.HasPrefix(e.ResultBranch, "agent/") || strings.ContainsAny(e.ResultBranch, "\x00\r\n ~^:?*[\\") {
		return fmt.Errorf("result_branch must be a safe agent/ branch")
	}
	return nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != 2 {
		return fmt.Errorf("unsupported patch manifest schema_version %d", m.SchemaVersion)
	}
	if m.Workflow.Repository != WorkflowRepository || m.Workflow.Version != WorkflowVersion || m.Workflow.Commit != WorkflowCommit || m.Workflow.Document != WorkflowDocument {
		return fmt.Errorf("patch manifest does not use the required immutable gpt-review-planner pin")
	}
	if !shaPattern.MatchString(m.Target.BaseRevision) {
		return fmt.Errorf("target.base_revision must be a 40-character SHA")
	}
	if strings.TrimSpace(m.Target.Repository) == "" || strings.TrimSpace(m.Target.Branch) == "" {
		return fmt.Errorf("target repository and branch are required")
	}
	if len(m.Requirements) == 0 || len(m.Gates) == 0 {
		return fmt.Errorf("manifest requires at least one requirement and gate")
	}
	if err := validateScopeSets(m.FilesCreated, m.FilesModified, m.FilesDeleted); err != nil {
		return err
	}
	seenRequirements := map[string]bool{}
	for _, requirement := range m.Requirements {
		if strings.TrimSpace(requirement.ID) == "" || seenRequirements[requirement.ID] {
			return fmt.Errorf("requirement IDs must be non-empty and unique")
		}
		seenRequirements[requirement.ID] = true
		if len(requirement.Acceptance) == 0 {
			return fmt.Errorf("requirement %s has no acceptance criteria", requirement.ID)
		}
	}
	seenGates := map[string]bool{}
	for _, gate := range m.Gates {
		if strings.TrimSpace(gate.ID) == "" || seenGates[gate.ID] {
			return fmt.Errorf("gate IDs must be non-empty and unique")
		}
		seenGates[gate.ID] = true
		switch gate.Kind {
		case "command":
			if strings.TrimSpace(gate.Command) == "" {
				return fmt.Errorf("command gate %s has no command", gate.ID)
			}
		case "github-actions", "scope", "evidence":
		default:
			return fmt.Errorf("gate %s has unsupported kind %q", gate.ID, gate.Kind)
		}
	}
	if err := ValidateRelativePath(m.EvidenceDirectory); err != nil {
		return fmt.Errorf("evidence_directory: %w", err)
	}
	return nil
}

func (r AgentResult) Validate() error {
	if r.SchemaVersion != 1 || !config.ValidSlug(r.TaskID) {
		return fmt.Errorf("invalid agent result identity")
	}
	switch r.Status {
	case "succeeded", "failed", "needs_gpt_revision":
	default:
		return fmt.Errorf("unsupported agent result status %q", r.Status)
	}
	if r.Status == "succeeded" {
		if !shaPattern.MatchString(r.ImplementationCommit) || !shaPattern.MatchString(r.EvidenceCommit) {
			return fmt.Errorf("successful result requires implementation and evidence commits")
		}
	}
	for _, deviation := range r.Deviations {
		if deviation.BehaviorChanged {
			return fmt.Errorf("agent result may not claim a behavior-changing deviation")
		}
	}
	return nil
}

func (r AgentResult) ValidateAgainst(manifest Manifest) error {
	if r.Status != "succeeded" {
		return nil
	}
	expected := map[string]bool{}
	for _, gate := range manifest.Gates {
		expected[gate.ID] = true
	}
	seen := map[string]bool{}
	for _, gate := range r.Gates {
		if !expected[gate.ID] || seen[gate.ID] {
			return fmt.Errorf("unexpected or duplicate agent gate %q", gate.ID)
		}
		if gate.Status != "pass" || gate.Exit != 0 {
			return fmt.Errorf("gate %s did not pass", gate.ID)
		}
		seen[gate.ID] = true
	}
	for id := range expected {
		if !seen[id] {
			return fmt.Errorf("missing agent gate result %q", id)
		}
	}
	return nil
}

func ValidateRelativePath(value string) error {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("unsafe relative path %q", value)
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean != value || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("path is not normalized: %q", value)
	}
	return nil
}

func ResolveInside(root, relative string) (string, error) {
	if err := ValidateRelativePath(relative); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	prefix := rootAbs + string(filepath.Separator)
	if resolved != rootAbs && !strings.HasPrefix(resolved, prefix) {
		return "", fmt.Errorf("path escapes task root")
	}
	return resolved, nil
}

func CompareScope(manifest Manifest, actual gitx.Scope) error {
	for label, value := range map[string]struct {
		Expected []string
		Actual   []string
	}{
		"created":  {manifest.FilesCreated, actual.Created},
		"modified": {manifest.FilesModified, actual.Modified},
		"deleted":  {manifest.FilesDeleted, actual.Deleted},
	} {
		expected := sortedCopy(value.Expected)
		actualPaths := sortedCopy(value.Actual)
		if strings.Join(expected, "\x00") != strings.Join(actualPaths, "\x00") {
			return fmt.Errorf("%s scope mismatch: expected %v, got %v", label, expected, actualPaths)
		}
	}
	return nil
}

func validateScopeSets(sets ...[]string) error {
	seen := map[string]string{}
	labels := []string{"created", "modified", "deleted"}
	for index, values := range sets {
		for _, value := range values {
			if err := ValidateRelativePath(value); err != nil {
				return fmt.Errorf("%s path: %w", labels[index], err)
			}
			if prior, exists := seen[value]; exists {
				return fmt.Errorf("path %s appears in both %s and %s", value, prior, labels[index])
			}
			seen[value] = labels[index]
		}
	}
	return nil
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
