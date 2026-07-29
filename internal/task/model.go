package task

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	WorkflowVersion    = "v1.3.0"
	WorkflowCommit     = "f8cb8bc67c138f7e0e026c9270d3bd89dcd855d1"
	WorkflowDocument   = "GPT_REVIEW_PLANNER.md"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

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
	Summary              string           `json:"summary"`
	Details              []string         `json:"details"`
	Gates                []AgentGate      `json:"gates"`
	Deviations           []AgentDeviation `json:"deviations"`
	NextAction           string           `json:"next_action,omitempty"`
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
	var v Envelope
	if err := decodeStrict(path, &v); err != nil {
		return Envelope{}, err
	}
	if err := v.Validate(); err != nil {
		return Envelope{}, err
	}
	return v, nil
}
func LoadManifest(path string) (Manifest, error) {
	var v Manifest
	if err := decodeStrict(path, &v); err != nil {
		return Manifest{}, err
	}
	if err := v.Validate(); err != nil {
		return Manifest{}, err
	}
	return v, nil
}
func LoadAgentResult(path string) (AgentResult, error) {
	var v AgentResult
	if err := decodeStrict(path, &v); err != nil {
		return AgentResult{}, err
	}
	if err := v.Validate(); err != nil {
		return AgentResult{}, err
	}
	return v, nil
}

func decodeStrict(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("decode %s: trailing JSON data", path)
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(d); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}
func scanJSONValue(d *json.Decoder) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			kTok, err := d.Token()
			if err != nil {
				return err
			}
			k, ok := kTok.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if seen[k] {
				return fmt.Errorf("duplicate JSON key %q", k)
			}
			seen[k] = true
			if err := scanJSONValue(d); err != nil {
				return err
			}
		}
		close, err := d.Token()
		if err != nil {
			return err
		}
		if close != json.Delim('}') {
			return fmt.Errorf("invalid JSON object terminator")
		}
	case '[':
		for d.More() {
			if err := scanJSONValue(d); err != nil {
				return err
			}
		}
		close, err := d.Token()
		if err != nil {
			return err
		}
		if close != json.Delim(']') {
			return fmt.Errorf("invalid JSON array terminator")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
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
		return fmt.Errorf("target.base_revision must be a lowercase 40-character SHA")
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
	reqs := map[string]bool{}
	for _, r := range m.Requirements {
		if strings.TrimSpace(r.ID) == "" || reqs[r.ID] {
			return fmt.Errorf("requirement IDs must be non-empty and unique")
		}
		reqs[r.ID] = true
		if len(r.Acceptance) == 0 {
			return fmt.Errorf("requirement %s has no acceptance criteria", r.ID)
		}
	}
	gates := map[string]bool{}
	for _, g := range m.Gates {
		if strings.TrimSpace(g.ID) == "" || gates[g.ID] {
			return fmt.Errorf("gate IDs must be non-empty and unique")
		}
		gates[g.ID] = true
		switch g.Kind {
		case "command":
			if strings.TrimSpace(g.Command) == "" {
				return fmt.Errorf("command gate %s has no command", g.ID)
			}
		case "github-actions", "scope", "evidence":
		default:
			return fmt.Errorf("gate %s has unsupported kind %q", g.ID, g.Kind)
		}
	}
	if err := ValidateRelativePath(m.EvidenceDirectory); err != nil {
		return fmt.Errorf("evidence_directory: %w", err)
	}
	return nil
}

