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
		result, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit, ProdRepo: prodRepo, LatestMarker: "latest"}, host, reg)
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

	t.Run("publish with custom latest marker resolves and moves its own pointer", func(t *testing.T) {
		const (
			backportCommit  = "b1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
			backportVersion = "1.10.1"
			marker          = "latestv1"
		)
		backportSrcRef := fmt.Sprintf("%s:%s", stagingBase, PromotedTagFor(backportCommit, backportVersion))
		backportDigest := pushImage(t, backportSrcRef, name.Insecure)
		tagAs(t, backportSrcRef, fmt.Sprintf("%s:%s", stagingBase, promotedTagFor(marker)), name.Insecure)

		result, err := Publish(PublishInputs{StagingImage: stagingBase, ProdRepo: prodRepo, LatestMarker: marker}, host, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Commit != backportCommit {
			t.Errorf("commit: got %q, want %q", result.Commit, backportCommit)
		}
		if result.Version != backportVersion {
			t.Errorf("version: got %q, want %q", result.Version, backportVersion)
		}

		// The custom marker tag must point at the backport image.
		markerRef, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, prodRepo, marker), name.Insecure)
		if err != nil {
			t.Fatalf("parse marker tag: %v", err)
		}
		desc, err := remote.Get(markerRef)
		if err != nil {
			t.Fatalf("marker tag %s not found: %v", marker, err)
		}
		if desc.Digest.String() != backportDigest {
			t.Errorf("marker tag digest got %q, want %q", desc.Digest, backportDigest)
		}

		// The default :latest tag from the earlier subtest must be untouched
		// (still pointing at the newer, non-backport release).
		latestRef, err := name.NewTag(fmt.Sprintf("%s/%s:latest", host, prodRepo), name.Insecure)
		if err != nil {
			t.Fatalf("parse latest tag: %v", err)
		}
		latestDesc, err := remote.Get(latestRef)
		if err != nil {
			t.Fatalf("latest tag not found: %v", err)
		}
		if latestDesc.Digest.String() != srcDigest {
			t.Errorf(":latest digest got %q, want %q (should be untouched by backport publish)", latestDesc.Digest, srcDigest)
		}
	})

	t.Run("publish resolves latest when commit omitted", func(t *testing.T) {
		result, err := Publish(PublishInputs{StagingImage: stagingBase, ProdRepo: prodRepo, LatestMarker: "latest"}, host, reg)
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
		result, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit, ProdRepo: prodRepo, LatestMarker: "latest", DryRun: true}, host, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Version != version {
			t.Errorf("version: got %q, want %q", result.Version, version)
		}
	})

	t.Run("staging-image required", func(t *testing.T) {
		_, err := Publish(PublishInputs{Commit: commit, ProdRepo: prodRepo}, host, reg)
		if err == nil || !strings.Contains(err.Error(), "staging-image") {
			t.Errorf("expected error containing %q, got %v", "staging-image", err)
		}
	})

	t.Run("prod-repo required", func(t *testing.T) {
		_, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit}, host, reg)
		if err == nil || !strings.Contains(err.Error(), "prod-repo") {
			t.Errorf("expected error containing %q, got %v", "prod-repo", err)
		}
	})

	t.Run("unknown commit returns error", func(t *testing.T) {
		_, err := Publish(PublishInputs{
			StagingImage: stagingBase,
			Commit:       "0000000000000000000000000000000000000000",
			ProdRepo:     prodRepo,
			LatestMarker: "latest",
		}, host, reg)
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

		result, err := Publish(PublishInputs{StagingImage: multiBase, ProdRepo: prodRepo, LatestMarker: "latest"}, host, reg)
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

		_, err := Publish(PublishInputs{StagingImage: orphanBase, ProdRepo: prodRepo, LatestMarker: "latest"}, host, reg)
		if err == nil || !strings.Contains(err.Error(), "matches promoted-latest") {
			t.Errorf("expected 'matches promoted-latest' error, got %v", err)
		}
	})
}

func TestPublishRefusesStompedVersionTag(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	const (
		stagingRepo = "myorg/staging/stomp"
		prodRepo    = "myorg/stomp"
		commit      = "abc1234"
		version     = "1.9.0"
	)
	stagingBase := fmt.Sprintf("%s/%s", host, stagingRepo)
	srcRef := fmt.Sprintf("%s:%s", stagingBase, PromotedTagFor(commit, version))
	pushImage(t, srcRef, name.Insecure)

	// Production already has a DIFFERENT image at the immutable :version tag.
	prodBase := fmt.Sprintf("%s/%s", host, prodRepo)
	pushImage(t, fmt.Sprintf("%s:%s", prodBase, version), name.Insecure)

	reg := DefaultRegistryConnector(srv.URL)

	t.Run("refused without force", func(t *testing.T) {
		_, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit, ProdRepo: prodRepo, LatestMarker: "latest"}, host, reg)
		if err == nil || !strings.Contains(err.Error(), "already exists at a different digest") {
			t.Fatalf("expected stomp error, got %v", err)
		}
	})

	t.Run("proceeds with force", func(t *testing.T) {
		result, err := Publish(PublishInputs{StagingImage: stagingBase, Commit: commit, ProdRepo: prodRepo, LatestMarker: "latest", Force: true}, host, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Warnings) == 0 {
			t.Error("expected a warning when force-overwriting a conflicting tag")
		}
		ref, _ := name.NewTag(fmt.Sprintf("%s:%s", prodBase, version), name.Insecure)
		desc, err := remote.Get(ref)
		if err != nil {
			t.Fatalf("get prod tag: %v", err)
		}
		srcRefParsed, _ := name.NewTag(srcRef, name.Insecure)
		srcDesc, err := remote.Get(srcRefParsed)
		if err != nil {
			t.Fatalf("get src: %v", err)
		}
		if desc.Digest.String() != srcDesc.Digest.String() {
			t.Errorf("prod tag digest got %q, want %q (forced overwrite)", desc.Digest, srcDesc.Digest)
		}
	})
}

func TestPublishRequiresLatestMarker(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	const stagingRepo = "myorg/staging/myimage"
	const prodRepo = "myorg/myimage"
	stagingBase := fmt.Sprintf("%s/%s", host, stagingRepo)

	_, err := Publish(PublishInputs{
		StagingImage: stagingBase,
		ProdRepo:     prodRepo,
	}, host, DefaultRegistryConnector(srv.URL))
	if err == nil || !strings.Contains(err.Error(), "latest-marker is required") {
		t.Fatalf("expected 'latest-marker is required' error, got %v", err)
	}
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
