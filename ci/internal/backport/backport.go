// Package backport models the backporting-branch chain declared in
// ci/backporting.yaml and answers the one question the backporting automation
// needs: given the branch a fix merged into, which branch does it flow to next.
//
// The automation is triggered by a merge event that already carries the branch
// name, so no git/ancestry discovery is required here; the logic is pure and
// fully unit-testable from the parsed config alone.
package backport

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ErrUntracked is returned when a branch is not present in the config.
var ErrUntracked = errors.New("branch is not a tracked backporting branch")

// Branch is a single entry in the backporting chain. Order in the config
// defines the chain: a fix merged into one branch is backported to the next.
type Branch struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Version     string `yaml:"version" json:"version"`
	// InternalOnly marks branches used only for testing the automation; they
	// are still part of the chain but automation may choose not to open real PRs.
	InternalOnly bool `yaml:"internal-only" json:"internal-only"`
}

// Config is the parsed backporting.yaml. Branches are ordered newest-first
// (master, then progressively older maintenance branches).
type Config struct {
	Branches []Branch `yaml:"branches"`
}

// Parse decodes and validates a backporting.yaml document.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing backporting config: %w", err)
	}
	if len(cfg.Branches) == 0 {
		return nil, errors.New("backporting config declares no branches")
	}
	seen := make(map[string]struct{}, len(cfg.Branches))
	for i, b := range cfg.Branches {
		if b.Name == "" {
			return nil, fmt.Errorf("branch at index %d has no name", i)
		}
		if _, dup := seen[b.Name]; dup {
			return nil, fmt.Errorf("duplicate branch name %q", b.Name)
		}
		seen[b.Name] = struct{}{}
	}
	return &cfg, nil
}

// NextBranch returns the full entry a fix in `branch` should be backported to
// next. It returns nil (and no error) when `branch` is the last in the chain,
// and wraps ErrUntracked when `branch` is not tracked in the config.
func (c *Config) NextBranch(branch string) (*Branch, error) {
	for i, b := range c.Branches {
		if b.Name == branch {
			if i+1 < len(c.Branches) {
				return &c.Branches[i+1], nil
			}
			return nil, nil
		}
	}
	return nil, fmt.Errorf("%q: %w", branch, ErrUntracked)
}

// Next returns the name of the branch NextBranch resolves to, or "" when
// `branch` is the last in the chain.
func (c *Config) Next(branch string) (string, error) {
	next, err := c.NextBranch(branch)
	if err != nil || next == nil {
		return "", err
	}
	return next.Name, nil
}
