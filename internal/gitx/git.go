package gitx

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rceman/gpt-github-gateway/internal/execx"
)

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type Git struct {
	Binary string
}

func New() Git {
	return Git{Binary: "git"}
}

func (g Git) Run(ctx context.Context, cwd string, args ...string) (execx.Result, error) {
	return execx.Run(ctx, cwd, g.Binary, args...)
}

func (g Git) VerifyRepository(ctx context.Context, path, expected string) error {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		result, gitErr := g.Run(ctx, path, "rev-parse", "--is-inside-work-tree")
		if gitErr != nil || strings.TrimSpace(result.Stdout) != "true" {
			return fmt.Errorf("%s is not a Git worktree", path)
		}
	}
	result, err := g.Run(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	actual := NormalizeRepository(strings.TrimSpace(result.Stdout))
	if actual != strings.ToLower(expected) {
		return fmt.Errorf("repository identity mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func NormalizeRepository(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".git")
	if strings.HasPrefix(value, "git@github.com:") {
		return strings.ToLower(strings.TrimPrefix(value, "git@github.com:"))
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host == "github.com" {
		return strings.ToLower(strings.TrimPrefix(parsed.Path, "/"))
	}
	return strings.ToLower(value)
}

func (g Git) VerifyCommit(ctx context.Context, repo, sha string) error {
	if !shaPattern.MatchString(sha) {
		return fmt.Errorf("invalid commit SHA %q", sha)
	}
	_, err := g.Run(ctx, repo, "rev-parse", "--verify", sha+"^{commit}")
	return err
}

func (g Git) CreateWorktree(ctx context.Context, repo, worktree, branch, base string) error {
	if !strings.HasPrefix(branch, "agent/") {
		return fmt.Errorf("result branch must use agent/ prefix")
	}
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(worktree); err == nil {
		return fmt.Errorf("worktree already exists: %s", worktree)
	}
	_, err := g.Run(ctx, repo, "worktree", "add", "-b", branch, worktree, base)
	return err
}

func (g Git) BackupRef(ctx context.Context, repo, taskID, base string) error {
	_, err := g.Run(ctx, repo, "update-ref", "refs/gpt-gateway/backups/"+taskID, base)
	return err
}

func (g Git) RemoveWorktree(ctx context.Context, repo, worktree string) error {
	_, err := g.Run(ctx, repo, "worktree", "remove", "--force", worktree)
	return err
}

func (g Git) DeleteBranch(ctx context.Context, repo, branch string) error {
	_, err := g.Run(ctx, repo, "branch", "-D", branch)
	return err
}

func (g Git) ApplyPatch(ctx context.Context, worktree, patch string) error {
	if _, err := g.Run(ctx, worktree, "apply", "--check", "--", patch); err != nil {
		return fmt.Errorf("patch check failed: %w", err)
	}
	if _, err := g.Run(ctx, worktree, "apply", "--3way", "--", patch); err != nil {
		return fmt.Errorf("patch apply failed: %w", err)
	}
	return nil
}

type Scope struct {
	Created  []string
	Modified []string
	Deleted  []string
}

func (g Git) ScopeFromBase(ctx context.Context, worktree, base string) (Scope, error) {
	result, err := g.Run(ctx, worktree, "diff", "--name-status", "-z", "--find-renames", "--find-copies", base, "--")
	if err != nil {
		return Scope{}, err
	}
	fields := strings.Split(result.Stdout, "\x00")
	scope := Scope{}
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		if status == "" {
			continue
		}
		code := status[:1]
		if code == "R" || code == "C" {
			if index+1 >= len(fields) {
				return Scope{}, fmt.Errorf("malformed rename/copy status")
			}
			oldPath := fields[index]
			newPath := fields[index+1]
			index += 2
			if code == "R" {
				scope.Deleted = append(scope.Deleted, oldPath)
			}
			scope.Created = append(scope.Created, newPath)
			continue
		}
		if index >= len(fields) {
			return Scope{}, fmt.Errorf("malformed status %q", status)
		}
		path := fields[index]
		index++
		switch code {
		case "A":
			scope.Created = append(scope.Created, path)
		case "M", "T":
			scope.Modified = append(scope.Modified, path)
		case "D":
			scope.Deleted = append(scope.Deleted, path)
		default:
			return Scope{}, fmt.Errorf("unsupported Git status %q", status)
		}
	}
	untracked, err := g.Run(ctx, worktree, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Scope{}, err
	}
	for _, path := range strings.Split(untracked.Stdout, "\x00") {
		if path != "" {
			scope.Created = append(scope.Created, path)
		}
	}
	sort.Strings(scope.Created)
	sort.Strings(scope.Modified)
	sort.Strings(scope.Deleted)
	return scope, nil
}

func (g Git) Head(ctx context.Context, worktree string) (string, error) {
	result, err := g.Run(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}
