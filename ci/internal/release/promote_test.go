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
			result, err := Promote(tt.inputs, host, DefaultRegistryConnector(srv.URL))

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, tag := range result.Tags {
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

	result, err := Promote(PromoteInputs{
		Image:   srcRef,
		Commit:  "abc1234",
		Version: "9.9.9",
		Repo:    repo,
		DryRun:  true,
	}, host, DefaultRegistryConnector(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{PromotedTagFor("abc1234", "9.9.9"), promotedLatestTag()}
	if !slices.Equal(result.Tags, want) {
		t.Errorf("tags: got %v, want %v", result.Tags, want)
	}

	// Dry-run must not write anything to the registry.
	for _, tag := range result.Tags {
		ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repo, tag), name.Insecure)
		if err != nil {
			t.Fatalf("parse tag %s: %v", tag, err)
		}
		if _, err := remote.Get(ref); err == nil {
			t.Errorf("tag %s should not exist after dry-run", tag)
		}
	}
}

func TestPromoteRefusesStompedTag(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	const repo = "myorg/stomp"

	newSrc := fmt.Sprintf("%s/%s:nightly-new", host, repo)
	pushImage(t, newSrc, name.Insecure)

	// A different image already sits at the exact promoted tag we're about to write.
	pushImage(t, fmt.Sprintf("%s/%s:%s", host, repo, PromotedTagFor("abc1234", "1.9.0")), name.Insecure)

	reg := DefaultRegistryConnector(srv.URL)

	t.Run("refused without force", func(t *testing.T) {
		_, err := Promote(PromoteInputs{Image: newSrc, Commit: "abc1234", Version: "1.9.0", Repo: repo}, host, reg)
		if err == nil || !strings.Contains(err.Error(), "already exists at a different digest") {
			t.Fatalf("expected stomp error, got %v", err)
		}
	})

	t.Run("proceeds with force", func(t *testing.T) {
		result, err := Promote(PromoteInputs{Image: newSrc, Commit: "abc1234", Version: "1.9.0", Repo: repo, Force: true}, host, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ref, _ := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repo, PromotedTagFor("abc1234", "1.9.0")), name.Insecure)
		desc, err := remote.Get(ref)
		if err != nil {
			t.Fatalf("get promoted tag: %v", err)
		}
		newDigest, err := name.NewTag(newSrc, name.Insecure)
		if err != nil {
			t.Fatalf("parse src: %v", err)
		}
		srcDesc, err := remote.Get(newDigest)
		if err != nil {
			t.Fatalf("get src: %v", err)
		}
		if desc.Digest.String() != srcDesc.Digest.String() {
			t.Errorf("promoted tag digest got %q, want %q (forced overwrite)", desc.Digest, srcDesc.Digest)
		}
		if len(result.Warnings) == 0 {
			t.Error("expected a warning when force-overwriting a conflicting tag")
		}
	})
}

func TestPromoteWarnsOnIdempotentRerun(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	const repo = "myorg/idempotent"

	srcRef := fmt.Sprintf("%s/%s:nightly-abc1234", host, repo)
	pushImage(t, srcRef, name.Insecure)

	reg := DefaultRegistryConnector(srv.URL)
	inputs := PromoteInputs{Image: srcRef, Commit: "abc1234", Version: "1.9.0", Repo: repo}

	if _, err := Promote(inputs, host, reg); err != nil {
		t.Fatalf("first promote: unexpected error: %v", err)
	}

	// Re-running promote for the exact same source/commit/version is a safe
	// no-op (same digest): it must not fail, but should surface a warning.
	result, err := Promote(inputs, host, reg)
	if err != nil {
		t.Fatalf("second promote: unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected a warning for a same-digest re-promote")
	}
}
