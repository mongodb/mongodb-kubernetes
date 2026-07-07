package backport_test

import (
	"errors"
	"testing"

	"github.com/mongodb/mongodb-kubernetes/ci/internal/backport"
)

const sampleYAML = `
branches:
  - name: master
    description: Master branch for latest version
    version: 3.x
  - name: v2
    description: Maintenance branch for 2.x
    version: 2.x
  - name: v1
    description: Maintenance branch for 1.x
    version: 1.x
  - name: old-master
    description: Test backporting branch
    version: 0.x
    internal-only: true
`

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
		want    []backport.Branch
	}{
		{
			name: "valid config",
			data: sampleYAML,
			want: []backport.Branch{
				{Name: "master", Description: "Master branch for latest version", Version: "3.x"},
				{Name: "v2", Description: "Maintenance branch for 2.x", Version: "2.x"},
				{Name: "v1", Description: "Maintenance branch for 1.x", Version: "1.x"},
				{Name: "old-master", Description: "Test backporting branch", Version: "0.x", InternalOnly: true},
			},
		},
		{
			name:    "invalid yaml",
			data:    "branches: [this is: not valid",
			wantErr: true,
		},
		{
			name:    "duplicate branch names",
			data:    "branches:\n  - name: master\n    version: 3.x\n  - name: master\n    version: 2.x\n",
			wantErr: true,
		},
		{
			name:    "branch without name",
			data:    "branches:\n  - version: 3.x\n",
			wantErr: true,
		},
		{
			name:    "no branches",
			data:    "branches: []\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := backport.Parse([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() unexpected error: %v", err)
			}
			if len(cfg.Branches) != len(tt.want) {
				t.Fatalf("Parse() got %d branches, want %d", len(cfg.Branches), len(tt.want))
			}
			for i, b := range cfg.Branches {
				if b != tt.want[i] {
					t.Errorf("branch[%d] = %+v, want %+v", i, b, tt.want[i])
				}
			}
		})
	}
}

func TestConfig_NextBranch(t *testing.T) {
	cfg, err := backport.Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}

	tests := []struct {
		name    string
		from    string
		want    *backport.Branch
		wantErr error
	}{
		{
			name: "returns full target entry",
			from: "v1",
			want: &backport.Branch{Name: "old-master", Description: "Test backporting branch", Version: "0.x", InternalOnly: true},
		},
		{name: "last branch returns nil", from: "old-master", want: nil},
		{name: "untracked branch errors", from: "nope", wantErr: backport.ErrUntracked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.NextBranch(tt.from)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NextBranch(%q) error = %v, want %v", tt.from, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NextBranch(%q) unexpected error: %v", tt.from, err)
			}
			if tt.want == nil {
				if got != nil {
					t.Fatalf("NextBranch(%q) = %+v, want nil", tt.from, got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Errorf("NextBranch(%q) = %+v, want %+v", tt.from, got, tt.want)
			}
		})
	}
}

func TestConfig_Next(t *testing.T) {
	cfg, err := backport.Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}

	tests := []struct {
		name    string
		from    string
		want    string
		wantErr error
	}{
		{name: "master to v2", from: "master", want: "v2"},
		{name: "v2 to v1", from: "v2", want: "v1"},
		{name: "v1 to old-master", from: "v1", want: "old-master"},
		{name: "last branch has no next", from: "old-master", want: ""},
		{name: "untracked branch errors", from: "nope", wantErr: backport.ErrUntracked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.Next(tt.from)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Next(%q) error = %v, want %v", tt.from, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Next(%q) unexpected error: %v", tt.from, err)
			}
			if got != tt.want {
				t.Errorf("Next(%q) = %q, want %q", tt.from, got, tt.want)
			}
		})
	}
}
