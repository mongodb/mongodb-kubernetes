package release

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestPublish(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	const (
		stagingRepo = "myorg/staging/myimage"
		prodRepo    = "myorg/myimage"
		commit      = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
		version     = "1.10.0"
	)

	stagingBase := fmt.Sprintf("%s/%s", host, stagingRepo)
	promotedTag := PromotedTagFor(commit, version)

	srcRef := fmt.Sprintf("%s:%s", stagingBase, promotedTag)
	srcDigest := pushImage(t, srcRef, name.Insecure)
	tagAs(t, srcRef, fmt.Sprintf("%s:%s", stagingBase, promotedLatestTag()), name.Insecure)

	reg := DefaultRegistryConnector(srv.URL)

	t.Run("publish with explicit commit", func(t *testing.T) {
		result, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit, ProdRepo: prodRepo}, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Commit != commit {
			t.Errorf("commit: got %q, want %q", result.Commit, commit)
		}
		if result.Version != version {
			t.Errorf("version: got %q, want %q", result.Version, version)
		}
		for _, tag := range result.AppliedTags {
			ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, prodRepo, tag), name.Insecure)
			if err != nil {
				t.Fatalf("parse prod tag %s: %v", tag, err)
			}
			desc, err := remote.Get(ref)
			if err != nil {
				t.Errorf("prod tag %s not found: %v", tag, err)
				continue
			}
			if desc.Digest.String() != srcDigest {
				t.Errorf("prod tag %s: digest got %q, want %q", tag, desc.Digest, srcDigest)
			}
		}
	})

	t.Run("publish resolves latest when commit omitted", func(t *testing.T) {
		result, err := Publish(PublishInputs{StagingImage: stagingBase, ProdRepo: prodRepo}, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Commit != commit {
			t.Errorf("commit: got %q, want %q", result.Commit, commit)
		}
		if result.Version != version {
			t.Errorf("version: got %q, want %q", result.Version, version)
		}
	})

	t.Run("dry-run returns result without copying", func(t *testing.T) {
		result, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit, ProdRepo: prodRepo, DryRun: true}, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Version != version {
			t.Errorf("version: got %q, want %q", result.Version, version)
		}
	})

	t.Run("staging-image required", func(t *testing.T) {
		_, err := Publish(PublishInputs{Commit: commit, ProdRepo: prodRepo}, reg)
		if err == nil || !strings.Contains(err.Error(), "staging-image") {
			t.Errorf("expected error containing %q, got %v", "staging-image", err)
		}
	})

	t.Run("prod-repo required", func(t *testing.T) {
		_, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit}, reg)
		if err == nil || !strings.Contains(err.Error(), "prod-repo") {
			t.Errorf("expected error containing %q, got %v", "prod-repo", err)
		}
	})

	t.Run("unknown commit returns error", func(t *testing.T) {
		_, err := Publish(PublishInputs{
			StagingImage: stagingBase,
			Commit:       "0000000000000000000000000000000000000000",
			ProdRepo:     prodRepo,
		}, reg)
		if err == nil {
			t.Error("expected error for unknown commit, got nil")
		}
	})

	t.Run("resolves the candidate matching promoted-latest among several", func(t *testing.T) {
		// A dedicated staging repo with two distinct promoted candidates
		// (different digests). promoted-latest points at the second one; publish
		// with no --commit must resolve that commit/version, not the other.
		multiBase := fmt.Sprintf("%s/%s", host, "myorg/staging/multi")
		const (
			oldCommit  = "1111111111111111111111111111111111111111"
			oldVersion = "1.0.0"
			newCommit  = "2222222222222222222222222222222222222222"
			newVersion = "2.0.0"
		)
		pushImage(t, fmt.Sprintf("%s:%s", multiBase, PromotedTagFor(oldCommit, oldVersion)), name.Insecure)
		newRef := fmt.Sprintf("%s:%s", multiBase, PromotedTagFor(newCommit, newVersion))
		pushImage(t, newRef, name.Insecure)
		tagAs(t, newRef, fmt.Sprintf("%s:%s", multiBase, promotedLatestTag()), name.Insecure)

		result, err := Publish(PublishInputs{StagingImage: multiBase, ProdRepo: prodRepo}, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Commit != newCommit {
			t.Errorf("commit: got %q, want %q", result.Commit, newCommit)
		}
		if result.Version != newVersion {
			t.Errorf("version: got %q, want %q", result.Version, newVersion)
		}
	})

	t.Run("promoted-latest matching no version tag returns error", func(t *testing.T) {
		// promoted-latest exists but points at a digest no promoted-{c}-{v}
		// version tag shares, so resolution must fail.
		orphanBase := fmt.Sprintf("%s/%s", host, "myorg/staging/orphan")
		pushImage(t, fmt.Sprintf("%s:%s", orphanBase, promotedLatestTag()), name.Insecure)

		_, err := Publish(PublishInputs{StagingImage: orphanBase, ProdRepo: prodRepo}, reg)
		if err == nil || !strings.Contains(err.Error(), "matches promoted-latest") {
			t.Errorf("expected 'matches promoted-latest' error, got %v", err)
		}
	})
}

func TestParsePromotedTag(t *testing.T) {
	const commit = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	tests := []struct {
		tag         string
		wantCommit  string
		wantVersion string
		wantErr     string
	}{
		{
			tag:         "promoted-" + commit + "-1.10.0",
			wantCommit:  commit,
			wantVersion: "1.10.0",
		},
		{
			// Short commits are valid — promote does not require a full 40-char SHA.
			tag:         "promoted-abc1234-1.9.0",
			wantCommit:  "abc1234",
			wantVersion: "1.9.0",
		},
		{
			// Versions may themselves contain dashes; only the first dash splits.
			tag:         "promoted-" + commit + "-1.0.0-rc1",
			wantCommit:  commit,
			wantVersion: "1.0.0-rc1",
		},
		{
			// No version separator at all.
			tag:     "promoted-" + commit,
			wantErr: "malformed",
		},
		{
			// Empty version.
			tag:     "promoted-" + commit + "-",
			wantErr: "malformed",
		},
		{
			// Empty commit.
			tag:     "promoted--1.10.0",
			wantErr: "malformed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			c, v, err := parsePromotedTag(tt.tag)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c != tt.wantCommit {
				t.Errorf("commit: got %q, want %q", c, tt.wantCommit)
			}
			if v != tt.wantVersion {
				t.Errorf("version: got %q, want %q", v, tt.wantVersion)
			}
		})
	}
}

// pushImage pushes a random image to the given reference and returns its digest string.
func pushImage(t *testing.T, ref string, opts ...name.Option) string {
	t.Helper()
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatalf("image digest: %v", err)
	}
	r, err := name.ParseReference(ref, opts...)
	if err != nil {
		t.Fatalf("parse ref %s: %v", ref, err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatalf("push %s: %v", ref, err)
	}
	return d.String()
}

// tagAs creates an additional tag on an existing image.
func tagAs(t *testing.T, existingRef, newRef string, opts ...name.Option) {
	t.Helper()
	src, err := name.ParseReference(existingRef, opts...)
	if err != nil {
		t.Fatalf("parse src ref: %v", err)
	}
	desc, err := remote.Get(src)
	if err != nil {
		t.Fatalf("get %s: %v", existingRef, err)
	}
	dst, err := name.NewTag(newRef, opts...)
	if err != nil {
		t.Fatalf("parse dst tag: %v", err)
	}
	if err := remote.Tag(dst, desc); err != nil {
		t.Fatalf("tag %s: %v", newRef, err)
	}
}