func (r AgentResult) Validate() error {
	if r.SchemaVersion != 2 || !config.ValidSlug(r.TaskID) {
		return fmt.Errorf("invalid agent result identity")
	}
	switch r.Status {
	case "succeeded", "failed", "needs_gpt_revision":
	default:
		return fmt.Errorf("unsupported agent result status %q", r.Status)
	}
	if strings.TrimSpace(r.Summary) == "" || len([]byte(r.Summary)) > 4096 {
		return fmt.Errorf("summary must contain 1 to 4096 UTF-8 bytes")
	}
	if len(r.Details) > 64 {
		return fmt.Errorf("details may contain at most 64 items")
	}
	for i, v := range r.Details {
		if strings.TrimSpace(v) == "" || len([]byte(v)) > 2048 {
			return fmt.Errorf("details[%d] must contain 1 to 2048 UTF-8 bytes", i)
		}
	}
	if len(r.Gates) > 128 {
		return fmt.Errorf("gates may contain at most 128 items")
	}
	seen := map[string]bool{}
	for i, g := range r.Gates {
		if strings.TrimSpace(g.ID) == "" || seen[g.ID] {
			return fmt.Errorf("gate %d has an empty or duplicate ID", i)
		}
		seen[g.ID] = true
		if g.Status != "pass" && g.Status != "fail" && g.Status != "not_run" {
			return fmt.Errorf("gate %s has invalid status", g.ID)
		}
		if strings.TrimSpace(g.Summary) == "" || len([]byte(g.Summary)) > 2048 {
			return fmt.Errorf("gate %s has invalid summary", g.ID)
		}
	}
	if len(r.Deviations) > 64 {
		return fmt.Errorf("deviations may contain at most 64 items")
	}
	for i, d := range r.Deviations {
		if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.Kind) == "" || strings.TrimSpace(d.Summary) == "" {
			return fmt.Errorf("deviation %d has empty required text", i)
		}
		if d.BehaviorChanged {
			return fmt.Errorf("agent result may not claim a behavior-changing deviation")
		}
		for _, req := range d.Requirements {
			if strings.TrimSpace(req) == "" {
				return fmt.Errorf("deviation %s has an empty requirement", d.ID)
			}
		}
	}
	if r.Status == "succeeded" {
		if !shaPattern.MatchString(r.ImplementationCommit) || !shaPattern.MatchString(r.EvidenceCommit) {
			return fmt.Errorf("successful result requires implementation and evidence commits")
		}
	}
	if r.Status == "needs_gpt_revision" && (strings.TrimSpace(r.NextAction) == "" || len([]byte(r.NextAction)) > 2048) {
		return fmt.Errorf("needs_gpt_revision requires next_action")
	}
	if r.NextAction != "" && len([]byte(r.NextAction)) > 2048 {
		return fmt.Errorf("next_action exceeds 2048 UTF-8 bytes")
	}
	return nil
}

func (r AgentResult) ValidateAgainst(m Manifest) error {
	if r.Status != "succeeded" {
		return nil
	}
	if len(r.Gates) != len(m.Gates) {
		return fmt.Errorf("successful result gate count mismatch")
	}
	for i, g := range m.Gates {
		actual := r.Gates[i]
		if actual.ID != g.ID {
			return fmt.Errorf("successful result gate order mismatch at %d: expected %s, got %s", i, g.ID, actual.ID)
		}
		if actual.Status != "pass" || actual.Exit != 0 {
			return fmt.Errorf("gate %s did not pass", actual.ID)
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
func CompareScope(m Manifest, a gitx.Scope) error {
	for label, v := range map[string]struct{ Expected, Actual []string }{"created": {m.FilesCreated, a.Created}, "modified": {m.FilesModified, a.Modified}, "deleted": {m.FilesDeleted, a.Deleted}} {
		e := sortedCopy(v.Expected)
		x := sortedCopy(v.Actual)
		if strings.Join(e, "\x00") != strings.Join(x, "\x00") {
			return fmt.Errorf("%s scope mismatch: expected %v, got %v", label, e, x)
		}
	}
	return nil
}
func validateScopeSets(sets ...[]string) error {
	seen := map[string]string{}
	labels := []string{"created", "modified", "deleted"}
	for i, values := range sets {
		for _, v := range values {
			if err := ValidateRelativePath(v); err != nil {
				return fmt.Errorf("%s path: %w", labels[i], err)
			}
			if prior, ok := seen[v]; ok {
				return fmt.Errorf("path %s appears in both %s and %s", v, prior, labels[i])
			}
			seen[v] = labels[i]
		}
	}
	return nil
}
func sortedCopy(values []string) []string {
	r := append([]string(nil), values...)
	sort.Strings(r)
	return r
}
