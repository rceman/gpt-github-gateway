package config

import (
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
	SchemaVersion         = 1
	DefaultConfigRelPath  = ".config/gpt-github-gateway/config.json"
	DefaultPollSeconds    = 10
	DefaultTimeoutSeconds = 7200
	DefaultListen         = "127.0.0.1:8787"
	DefaultAirelayBinary  = "airelay"
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
	MaxTaskFileBytes      int64  `json:"max_task_file_bytes,omitempty"`
	MaxTaskAggregateBytes int64  `json:"max_task_aggregate_bytes,omitempty"`
}

type BusConfig struct {
	Repository string `json:"repository"`
	URL        string `json:"url"`
	Branch     string `json:"branch"`
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
	Root       string
	BusDir     string
	PIDFile    string
	LogFile    string
	LockFile   string
	GatewayID  string
	ConfigPath string
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
	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
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
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
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
	if c.Gateway.MaxTaskFileBytes == 0 {
		c.Gateway.MaxTaskFileBytes = 20 << 20
	}
	if c.Gateway.MaxTaskAggregateBytes == 0 {
		c.Gateway.MaxTaskAggregateBytes = 100 << 20
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
	if c.Gateway.MaxTaskFileBytes < 1024 || c.Gateway.MaxTaskAggregateBytes < c.Gateway.MaxTaskFileBytes {
		return errors.New("invalid task size limits")
	}
	if !validRepository(c.Bus.Repository) {
		return errors.New("bus.repository must use owner/name form")
	}
	if strings.TrimSpace(c.Bus.URL) == "" || strings.TrimSpace(c.Bus.Branch) == "" {
		return errors.New("bus.url and bus.branch are required")
	}
	if c.Server.Listen != DefaultListen && !strings.HasPrefix(c.Server.Listen, "127.0.0.1:") && !strings.HasPrefix(c.Server.Listen, "[::1]:") {
		return errors.New("server.listen must bind to loopback")
	}
	if strings.TrimSpace(c.Airelay.Binary) == "" {
		return errors.New("airelay.binary is required")
	}
	for id, project := range c.Projects {
		if !ValidSlug(id) {
			return fmt.Errorf("invalid project id %q", id)
		}
		if strings.TrimSpace(project.Path) == "" {
			return fmt.Errorf("projects.%s.path is required", id)
		}
		if !filepath.IsAbs(project.Path) {
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
		for _, arg := range project.LaunchArgs {
			if strings.ContainsAny(arg, "\x00\r\n") {
				return fmt.Errorf("projects.%s.launch_args contains an invalid character", id)
			}
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
	return Layout{
		Root:       root,
		BusDir:     filepath.Join(root, "bus"),
		PIDFile:    filepath.Join(root, "daemon.pid"),
		LogFile:    filepath.Join(root, "gateway.log"),
		LockFile:   filepath.Join(root, "daemon.lock"),
		GatewayID:  c.Gateway.ID,
		ConfigPath: configPath,
	}, nil
}

func (l Layout) ProjectRoot(projectID string) string {
	return filepath.Join(l.Root, projectID)
}

func (l Layout) TaskRoot(projectID, taskID string) string {
	return filepath.Join(l.ProjectRoot(projectID), "tasks", taskID)
}

func (l Layout) WorktreeRoot(projectID, taskID string) string {
	return filepath.Join(l.ProjectRoot(projectID), "worktrees", taskID)
}

func ValidSlug(value string) bool {
	return slugPattern.MatchString(value)
}

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
