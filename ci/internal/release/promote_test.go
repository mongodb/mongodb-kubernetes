package release

import (
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestPromote(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	const repo = "myorg/myimage"

	srcRef := fmt.Sprintf("%s/%s:nightly-abc1234", host, repo)
	srcDigest := pushImage(t, srcRef, name.Insecure)

	tests := []struct {
		name    string
		inputs  PromoteInputs
		wantErr string
	}{
		{
			name:   "happy path",
			inputs: PromoteInputs{Image: srcRef, Commit: "abc1234", Version: "1.9.0", Repo: repo},
		},
		{
			name:    "image required",
			inputs:  PromoteInputs{Commit: "abc1234", Version: "1.9.0", Repo: repo},
			wantErr: "image",
		},
		{
			name:    "commit required",
			inputs:  PromoteInputs{Image: srcRef, Version: "1.9.0", Repo: repo},
			wantErr: "commit",
		},
		{
			name:    "version required",
			inputs:  PromoteInputs{Image: srcRef, Commit: "abc1234", Repo: repo},
			wantErr: "version",
		},
		{
			name:    "repo required",
			inputs:  PromoteInputs{Image: srcRef, Commit: "abc1234", Version: "1.9.0"},
			wantErr: "repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags, err := Promote(tt.inputs, DefaultRegistryConnector(srv.URL))

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, tag := range tags {
				ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repo, tag), name.Insecure)
				if err != nil {
					t.Errorf("parse tag %s: %v", tag, err)
					continue
				}
				desc, err := remote.Get(ref)
				if err != nil {
					t.Errorf("tag %s not found after promote: %v", tag, err)
					continue
				}
				if desc.Digest.String() != srcDigest {
					t.Errorf("tag %s: digest got %q, want %q", tag, desc.Digest, srcDigest)
				}
			}
		})
	}
}

func TestPromoteDryRun(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	const repo = "myorg/myimage"

	srcRef := fmt.Sprintf("%s/%s:nightly-abc1234", host, repo)
	pushImage(t, srcRef, name.Insecure)

	tags, err := Promote(PromoteInputs{
		Image:   srcRef,
		Commit:  "abc1234",
		Version: "9.9.9",
		Repo:    repo,
		DryRun:  true,
	}, DefaultRegistryConnector(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{PromotedTagFor("abc1234", "9.9.9"), promotedLatestTag()}
	if !slices.Equal(tags, want) {
		t.Errorf("tags: got %v, want %v", tags, want)
	}

	// Dry-run must not write anything to the registry.
	for _, tag := range tags {
		ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repo, tag), name.Insecure)
		if err != nil {
			t.Fatalf("parse tag %s: %v", tag, err)
		}
		if _, err := remote.Get(ref); err == nil {
			t.Errorf("tag %s should not exist after dry-run", tag)
		}
	}
}
