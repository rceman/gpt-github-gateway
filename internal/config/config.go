package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	SchemaVersion           = 2
	DefaultConfigRelPath    = ".config/gpt-github-gateway/config.json"
	DefaultPollSeconds      = 10
	DefaultTimeoutSeconds   = 7200
	DefaultListen           = "127.0.0.1:8787"
	DefaultAirelayBinary    = "airelay"
	DefaultTemplateBranch   = "main"
	DefaultControlPattern   = "gateway/{gateway_id}"
	DefaultProjectPattern   = "project/{gateway_id}/{project_id}"
	DefaultHeartbeatSeconds = 600
	DefaultLeaseSeconds     = 1500
	ExecutionModeAuto       = "auto"
	ExecutionModeManual     = "manual"
	ExecutionModeDisabled   = "disabled"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

type Config struct {
	SchemaVersion int                      `json:"schema_version"`
	Gateway       GatewayConfig            `json:"gateway"`
	Bus           BusConfig                `json:"bus"`
	Server        ServerConfig             `json:"server"`
	Airelay       AirelayConfig            `json:"airelay"`
	Projects      map[string]ProjectConfig `json:"projects"`
}

type GatewayConfig struct {
	ID                    string `json:"id"`
	PollIntervalSeconds   int    `json:"poll_interval_seconds"`
	AgentTimeoutSeconds   int    `json:"agent_timeout_seconds"`
	AllowPatchRepair      bool   `json:"allow_patch_repair"`
	TaskExecutionMode     string `json:"task_execution_mode,omitempty"`
	MaxTaskFileBytes      int64  `json:"max_task_file_bytes,omitempty"`
	MaxTaskAggregateBytes int64  `json:"max_task_aggregate_bytes,omitempty"`
}

