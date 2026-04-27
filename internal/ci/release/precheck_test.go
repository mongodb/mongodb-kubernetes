package release

import (
	"strings"
	"testing"
)

func TestPreflightInputs_Validate(t *testing.T) {
	happy := PreflightInputs{
		Branch:         "release/mck-1.8.0",
		WorktreeClean:  true,
		ReleaseJSONVer: "1.8.0",
		WantVersion:    "1.8.0",
	}

	tests := []struct {
		name    string
		mutate  func(*PreflightInputs)
		wantErr string
	}{
		{
			name:    "happy path",
			mutate:  func(*PreflightInputs) {},
			wantErr: "",
		},
		{
			name:    "empty branch",
			mutate:  func(p *PreflightInputs) { p.Branch = "" },
			wantErr: "could not determine current branch",
		},
		{
			name:    "branch is master",
			mutate:  func(p *PreflightInputs) { p.Branch = "master" },
			wantErr: "cannot open a release PR from master",
		},
		{
			name:    "branch matches protected pattern",
			mutate:  func(p *PreflightInputs) { p.Branch = "release-1.8.0" },
			wantErr: "protected pattern 'release-*'",
		},
		{
			name:    "release-foo also protected",
			mutate:  func(p *PreflightInputs) { p.Branch = "release-foo" },
			wantErr: "protected pattern",
		},
		{
			name:    "worktree dirty",
			mutate:  func(p *PreflightInputs) { p.WorktreeClean = false },
			wantErr: "uncommitted changes",
		},
		{
			name:    "empty want version",
			mutate:  func(p *PreflightInputs) { p.WantVersion = "" },
			wantErr: "target version must not be empty",
		},
		{
			name: "release.json mismatch",
			mutate: func(p *PreflightInputs) {
				p.ReleaseJSONVer = "1.7.0"
			},
			wantErr: `release.json mongodbOperator is "1.7.0", expected "1.8.0"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := happy
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}