type BusConfig struct {
	Repository               string `json:"repository"`
	URL                      string `json:"url"`
	TemplateBranch           string `json:"template_branch"`
	ControlBranchPattern     string `json:"control_branch_pattern"`
	ProjectBranchPattern     string `json:"project_branch_pattern"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
	LeaseDurationSeconds     int    `json:"lease_duration_seconds"`
}

type ServerConfig struct {
	Listen string `json:"listen"`
}

type AirelayConfig struct {
	Binary string `json:"binary"`
}

type ProjectConfig struct {
	Path            string   `json:"path"`
	Repository      string   `json:"repository"`
	DefaultBranch   string   `json:"default_branch"`
	AirelayProfile  string   `json:"airelay_profile"`
	SessionKey      string   `json:"session_key"`
	ResumeSessionID string   `json:"resume_session_id,omitempty"`
	LaunchArgs      []string `json:"launch_args,omitempty"`
}

type Layout struct {
	Root           string
	BusDir         string
	BusMirrorDir   string
	BusControlDir  string
	BusProjectsDir string
	PIDFile        string
	LogFile        string
	LockFile       string
	GatewayID      string
	ConfigPath     string
}

func DefaultConfigPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("GPT_GITHUB_GATEWAY_CONFIG")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(DefaultConfigRelPath)), nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	if header.SchemaVersion == 1 {
		return nil, fmt.Errorf("legacy schema_version 1 uses a single bus.branch; run scripts/migrate-bus-multibranch.py before starting gateway 0.3.0")
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("decode config %s: trailing JSON data", path)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}
	return &cfg, nil
}

func Save(path string, cfg *Config) error {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temp := path + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func (c *Config) ApplyDefaults() {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if c.Gateway.PollIntervalSeconds == 0 {
		c.Gateway.PollIntervalSeconds = DefaultPollSeconds
	}
	if c.Gateway.AgentTimeoutSeconds == 0 {
		c.Gateway.AgentTimeoutSeconds = DefaultTimeoutSeconds
	}
	if strings.TrimSpace(c.Gateway.TaskExecutionMode) == "" {
		c.Gateway.TaskExecutionMode = ExecutionModeAuto
	}
	if c.Gateway.MaxTaskFileBytes == 0 {
		c.Gateway.MaxTaskFileBytes = 20 << 20
	}
	if c.Gateway.MaxTaskAggregateBytes == 0 {
		c.Gateway.MaxTaskAggregateBytes = 100 << 20
	}
	if strings.TrimSpace(c.Bus.TemplateBranch) == "" {
		c.Bus.TemplateBranch = DefaultTemplateBranch
	}
	if strings.TrimSpace(c.Bus.ControlBranchPattern) == "" {
		c.Bus.ControlBranchPattern = DefaultControlPattern
	}
	if strings.TrimSpace(c.Bus.ProjectBranchPattern) == "" {
		c.Bus.ProjectBranchPattern = DefaultProjectPattern
	}
	if c.Bus.HeartbeatIntervalSeconds == 0 {
		c.Bus.HeartbeatIntervalSeconds = DefaultHeartbeatSeconds
	}
	if c.Bus.LeaseDurationSeconds == 0 {
		c.Bus.LeaseDurationSeconds = DefaultLeaseSeconds
	}
	if strings.TrimSpace(c.Server.Listen) == "" {
		c.Server.Listen = DefaultListen
	}
	if strings.TrimSpace(c.Airelay.Binary) == "" {
		c.Airelay.Binary = DefaultAirelayBinary
	}
	if c.Projects == nil {
		c.Projects = map[string]ProjectConfig{}
	}
	for id, project := range c.Projects {
		if strings.TrimSpace(project.DefaultBranch) == "" {
			project.DefaultBranch = "main"
		}
		if strings.TrimSpace(project.AirelayProfile) == "" {
			project.AirelayProfile = "codex"
		}
		if strings.TrimSpace(project.SessionKey) == "" {
			project.SessionKey = id + "_master"
		}
		c.Projects[id] = project
	}
}

func (c *Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", c.SchemaVersion)
	}
	if !ValidSlug(c.Gateway.ID) {
		return errors.New("gateway.id must be a lowercase slug using letters, digits, underscore, or hyphen")
	}
	if c.Gateway.PollIntervalSeconds < 1 || c.Gateway.PollIntervalSeconds > 3600 {
		return errors.New("gateway.poll_interval_seconds must be between 1 and 3600")
	}
	if c.Gateway.AgentTimeoutSeconds < 30 || c.Gateway.AgentTimeoutSeconds > 86400 {
		return errors.New("gateway.agent_timeout_seconds must be between 30 and 86400")
	}
	switch c.Gateway.TaskExecutionMode {
	case ExecutionModeAuto, ExecutionModeManual, ExecutionModeDisabled:
	default:
		return errors.New("gateway.task_execution_mode must be auto, manual, or disabled")
	}
	if c.Gateway.MaxTaskFileBytes < 1024 || c.Gateway.MaxTaskAggregateBytes < c.Gateway.MaxTaskFileBytes {
		return errors.New("invalid task size limits")
	}
	if !validRepository(c.Bus.Repository) {
		return errors.New("bus.repository must use owner/name form")
	}
	if strings.TrimSpace(c.Bus.URL) == "" {
		return errors.New("bus.url is required")
	}
	if err := ValidateBranchName(c.Bus.TemplateBranch); err != nil {
		return fmt.Errorf("bus.template_branch: %w", err)
	}
	if c.Bus.HeartbeatIntervalSeconds < 60 || c.Bus.HeartbeatIntervalSeconds > 86400 {
		return errors.New("bus.heartbeat_interval_seconds must be between 60 and 86400")
	}
	if c.Bus.LeaseDurationSeconds <= 2*c.Bus.HeartbeatIntervalSeconds || c.Bus.LeaseDurationSeconds > 172800 {
		return errors.New("bus.lease_duration_seconds must be greater than two heartbeat intervals and no more than 172800")
	}
	control, err := ExpandBranchPattern(c.Bus.ControlBranchPattern, c.Gateway.ID, "")
	if err != nil {
		return fmt.Errorf("bus.control_branch_pattern: %w", err)
	}
	if control == c.Bus.TemplateBranch {
		return errors.New("control branch must differ from template branch")
	}
	if c.Server.Listen != DefaultListen && !strings.HasPrefix(c.Server.Listen, "127.0.0.1:") && !strings.HasPrefix(c.Server.Listen, "[::1]:") {
		return errors.New("server.listen must bind to loopback")
	}
	if strings.TrimSpace(c.Airelay.Binary) == "" {
		return errors.New("airelay.binary is required")
	}
	seenBranches := map[string]string{control: "control"}
	for id, project := range c.Projects {
		if !ValidSlug(id) {
			return fmt.Errorf("invalid project id %q", id)
		}
		if strings.TrimSpace(project.Path) == "" || !filepath.IsAbs(project.Path) {
			return fmt.Errorf("projects.%s.path must be absolute", id)
		}
		if !validRepository(project.Repository) {
			return fmt.Errorf("projects.%s.repository must use owner/name form", id)
		}
		if !ValidSlug(project.SessionKey) {
			return fmt.Errorf("projects.%s.session_key is invalid", id)
		}
		if !ValidSlug(project.AirelayProfile) {
			return fmt.Errorf("projects.%s.airelay_profile is invalid", id)
		}
		branch, err := ExpandBranchPattern(c.Bus.ProjectBranchPattern, c.Gateway.ID, id)
		if err != nil {
			return fmt.Errorf("projects.%s bus branch: %w", id, err)
		}
		if branch == c.Bus.TemplateBranch || branch == control {
			return fmt.Errorf("projects.%s bus branch collides with another reserved branch", id)
		}
		if owner, exists := seenBranches[branch]; exists {
			return fmt.Errorf("projects.%s bus branch collides with %s", id, owner)
		}
		seenBranches[branch] = "project " + id
		for _, arg := range project.LaunchArgs {
			if strings.ContainsAny(arg, "\x00\r\n") {
				return fmt.Errorf("projects.%s.launch_args contains an invalid character", id)
			}
		}
	}
	return nil
}

func (c *Config) ControlBranch() (string, error) {
	return ExpandBranchPattern(c.Bus.ControlBranchPattern, c.Gateway.ID, "")
}

func (c *Config) ProjectBranch(projectID string) (string, error) {
	if _, ok := c.Projects[projectID]; !ok {
		return "", fmt.Errorf("project %s is not configured", projectID)
	}
	return ExpandBranchPattern(c.Bus.ProjectBranchPattern, c.Gateway.ID, projectID)
}

func ExpandBranchPattern(pattern, gatewayID, projectID string) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", errors.New("branch pattern is required")
	}
	result := pattern
	for {
		start := strings.IndexByte(result, '{')
		if start < 0 {
			break
		}
		endRel := strings.IndexByte(result[start:], '}')
		if endRel < 0 {
			return "", errors.New("unterminated template variable")
		}
		end := start + endRel
		name := result[start+1 : end]
		var value string
		switch name {
		case "gateway_id":
			value = gatewayID
		case "project_id":
			if projectID == "" {
				return "", errors.New("project_id variable is not allowed in this branch pattern")
			}
			value = projectID
		default:
			return "", fmt.Errorf("unknown template variable %q", name)
		}
		result = result[:start] + value + result[end+1:]
	}
	if strings.ContainsAny(result, "{}") {
		return "", errors.New("malformed branch template")
	}
	if err := ValidateBranchName(result); err != nil {
		return "", err
	}
	return result, nil
}

func ValidateBranchName(value string) error {
	if value == "" || value == "HEAD" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return errors.New("invalid Git branch name")
	}
	if strings.Contains(value, "//") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, "\\ ~^:?*") || strings.IndexFunc(value, func(r rune) bool { return r <= 0x20 || r == 0x7f }) >= 0 {
		return errors.New("invalid Git branch name")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return errors.New("invalid Git branch segment")
		}
	}
	return nil
}

func (c *Config) Layout(configPath string) (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, fmt.Errorf("resolve home directory: %w", err)
	}
	root := filepath.Join(home, ".gpt-github-gateway", c.Gateway.ID)
	busRoot := filepath.Join(root, "bus")
	return Layout{
		Root:           root,
		BusDir:         busRoot,
		BusMirrorDir:   filepath.Join(busRoot, "mirror.git"),
		BusControlDir:  filepath.Join(busRoot, "control"),
		BusProjectsDir: filepath.Join(busRoot, "projects"),
		PIDFile:        filepath.Join(root, "daemon.pid"),
		LogFile:        filepath.Join(root, "gateway.log"),
		LockFile:       filepath.Join(root, "daemon.lock"),
		GatewayID:      c.Gateway.ID,
		ConfigPath:     configPath,
	}, nil
}

func (l Layout) ProjectRoot(projectID string) string { return filepath.Join(l.Root, projectID) }
func (l Layout) TaskRoot(projectID, taskID string) string {
	return filepath.Join(l.ProjectRoot(projectID), "tasks", taskID)
}
func (l Layout) WorktreeRoot(projectID, taskID string) string {
	return filepath.Join(l.ProjectRoot(projectID), "worktrees", taskID)
}
func (l Layout) BusProjectDir(projectID string) string {
	return filepath.Join(l.BusProjectsDir, projectID)
}

func ValidSlug(value string) bool { return slugPattern.MatchString(value) }

func validRepository(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && ValidSlug(strings.ToLower(parts[0])) && ValidSlug(strings.ToLower(parts[1]))
}

func ProjectIDs(cfg *Config) []string {
	ids := make([]string, 0, len(cfg.Projects))
	for id := range cfg.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return nil
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON data")
	}
	return nil
}
